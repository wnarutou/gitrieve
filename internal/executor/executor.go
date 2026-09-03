package executor

import (
	"context"
	"database/sql"
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
	done       chan struct{}
	repoKey    string
	cancelled  bool
}

type queuedJob struct {
	id   string
	repo typedef.Repository
	ctx  context.Context
	plan []component
}

type preparedJob struct {
	queued            *queuedJob
	jobContext        *JobContext
	executionCreated  bool
	componentsCreated bool
}

type executionStore interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	ActiveExecutionExists(context.Context, string) (bool, error)
	CreatePendingExecutions(context.Context, []db.PendingExecution) error
	StartComponent(context.Context, string, db.ComponentName, time.Time) error
	FinishComponent(context.Context, string, db.ComponentName, db.ComponentStatus, time.Time, string) error
}

type Executor struct {
	db      executionStore
	cfg     atomic.Pointer[config.Config]
	runners Runners

	queueMu           sync.Mutex
	queueCond         *sync.Cond
	pending           []*queuedJob
	jobs              map[string]*JobContext
	activeByRepo      map[string]string
	running           int
	limit             int
	inflight          int
	closed            bool
	dispatcherStarted bool
	dispatcherDone    chan struct{}
	closeDone         chan struct{}
	closeErr          error
	closeWaiters      int
	workers           sync.WaitGroup
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
		db:             db,
		runners:        runners,
		jobs:           make(map[string]*JobContext),
		activeByRepo:   make(map[string]string),
		limit:          concurrencyLimit(cfg),
		dispatcherDone: make(chan struct{}),
		closeDone:      make(chan struct{}),
	}
	exec.queueCond = sync.NewCond(&exec.queueMu)
	exec.cfg.Store(cfg)
	return exec
}

func concurrencyLimit(cfg *config.Config) int {
	if cfg == nil || cfg.ConcurrencyNum == 0 {
		return 3
	}
	maxInt := int(^uint(0) >> 1)
	if cfg.ConcurrencyNum > uint(maxInt) {
		return maxInt
	}
	return int(cfg.ConcurrencyNum)
}

// RefreshConfig repoints the executor at a new config instance. Called by the
// server's config-reload endpoint after the config file is re-read. e.cfg is an
// atomic pointer: concurrent reads (ExecuteJob's repository lookup and the
// async executeAsync goroutine's storage lookup) observe either the old or the
// new config in its entirety, never a torn read, so this is safe to call while
// jobs are running.
func (e *Executor) RefreshConfig(cfg *config.Config) {
	e.queueMu.Lock()
	e.cfg.Store(cfg)
	e.limit = concurrencyLimit(cfg)
	e.queueCond.Broadcast()
	e.queueMu.Unlock()
}

// ErrRepositoryNotFound 表示配置中找不到匹配该身份键的仓库条目。
var ErrRepositoryNotFound = errors.New("repository not found in configuration")
var ErrRepositoryActive = errors.New("repository is already active")
var ErrExecutorClosed = errors.New("executor is closed")

// expandRepos 是把 user/org 条目展开为具体仓库的 seam：生产用 repository.Expand，
// 测试注入 fake，避免真实 GitHub 调用。
var expandRepos = repository.Expand

// ExecuteJob 按仓库身份键（规范化 URL）在配置中定位条目并执行。type=repo 产生
// 一条 execution 并返回单元素 jobID；type=user/org 先在任务内展开为具体仓库，
// 每个具体仓库独立执行（各自 jobID / execution / 日志流 / 可取消）。
func (e *Executor) ExecuteJob(repoKey string) ([]string, error) {
	var repo typedef.Repository
	found := false
	if cfg := e.cfg.Load(); cfg != nil {
		for _, r := range cfg.Repository {
			if r.Matches(repoKey) {
				repo = r
				found = true
				break
			}
		}
	}
	if !found {
		return nil, ErrRepositoryNotFound
	}

	return e.submitBatch(expandRepos(repo))
}

// submitBatch reserves, preflights, and persists every concrete repository
// before atomically publishing any of them to the dispatcher.
func (e *Executor) submitBatch(repositories []typedef.Repository) ([]string, error) {
	prepared := make([]*preparedJob, len(repositories))
	for i, repo := range repositories {
		ctx, cancel := context.WithCancel(context.Background())
		jobID := uuid.New().String()
		plan := componentPlan(repo, e.runners)
		prepared[i] = &preparedJob{
			queued: &queuedJob{id: jobID, repo: repo, ctx: ctx, plan: plan},
			jobContext: &JobContext{
				Ctx:        ctx,
				CancelFunc: cancel,
				done:       make(chan struct{}),
				repoKey:    repo.Key(),
			},
		}
	}

	if err := e.reserveBatch(prepared); err != nil {
		for _, job := range prepared {
			job.jobContext.CancelFunc()
		}
		return nil, err
	}
	if err := e.preflightBatch(prepared); err != nil {
		e.releaseBatch(prepared)
		return nil, err
	}
	if err := e.persistBatch(prepared); err != nil {
		e.releaseBatch(prepared)
		return nil, err
	}

	e.queueMu.Lock()
	if e.closed {
		e.queueMu.Unlock()
		return nil, errors.Join(ErrExecutorClosed, e.abortBatch(prepared, StatusCancelled, context.Canceled.Error()))
	}
	jobIDs := make([]string, len(prepared))
	for i, job := range prepared {
		jobIDs[i] = job.queued.id
		e.jobs[job.queued.id] = job.jobContext
		e.pending = append(e.pending, job.queued)
	}
	e.inflight--
	if len(prepared) != 0 {
		e.startDispatcherLocked()
	}
	e.queueCond.Broadcast()
	e.queueMu.Unlock()
	return jobIDs, nil
}

func (e *Executor) reserveBatch(prepared []*preparedJob) error {
	e.queueMu.Lock()
	defer e.queueMu.Unlock()
	if e.closed {
		return ErrExecutorClosed
	}
	seen := make(map[string]struct{}, len(prepared))
	for _, job := range prepared {
		repoKey := job.jobContext.repoKey
		if _, duplicate := seen[repoKey]; duplicate {
			return ErrRepositoryActive
		}
		seen[repoKey] = struct{}{}
		if _, active := e.activeByRepo[repoKey]; active {
			return ErrRepositoryActive
		}
	}
	for _, job := range prepared {
		e.activeByRepo[job.jobContext.repoKey] = job.queued.id
	}
	e.inflight++
	return nil
}

func (e *Executor) preflightBatch(prepared []*preparedJob) error {
	for _, job := range prepared {
		active, err := e.db.ActiveExecutionExists(context.Background(), job.jobContext.repoKey)
		if err != nil {
			return fmt.Errorf("check active execution: %w", err)
		}
		if active {
			return ErrRepositoryActive
		}
	}
	return nil
}

func (e *Executor) persistBatch(prepared []*preparedJob) error {
	executions := make([]db.PendingExecution, len(prepared))
	for i, job := range prepared {
		componentNames := make([]db.ComponentName, len(job.queued.plan))
		for componentIndex := range job.queued.plan {
			componentNames[componentIndex] = job.queued.plan[componentIndex].name
		}
		executions[i] = db.PendingExecution{
			ID:         job.queued.id,
			JobName:    job.queued.repo.Name,
			RepoKey:    job.jobContext.repoKey,
			StartTime:  time.Now(),
			Components: componentNames,
		}
	}
	if err := e.db.CreatePendingExecutions(context.Background(), executions); err != nil {
		return fmt.Errorf("create pending execution batch: %w", err)
	}
	for _, job := range prepared {
		job.executionCreated = true
		job.componentsCreated = true
	}
	return nil
}

func (e *Executor) abortBatch(prepared []*preparedJob, status ExecutionStatus, message string) error {
	var errs []error
	for _, job := range prepared {
		job.jobContext.CancelFunc()
		if !job.executionCreated {
			continue
		}
		allComponentsTerminal := true
		if job.componentsCreated {
			componentStatus := db.ComponentFailed
			componentMessage := message
			if status == StatusCancelled {
				componentStatus = db.ComponentCancelled
				componentMessage = context.Canceled.Error()
			}
			for _, planned := range job.queued.plan {
				if err := e.db.FinishComponent(context.Background(), job.queued.id, planned.name, componentStatus, time.Now(), componentMessage); err != nil {
					allComponentsTerminal = false
					errs = append(errs, err)
				}
			}
		}
		if allComponentsTerminal {
			executionMessage := message
			if status == StatusCancelled {
				executionMessage = ""
			}
			if err := e.updateJobStatus(job.queued.id, string(status), executionMessage); err != nil {
				errs = append(errs, err)
			}
		}
	}
	e.releaseBatch(prepared)
	return errors.Join(errs...)
}

func (e *Executor) releaseBatch(prepared []*preparedJob) {
	e.queueMu.Lock()
	for _, job := range prepared {
		job.jobContext.CancelFunc()
		if e.activeByRepo[job.jobContext.repoKey] == job.queued.id {
			delete(e.activeByRepo, job.jobContext.repoKey)
		}
	}
	e.inflight--
	e.queueCond.Broadcast()
	e.queueMu.Unlock()
}

func (e *Executor) startDispatcherLocked() {
	if e.dispatcherStarted {
		return
	}
	e.dispatcherStarted = true
	go e.dispatch()
}

func (e *Executor) dispatch() {
	defer close(e.dispatcherDone)
	for {
		e.queueMu.Lock()
		for !e.closed && (len(e.pending) == 0 || e.running >= e.limit) {
			e.queueCond.Wait()
		}
		if e.closed {
			e.queueMu.Unlock()
			return
		}

		job := e.pending[0]
		e.pending[0] = nil
		e.pending = e.pending[1:]
		e.running++
		e.workers.Add(1)
		e.queueMu.Unlock()

		go e.runAdmitted(job)
	}
}

func (e *Executor) runAdmitted(job *queuedJob) {
	defer e.workers.Done()
	defer e.completeRunningJob(job.id, job.repo.Key())

	if err := e.updateJobStatus(job.id, string(StatusRunning), ""); err != nil {
		e.finishAdmissionFailure(job, err)
		return
	}
	e.executeAsync(job.ctx, job.id, job.repo, job.plan)
}

func (e *Executor) finishAdmissionFailure(job *queuedJob, admissionErr error) {
	message := fmt.Sprintf("persist running execution state: %v", admissionErr)
	allTerminal := true
	for _, planned := range job.plan {
		if err := e.db.FinishComponent(context.Background(), job.id, planned.name, db.ComponentFailed, time.Now(), message); err != nil {
			allTerminal = false
			ui.Errorf("Failed to record %s admission failure: %v", planned.name, err)
		}
	}
	if allTerminal {
		e.finishExecution(job.id, StatusFailed, message)
	}
}

func (e *Executor) completeRunningJob(jobID, repoKey string) {
	e.queueMu.Lock()
	jobCtx := e.jobs[jobID]
	delete(e.jobs, jobID)
	if e.activeByRepo[repoKey] == jobID {
		delete(e.activeByRepo, repoKey)
	}
	e.running--
	if jobCtx != nil {
		close(jobCtx.done)
	}
	e.queueCond.Broadcast()
	e.queueMu.Unlock()
}

func (e *Executor) executeAsync(ctx context.Context, jobID string, job typedef.Repository, plan []component) {
	unbind := ui.Bind(jobID, job.Name)
	defer unbind()

	ui.Printf("Starting job execution")

	// Check if context is already cancelled
	if ctx.Err() != nil {
		// Write the final log line BEFORE the terminal status update so the SSE
		// log stream (which emits "done" as soon as it sees a terminal status)
		// does not flush before this row is committed and drop it.
		e.finishCancellation(jobID, plan, 0, ctx.Err())
		return
	}

	// Get storages
	var storages []typedef.MultiStorage
	if cfg := e.cfg.Load(); cfg != nil {
		for _, storageName := range job.Storage {
			for _, s := range cfg.Storage {
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
	}

	failures := make([]string, 0)
	allComponentsTerminal := true
	for i, planned := range plan {
		if ctx.Err() != nil {
			e.finishCancellation(jobID, plan, i, ctx.Err())
			return
		}

		if err := e.db.StartComponent(context.Background(), jobID, planned.name, time.Now()); err != nil {
			message := fmt.Sprintf("persist running state: %v", err)
			failures = append(failures, componentFailure(planned.name, message))
			ui.Errorf("Failed to start %s: %v", planned.name, err)
			if fallbackErr := e.finishAfterStoreFailure(jobID, planned.name, message); fallbackErr != nil {
				fallbackMessage := fmt.Sprintf("persist failed fallback: %v", fallbackErr)
				failures = append(failures, componentFailure(planned.name, fallbackMessage))
				ui.Errorf("Failed to record %s store failure: %v", planned.name, fallbackErr)
				allComponentsTerminal = false
			}
			continue
		}

		if planned.name != db.ComponentCode {
			ui.Printf("Downloading %s", componentLogName(planned.name))
		}
		runErr := planned.run(ctx, job, storages)
		if cancelErr := componentCancellation(ctx); cancelErr != nil {
			e.finishCancellation(jobID, plan, i, cancelErr)
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
			if fallbackErr := e.finishAfterStoreFailure(jobID, planned.name, message); fallbackErr != nil {
				fallbackMessage := fmt.Sprintf("persist failed fallback: %v", fallbackErr)
				failures = append(failures, componentFailure(planned.name, fallbackMessage))
				ui.Errorf("Failed to record %s store failure: %v", planned.name, fallbackErr)
				allComponentsTerminal = false
			}
		}
	}

	if !allComponentsTerminal {
		ui.Errorf("Job cannot be finalized while component rows remain active")
		return
	}
	if len(failures) != 0 {
		e.finishExecution(jobID, StatusFailed, strings.Join(failures, "; "))
		return
	}

	// Final log line before the terminal status update (see note above).
	ui.Printf("Job completed successfully")
	e.finishExecution(jobID, StatusCompleted, "")
}

func componentFailure(name db.ComponentName, message string) string {
	label := string(name)
	if name == db.ComponentIssue {
		label = "issue"
	}
	return fmt.Sprintf("%s: %s", label, message)
}

func componentCancellation(ctx context.Context) error {
	return ctx.Err()
}

func componentLogName(name db.ComponentName) string {
	if name == db.ComponentRelease {
		return "releases"
	}
	return string(name)
}

func (e *Executor) finishCancellation(jobID string, plan []component, first int, cancelErr error) {
	failures, allTerminal := e.cancelComponents(jobID, plan, first, cancelErr)
	ui.Printf("Job was cancelled")
	if !allTerminal {
		ui.Errorf("Job cannot be finalized while component rows remain active")
		return
	}
	if len(failures) != 0 {
		e.finishExecution(jobID, StatusFailed, strings.Join(failures, "; "))
		return
	}
	e.finishExecution(jobID, StatusCancelled, "")
}

func (e *Executor) cancelComponents(jobID string, plan []component, first int, cancelErr error) ([]string, bool) {
	failures := make([]string, 0)
	allTerminal := true
	message := cancelErr.Error()
	for _, planned := range plan[first:] {
		if err := e.db.FinishComponent(context.Background(), jobID, planned.name, db.ComponentCancelled, time.Now(), message); err != nil {
			ui.Errorf("Failed to cancel %s: %v", planned.name, err)
			persistenceMessage := fmt.Sprintf("persist cancelled state: %v", err)
			failures = append(failures, componentFailure(planned.name, persistenceMessage))
			if fallbackErr := e.finishAfterStoreFailure(jobID, planned.name, persistenceMessage); fallbackErr != nil {
				fallbackMessage := fmt.Sprintf("persist failed fallback: %v", fallbackErr)
				failures = append(failures, componentFailure(planned.name, fallbackMessage))
				ui.Errorf("Failed to record %s cancellation store failure: %v", planned.name, fallbackErr)
				allTerminal = false
			}
		}
	}
	return failures, allTerminal
}

func (e *Executor) finishAfterStoreFailure(jobID string, name db.ComponentName, message string) error {
	return e.db.FinishComponent(context.Background(), jobID, name, db.ComponentFailed, time.Now(), message)
}

func (e *Executor) finishExecution(jobID string, status ExecutionStatus, errorMessage string) {
	if err := e.updateJobStatus(jobID, string(status), errorMessage); err != nil {
		ui.Errorf("Failed to finish job as %s: %v", status, err)
		if status == StatusFailed {
			return
		}

		failureMessage := fmt.Sprintf("persist %s execution state: %v", status, err)
		if fallbackErr := e.updateJobStatus(jobID, string(StatusFailed), failureMessage); fallbackErr != nil {
			ui.Errorf("Failed to record overall execution store failure: %v", fallbackErr)
		}
	}
}

func (e *Executor) CancelJob(jobID string) error {
	e.queueMu.Lock()
	if e.closed {
		e.queueMu.Unlock()
		return ErrExecutorClosed
	}
	jobCtx, exists := e.jobs[jobID]
	if !exists {
		// Account the persistence operation so Close cannot let the server close
		// SQLite underneath it, but do not hold queueMu during the DB call.
		e.inflight++
		e.queueMu.Unlock()
		err := e.updateJobStatus(jobID, string(StatusCancelled), "")
		e.queueMu.Lock()
		e.inflight--
		e.queueCond.Broadcast()
		e.queueMu.Unlock()
		return err
	}

	for i, pending := range e.pending {
		if pending.id != jobID {
			continue
		}
		e.pending = append(e.pending[:i], e.pending[i+1:]...)
		e.signalCancellationLocked(jobCtx)
		delete(e.jobs, jobID)
		if e.activeByRepo[jobCtx.repoKey] == jobID {
			delete(e.activeByRepo, jobCtx.repoKey)
		}
		e.workers.Add(1)
		e.queueCond.Broadcast()
		e.queueMu.Unlock()

		defer e.workers.Done()
		e.finishQueuedCancellation(pending)
		close(jobCtx.done)
		return nil
	}

	e.signalCancellationLocked(jobCtx)
	e.queueMu.Unlock()
	return nil
}

func (e *Executor) signalCancellationLocked(jobCtx *JobContext) {
	if jobCtx.cancelled {
		return
	}
	jobCtx.cancelled = true
	jobCtx.CancelFunc()
}

func (e *Executor) finishQueuedCancellation(job *queuedJob) {
	unbind := ui.Bind(job.id, job.repo.Name)
	defer unbind()
	e.finishCancellation(job.id, job.plan, 0, context.Canceled)
}

// Close stops admission, terminalizes queued jobs without invoking their
// runners, signals every running job once, and waits for all executor-owned
// goroutines and persistence work to finish.
func (e *Executor) Close() error {
	e.queueMu.Lock()
	if e.closed {
		e.closeWaiters++
		e.queueCond.Broadcast()
		done := e.closeDone
		e.queueMu.Unlock()
		<-done
		e.queueMu.Lock()
		e.closeWaiters--
		err := e.closeErr
		e.queueMu.Unlock()
		return err
	}

	e.closed = true
	pending := e.pending
	e.pending = nil
	pendingContexts := make(map[string]*JobContext, len(pending))
	for _, job := range pending {
		jobCtx := e.jobs[job.id]
		if jobCtx == nil {
			continue
		}
		pendingContexts[job.id] = jobCtx
		e.signalCancellationLocked(jobCtx)
		delete(e.jobs, job.id)
		if e.activeByRepo[jobCtx.repoKey] == job.id {
			delete(e.activeByRepo, jobCtx.repoKey)
		}
	}
	for _, jobCtx := range e.jobs {
		e.signalCancellationLocked(jobCtx)
	}
	dispatcherStarted := e.dispatcherStarted
	e.queueCond.Broadcast()
	e.queueMu.Unlock()

	for _, job := range pending {
		e.finishQueuedCancellation(job)
		if jobCtx := pendingContexts[job.id]; jobCtx != nil {
			close(jobCtx.done)
		}
	}
	if dispatcherStarted {
		<-e.dispatcherDone
	}
	e.workers.Wait()

	e.queueMu.Lock()
	for e.inflight != 0 {
		e.queueCond.Wait()
	}
	close(e.closeDone)
	err := e.closeErr
	e.queueMu.Unlock()
	return err
}

func (e *Executor) updateJobStatus(jobID string, status string, errorMessage string) error {
	var (
		result interface {
			RowsAffected() (int64, error)
		}
		err error
	)
	if status == string(StatusPending) || status == string(StatusRunning) {
		result, err = e.db.Exec(`
			UPDATE executions SET status = ?, error_message = ?
			WHERE id = ?
		`, status, errorMessage, jobID)
	} else {
		endTime := time.Now()
		result, err = e.db.Exec(`
			UPDATE executions SET status = ?, error_message = ?, end_time = ?
			WHERE id = ?
		`, status, errorMessage, endTime, jobID)
	}
	if err != nil {
		return fmt.Errorf("update execution %q to %q: %w", jobID, status, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update execution %q to %q: inspect affected rows: %w", jobID, status, err)
	}
	if rows != 1 {
		return fmt.Errorf("update execution %q to %q: expected one row, updated %d", jobID, status, rows)
	}
	return nil
}

func (e *Executor) IsJobRunning(jobID string) bool {
	e.queueMu.Lock()
	_, exists := e.jobs[jobID]
	e.queueMu.Unlock()
	return exists
}
