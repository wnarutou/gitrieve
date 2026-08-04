package db

import (
	"time"
)

// Execution represents a job execution record
type Execution struct {
	ID           string     `json:"id"`
	JobName      string     `json:"job_name"`
	StartTime    time.Time  `json:"start_time"`
	EndTime      time.Time  `json:"end_time"`
	Status       string     `json:"status"`
	ErrorMessage string     `json:"error_message"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Log represents a log entry for an execution
type Log struct {
	ID          int       `json:"id"`
	ExecutionID string    `json:"execution_id"`
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
}