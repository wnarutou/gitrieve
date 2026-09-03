package db

import (
	"time"
)

type ComponentName string

type ComponentStatus string

const (
	ComponentCode       ComponentName = "code"
	ComponentRelease    ComponentName = "release"
	ComponentIssue      ComponentName = "issues"
	ComponentWiki       ComponentName = "wiki"
	ComponentDiscussion ComponentName = "discussion"

	ComponentPending   ComponentStatus = "pending"
	ComponentRunning   ComponentStatus = "running"
	ComponentCompleted ComponentStatus = "completed"
	ComponentFailed    ComponentStatus = "failed"
	ComponentSkipped   ComponentStatus = "skipped"
	ComponentCancelled ComponentStatus = "cancelled"
)

// Execution represents a job execution record
type Execution struct {
	ID           string    `json:"id"`
	JobName      string    `json:"job_name"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
}

type RepositoryRunStats struct {
	LatestExecutionID string
	LatestStatus      string
	LatestStart       time.Time
	LatestEnd         *time.Time
	LatestError       string
	LastSuccess       *time.Time
	TotalRuns         int64
	SuccessRuns       int64
	FailedRuns        int64
}

// ComponentExecution records an individual component's outcome within an execution.
type ComponentExecution struct {
	ID           int64           `json:"id"`
	ExecutionID  string          `json:"execution_id"`
	Component    ComponentName   `json:"component"`
	Status       ComponentStatus `json:"status"`
	StartTime    *time.Time      `json:"start_time"`
	EndTime      *time.Time      `json:"end_time"`
	ErrorMessage string          `json:"error_message"`
}

// Log represents a log entry for an execution
type Log struct {
	ID          int       `json:"id"`
	ExecutionID string    `json:"execution_id"`
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
}
