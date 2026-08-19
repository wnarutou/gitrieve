package server

import (
	"time"

	"github.com/wnarutou/gitrieve/internal/typedef"
)

type Response struct {
	Code    int         `json:"code"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

type Job struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	Status       string     `json:"status"`
	StartTime    *time.Time `json:"start_time"`
	EndTime      *time.Time `json:"end_time"`
	ErrorMessage string     `json:"error_message"`
}

type CreateJobRequest struct {
	RepositoryKey string `json:"repository_key" binding:"required"`
}

type CreateJobResponse struct {
	JobIDs []string `json:"job_ids"`
	Status string   `json:"status"`
}

type CancelJobResponse struct {
	Status string `json:"status"`
}

type ListJobsResponse struct {
	Jobs  []Job `json:"jobs"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
}

type LogEntry struct {
	ID          int64     `json:"id"`
	ExecutionID string    `json:"execution_id"`
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
}

// SuccessResponse is a simple boolean payload for delete-style operations.
type SuccessResponse struct {
	Success bool `json:"success"`
}

type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
)

type RepositoryOverview struct {
	typedef.Repository
	LastRunTime *time.Time `json:"last_run_time"`
	NextRunTime *time.Time `json:"next_run_time"`
	TotalRuns   int64      `json:"total_runs"`
	SuccessRuns int64      `json:"success_runs"`
	FailedRuns  int64      `json:"failed_runs"`
}

type ListRepositoriesResponse struct {
	Repositories []RepositoryOverview `json:"repositories"`
	Total        int                  `json:"total"`
	Page         int                  `json:"page"`
	Limit        int                  `json:"limit"`
}
