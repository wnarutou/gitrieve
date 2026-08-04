package executor

import (
	"time"
	"github.com/google/uuid"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type Execution struct {
	ID           string    `json:"id"`
	JobName      string    `json:"job_name"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
}

type Executor struct {
	logger *logger.Logger
}

func NewExecutor(logger *logger.Logger) *Executor {
	return &Executor{logger: logger}
}

func (e *Executor) ExecuteJob(job typedef.Repository) (*Execution, error) {
	execution := &Execution{
		ID:        uuid.New().String(),
		JobName:   job.Name,
		StartTime: time.Now(),
		Status:    "running",
	}

	// Log start
	if e.logger != nil {
		e.logger.Log(execution.ID, job.Name, "info", "Starting job execution")
	}

	// Simulate execution
	time.Sleep(100 * time.Millisecond)

	execution.EndTime = time.Now()
	execution.Status = "success"

	// Log completion
	if e.logger != nil {
		e.logger.Log(execution.ID, job.Name, "info", "Job completed successfully")
	}

	return execution, nil
}