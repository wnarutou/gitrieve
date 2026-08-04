package db

import (
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
)

func TestAddExecution(t *testing.T) {
	db, err := Initialize(":memory:")
	assert.NoError(t, err)

	migrations := NewMigrations(db)

	execution := &Execution{
		ID:        "test-id",
		JobName:   "test-job",
		StartTime: time.Now(),
		Status:    "running",
		CreatedAt: time.Now(),
	}

	err = migrations.AddExecution(execution)
	assert.NoError(t, err)

	// Verify the execution was added
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM executions").Scan(&count)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestAddLog(t *testing.T) {
	db, err := Initialize(":memory:")
	assert.NoError(t, err)

	migrations := NewMigrations(db)

	log := &Log{
		ExecutionID: "test-id",
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "Test log message",
	}

	err = migrations.AddLog(log)
	assert.NoError(t, err)

	// Verify the log was added
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestGetExecutions(t *testing.T) {
	db, err := Initialize(":memory:")
	assert.NoError(t, err)

	migrations := NewMigrations(db)

	// Add test data
	execution := &Execution{
		ID:        "test-id",
		JobName:   "test-job",
		StartTime: time.Now(),
		Status:    "completed",
		CreatedAt: time.Now(),
	}
	err = migrations.AddExecution(execution)
	assert.NoError(t, err)

	// Get executions
	executions, err := migrations.GetExecutions()
	assert.NoError(t, err)
	assert.Len(t, executions, 1)
	assert.Equal(t, "test-job", executions[0].JobName)
}

func TestGetLogs(t *testing.T) {
	db, err := Initialize(":memory:")
	assert.NoError(t, err)

	migrations := NewMigrations(db)

	// Add test data
	log := &Log{
		ExecutionID: "test-id",
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "Test log message",
	}
	err = migrations.AddLog(log)
	assert.NoError(t, err)

	// Get logs
	logs, err := migrations.GetLogs("test-id")
	assert.NoError(t, err)
	assert.Len(t, logs, 1)
	assert.Equal(t, "info", logs[0].Level)
}