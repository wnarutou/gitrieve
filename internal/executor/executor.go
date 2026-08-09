package executor

import (
	"context"
	"fmt"
	"sync"
	"time"
	"github.com/google/uuid"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/repository"
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
	cfg         *config.Config
	runningJobs map[string]*JobContext
	mu          sync.RWMutex
}

func NewExecutor(logger *logger.Logger, db *db.DB, cfg *config.Config) *Executor {
	if logger != nil {
		ui.SetSink(logger)
	}
	return &Executor{
		db:          db,
		cfg:         cfg,
		runningJobs: make(map[string]*JobContext),
	}
}

func (e *Executor) ExecuteJob(jobName string) (string, error) {
	// Find repository in config
	var job typedef.Repository
	found := false
	for _, repo := range e.cfg.Repository {
		if repo.Name == jobName {
			job = repo
			found = true
			break
		}
	}

	if !found {
		return "", fmt.Errorf("repository %s not found in configuration", jobName)
	}

	// Generate job ID
	jobID := uuid.New().String()
	startTime := time.Now()

	// Create execution record
	_, err := e.db.Exec(`
		INSERT INTO executions (id, job_name, start_time, status)
		VALUES (?, ?, ?, ?)
	`, jobID, jobName, startTime, string(StatusPending))
	if err != nil {
		return "", fmt.Errorf("failed to create execution record: %w", err)
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
	go e.executeAsync(ctx, jobID, job)

	return jobID, nil
}

func (e *Executor) executeAsync(ctx context.Context, jobID string, job typedef.Repository) {
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
		e.updateJobStatus(jobID, string(StatusCancelled), "")
		ui.Printf("Job was cancelled")
		return
	}

	// Get storages
	var storages []typedef.MultiStorage
	for _, storageName := range job.Storage {
		for _, s := range e.cfg.Storage {
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

	// Execute repository sync
	err := repository.Sync(job, false, storages)

	if err != nil {
		// Check if cancelled
		if ctx.Err() != nil {
			e.updateJobStatus(jobID, string(StatusCancelled), "")
			ui.Printf("Job was cancelled")
		} else {
			e.updateJobStatus(jobID, string(StatusFailed), err.Error())
			ui.Errorf("Job failed: %v", err)
		}
		return
	}

	e.updateJobStatus(jobID, string(StatusCompleted), "")
	ui.Printf("Job completed successfully")
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

	// Update status in database
	return e.updateJobStatus(jobID, string(StatusCancelled), "")
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