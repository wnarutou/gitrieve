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

type ImportPreviewRequest struct {
	Config string `json:"config"`
}

type FieldChange struct {
	Field    string      `json:"field"`
	Existing interface{} `json:"existing"`
	Imported interface{} `json:"imported"`
}

type RepoEntry struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	URL     string        `json:"url"`
	Changes []FieldChange `json:"changes,omitempty"`
}

type StorageEntry struct {
	Name    string        `json:"name"`
	Type    string        `json:"type,omitempty"`
	Changes []FieldChange `json:"changes,omitempty"`
}

type RepoDiff struct {
	Added    []RepoEntry `json:"added"`
	Deleted  []RepoEntry `json:"deleted"`
	Modified []RepoEntry `json:"modified"`
}

type StorageDiff struct {
	Added    []StorageEntry `json:"added"`
	Deleted  []StorageEntry `json:"deleted"`
	Modified []StorageEntry `json:"modified"`
}

type CountSummary struct {
	Added    int `json:"added"`
	Deleted  int `json:"deleted"`
	Modified int `json:"modified"`
}

type ChangedCount struct {
	Changed int `json:"changed"`
}

type ImportSummary struct {
	Repositories CountSummary `json:"repositories"`
	Storages     CountSummary `json:"storages"`
	Globals      ChangedCount `json:"globals"`
	Server       ChangedCount `json:"server"`
}

type ImportPreviewData struct {
	Summary      ImportSummary `json:"summary"`
	Repositories RepoDiff      `json:"repositories"`
	Storages     StorageDiff   `json:"storages"`
	Globals      []FieldChange `json:"globals"`
	Server       []FieldChange `json:"server"`
	Warnings     []string      `json:"warnings"`
}

type ImportErrorData struct {
	Errors []string `json:"errors"`
}

// ImportChoices selects, per entry, whether the imported or the existing value
// wins on apply. Entries without a choice use the documented defaults
// (added/modified/globals/server -> imported, deleted -> keep).
type ImportChoices struct {
	RepositoryDeletions []string          `json:"repository_deletions"`
	RepositoryChoices   map[string]string `json:"repository_choices"`
	StorageDeletions    []string          `json:"storage_deletions"`
	StorageChoices      map[string]string `json:"storage_choices"`
	GlobalChoices       map[string]string `json:"global_choices"`
	ServerChoices       map[string]string `json:"server_choices"`
}

type ImportRequest struct {
	Config  string        `json:"config"`
	Choices ImportChoices `json:"choices"`
}

type ImportResult struct {
	RepositoriesAdded   int `json:"repositories_added"`
	RepositoriesUpdated int `json:"repositories_updated"`
	RepositoriesDeleted int `json:"repositories_deleted"`
	StoragesAdded       int `json:"storages_added"`
	StoragesUpdated     int `json:"storages_updated"`
	StoragesDeleted     int `json:"storages_deleted"`
	GlobalsUpdated      int `json:"globals_updated"`
	ServerUpdated       int `json:"server_updated"`
}
