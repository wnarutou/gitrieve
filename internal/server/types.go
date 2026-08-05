package server

import "time"

type Response struct {
	Code    int         `json:"code"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

type Job struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Status      string     `json:"status"`
	StartTime   *time.Time `json:"start_time"`
	EndTime     *time.Time `json:"end_time"`
	ErrorMessage string    `json:"error_message"`
}

type CreateJobRequest struct {
	Repository string `json:"repository" binding:"required"`
}

type CreateJobResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
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