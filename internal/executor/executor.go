package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/syncresult"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
)

type JobContext struct {
	Ctx        context.Context
	CancelFunc context.CancelFunc
}

type Executor struct {
	db          *db.DB
	cfg         atomic.Pointer[config.Config]
	runners     Runners
	runningJobs map[string]*JobContext
	mu          sync.RWMutex
}

func NewExecutor(logger *logger.Logger, db *db.DB, cfg *config.Config) *Executor {
	return NewExecutorWithRunners(logger, db, cfg, defaultRunners())
}

func NewExecutorWithRunners(logger *logger.Logger, db *db.DB, cfg *config.Config, runners Runners) *Executor {
	// Side effect: re-points the package-global ui sink so executor log output
	// is persisted to the DB. Only the server process constructs an Executor;
	// the CLI and daemon never do, so their stdout-only ui output is unchanged.
	if logger != nil {
		ui.SetSink(logger)
	}
	exec := &Executor{
		db:          db,
		runners:     runners,
		runningJobs: make(map[string]*JobContext),
	}
	exec.cfg.Store(cfg)
	return exec
}

// RefreshConfig repoints the executor at a new config instance. Called by the
// server's config-reload endpoint after the config file is re-read. e.cfg is an
// atomic pointer: concurrent reads (ExecuteJob's repository lookup and the
// async executeAsync goroutine's storage lookup) observe either the old or the
// new config in its entirety, never a torn read, so this is safe to call while
// jobs are running.
func (e *Executor) RefreshConfig(cfg *config.Config) {
	e.cfg.Store(cfg)
}

// ErrRepositoryNotFound 表示配置中找不到匹配该身份键的仓库条目。
var ErrRepositoryNotFound = errors.New("repository not found in configuration")

// expandRepos 是把 user/org 条目展开为具体仓库的 seam：生产用 repository.Expand，
// 测试注入 fake，避免真实 GitHub 调用。
var expandRepos = repository.Expand

// ExecuteJob 按仓库身份键（规范化 URL）在配置中定位条目并执行。type=repo 产生
// 一条 execution 并返回单元素 jobID；type=user/org 先在任务内展开为具体仓库，
// 每个具体仓库独立执行（各自 jobID / execution / 日志流 / 可取消）。
func (e *Executor) ExecuteJob(repoKey string) ([]string, error) {
	var repo typedef.Repository
	found := false
	for _, r := range e.cfg.Load().Repository {
		if r.Matches(repoKey) {
			repo = r
			found = true
			break
		}
	}
	if !found {
		return nil, ErrRepositoryNotFound
	}

	jobIDs := make([]string, 0, 1)
	for _, concrete := range expandRepos(repo) {
		jobID, err := e.launchJob(concrete)
		if err != nil {
			return nil, err
		}
		jobIDs = append(jobIDs, jobID)
	}
	return jobIDs, nil
}

// launchJob 为单个具体仓库创建 execution 记录并异步执行。
func (e *Executor) launchJob(job typedef.Repository) (string, error) {
	// Generate job ID
	jobID := uuid.New().String()
	startTime := time.Now()
	plan := componentPlan(job, e.runners)

	// Create execution record: job_name 保存展示名快照，repo_key 是身份键。
	_, err := e.db.Exec(`
		INSERT INTO executions (id, job_name, repo_key, start_time, status)
		VALUES (?, ?, ?, ?, ?)
	`, jobID, job.Name, job.Key(), startTime, string(StatusPending))
	if err != nil {
		return "", fmt.Errorf("failed to create execution record: %w", err)
	}

	componentNames := make([]db.ComponentName, len(plan))
	for i := range plan {
		componentNames[i] = plan[i].name
	}
	if err := e.db.CreateComponents(context.Background(), jobID, componentNames); err != nil {
		message := fmt.Sprintf("create component records: %v", err)
		if statusErr := e.updateJobStatus(jobID, string(StatusFailed), message); statusErr != nil {
			return "", fmt.Errorf("%s; mark execution failed: %w", message, statusErr)
		}
		return "", errors.New(message)
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Store job context
	e.mu.Lock()
	e.runningJobs[jobID] = &JobContext{
		Ctx:        ctx,
		CancelFunc: cancel,
	}
	e.mu.Unlock()

	// Update status to running
	e.updateJobStatus(jobID, string(StatusRunning), "")

	// Execute async
	go e.executeAsync(ctx, jobID, job, plan)

	return jobID, nil
}

func (e *Executor) executeAsync(ctx context.Context, jobID string, job typedef.Repository, plan []component) {
	unbind := ui.Bind(jobID, job.Name)
	defer unbind()

	defer func() {
		e.mu.Lock()
		delete(e.runningJobs, jobID)
		e.mu.Unlock()
	}()

	ui.Printf("Starting job execution")

	// Check if context is already cancelled
	if ctx.Err() != nil {
		// Write the final log line BEFORE the terminal status update so the SSE
		// log stream (which emits "done" as soon as it sees a terminal status)
		// does not flush before this row is committed and drop it.
		ui.Printf("Job was cancelled")
		e.cancelComponents(jobID, plan, 0, ctx.Err())
		e.updateJobStatus(jobID, string(StatusCancelled), "")
		return
	}

	// Get storages
	var storages []typedef.MultiStorage
	for _, storageName := range job.Storage {
		for _, s := range e.cfg.Load().Storage {
			if s.Name == storageName {
				storages = append(storages, typedef.MultiStorage{
					Storage: typedef.Storage{
						Name: s.Name,
						Type: s.Type,
						Path: s.Path,
					},
				})
				break
			}
		}
	}

	failures := make([]string, 0)
	for i, planned := range plan {
		if ctx.Err() != nil {
			e.cancelComponents(jobID, plan, i, ctx.Err())
			ui.Printf("Job was cancelled")
			e.updateJobStatus(jobID, string(StatusCancelled), "")
			return
		}

		if err := e.db.StartComponent(context.Background(), jobID, planned.name, time.Now()); err != nil {
			message := fmt.Sprintf("persist running state: %v", err)
			failures = append(failures, componentFailure(planned.name, message))
			ui.Errorf("Failed to start %s: %v", planned.name, err)
			e.finishAfterStoreFailure(jobID, planned.name, message)
			continue
		}

		if planned.name != db.ComponentCode {
			ui.Printf("Downloading %s", componentLogName(planned.name))
		}
		runErr := planned.run(ctx, job, storages)
		if cancelErr := componentCancellation(ctx, runErr); cancelErr != nil {
			e.cancelComponents(jobID, plan, i, cancelErr)
			ui.Printf("Job was cancelled")
			e.updateJobStatus(jobID, string(StatusCancelled), "")
			return
		}

		status := db.ComponentCompleted
		errorMessage := ""
		if reason, skipped := syncresult.SkippedReason(runErr); skipped {
			status = db.ComponentSkipped
			errorMessage = reason
			ui.Printf("Skipping %s: %s", componentLogName(planned.name), reason)
		} else if runErr != nil {
			status = db.ComponentFailed
			errorMessage = runErr.Error()
			failures = append(failures, componentFailure(planned.name, errorMessage))
			ui.Errorf("Failed to download %s: %v", componentLogName(planned.name), runErr)
		}

		if err := e.db.FinishComponent(context.Background(), jobID, planned.name, status, time.Now(), errorMessage); err != nil {
			message := fmt.Sprintf("persist %s state: %v", status, err)
			failures = append(failures, componentFailure(planned.name, message))
			ui.Errorf("Failed to finish %s: %v", planned.name, err)
			e.finishAfterStoreFailure(jobID, planned.name, message)
		}
	}

	if len(failures) != 0 {
		e.updateJobStatus(jobID, string(StatusFailed), strings.Join(failures, "; "))
		return
	}

	// Final log line before the terminal status update (see note above).
	ui.Printf("Job completed successfully")
	e.updateJobStatus(jobID, string(StatusCompleted), "")
}

func componentFailure(name db.ComponentName, message string) string {
	label := string(name)
	if name == db.ComponentIssue {
		label = "issue"
	}
	return fmt.Sprintf("%s: %s", label, message)
}

func componentCancellation(ctx context.Context, runErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	return nil
}

func componentLogName(name db.ComponentName) string {
	if name == db.ComponentRelease {
		return "releases"
	}
	return string(name)
}

func (e *Executor) cancelComponents(jobID string, plan []component, first int, cancelErr error) {
	message := cancelErr.Error()
	for _, planned := range plan[first:] {
		if err := e.db.FinishComponent(context.Background(), jobID, planned.name, db.ComponentCancelled, time.Now(), message); err != nil {
			ui.Errorf("Failed to cancel %s: %v", planned.name, err)
		}
	}
}

func (e *Executor) finishAfterStoreFailure(jobID string, name db.ComponentName, message string) {
	if err := e.db.FinishComponent(context.Background(), jobID, name, db.ComponentFailed, time.Now(), message); err != nil {
		ui.Errorf("Failed to record %s store failure: %v", name, err)
	}
}

func (e *Executor) CancelJob(jobID string) error {
	e.mu.RLock()
	jobCtx, exists := e.runningJobs[jobID]
	e.mu.RUnlock()

	if !exists {
		// Job might not be running, try to update status anyway
		return e.updateJobStatus(jobID, string(StatusCancelled), "")
	}

	jobCtx.CancelFunc()
	return nil
}

func (e *Executor) updateJobStatus(jobID string, status string, errorMessage string) error {
	var err error
	if status == string(StatusPending) || status == string(StatusRunning) {
		_, err = e.db.Exec(`
			UPDATE executions SET status = ?, error_message = ?
			WHERE id = ?
		`, status, errorMessage, jobID)
	} else {
		endTime := time.Now()
		_, err = e.db.Exec(`
			UPDATE executions SET status = ?, error_message = ?, end_time = ?
			WHERE id = ?
		`, status, errorMessage, endTime, jobID)
	}

	return err
}

func (e *Executor) IsJobRunning(jobID string) bool {
	e.mu.RLock()
	_, exists := e.runningJobs[jobID]
	e.mu.RUnlock()
	return exists
}
