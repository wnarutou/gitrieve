package executor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func newTestExecutor(t *testing.T) (*Executor, *db.DB) {
	t.Helper()
	testDB, err := db.Initialize(":memory:")
	assert.NoError(t, err)
	t.Cleanup(func() { testDB.Close() })

	log := logger.NewLogger(testDB)
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "test-repo", URL: "github.com/test/repo"},
		},
	}
	return NewExecutor(log, testDB, cfg), testDB
}

func TestExecuteJobWritesBoundLogs(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobID, err := exec.ExecuteJob("test-repo")
	require.NoError(t, err)

	// executeAsync binds the goroutine and ui.Printf("Starting job execution")
	// is forwarded to the DB logs for this execution.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		err := testDB.QueryRow("SELECT COUNT(*) FROM logs WHERE execution_id = ?", jobID).Scan(&count)
		require.NoError(t, err)
		if count >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected at least one log row for execution %s, found none", jobID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The log content must be the unified ui output, scoped to this execution.
	var message string
	err = testDB.QueryRow("SELECT message FROM logs WHERE execution_id = ? ORDER BY id LIMIT 1", jobID).Scan(&message)
	require.NoError(t, err)
	assert.Equal(t, "Starting job execution", message)
}

func TestExecuteJobCreatesRecord(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobID, err := exec.ExecuteJob("test-repo")
	assert.NoError(t, err)
	assert.NotEmpty(t, jobID)

	// A pending/running execution record should exist in the database
	var status string
	err = testDB.QueryRow("SELECT status FROM executions WHERE id = ?", jobID).Scan(&status)
	assert.NoError(t, err)
	assert.Contains(t, []string{"pending", "running"}, status)

	// The job should be marked as running in memory
	assert.True(t, exec.IsJobRunning(jobID))
}

func TestExecuteJobUnknownRepository(t *testing.T) {
	exec, _ := newTestExecutor(t)

	_, err := exec.ExecuteJob("does-not-exist")
	assert.Error(t, err)
}

func TestCancelNonRunningJob(t *testing.T) {
	exec, _ := newTestExecutor(t)

	// Cancelling a job that was never started should still update its status
	err := exec.CancelJob("never-started")
	assert.NoError(t, err)
}
