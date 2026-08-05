package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// closeNotifierRecorder wraps httptest.ResponseRecorder to implement
// http.CloseNotifier, which gin's c.Stream() requires. The channel never
// fires, so streaming is driven solely by the request context timeout.
type closeNotifierRecorder struct {
	*httptest.ResponseRecorder
}

func (closeNotifierRecorder) CloseNotify() <-chan bool {
	return make(chan bool)
}


func TestGetJobLogsSSE(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := NewTestServerWithExecutor(testDB, exec)

	// Insert a completed execution with logs
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?)`,
		"job-sse-1", "test-repo", now, now.Add(1*time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO logs (execution_id, timestamp, level, message) VALUES (?, ?, ?, ?)`,
		"job-sse-1", now, "info", "Starting job execution")
	testDB.Exec(`INSERT INTO logs (execution_id, timestamp, level, message) VALUES (?, ?, ?, ?)`,
		"job-sse-1", now.Add(1*time.Second), "info", "Job completed successfully")

	// Use a request context with a timeout so the stream terminates
	req, _ := http.NewRequest("GET", "/api/jobs/job-sse-1/logs", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	resp := closeNotifierRecorder{httptest.NewRecorder()}

	s.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code)
	assert.Equal(t, "text/event-stream", resp.Header().Get("Content-Type"))

	body := resp.Body.String()
	assert.NotEmpty(t, body)
	// Verify at least one data line was streamed
	assert.True(t, strings.Contains(body, "data:"), "expected at least one data: line in SSE body, got: %s", body)
	// Verify the log content appears
	assert.Contains(t, body, "Starting job execution")
}

func TestGetJobLogsSSENotFound(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := NewTestServerWithExecutor(testDB, exec)

	// Request logs for a job that does not exist
	req, _ := http.NewRequest("GET", "/api/jobs/non-existent/logs", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	resp := httptest.NewRecorder()

	s.ServeHTTP(resp, req)

	assert.Equal(t, 404, resp.Code)
}

func TestGetJobLogsSSEStreamingJob(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := NewTestServerWithExecutor(testDB, exec)

	// Insert a running execution with a single log
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"job-sse-running", "test-repo", now, "running")
	testDB.Exec(`INSERT INTO logs (execution_id, timestamp, level, message) VALUES (?, ?, ?, ?)`,
		"job-sse-running", now, "info", "Job is running")

	// The job is still running, so the stream would normally continue.
	// The request context timeout ends the stream.
	req, _ := http.NewRequest("GET", "/api/jobs/job-sse-running/logs", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	resp := closeNotifierRecorder{httptest.NewRecorder()}

	s.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code)
	assert.Equal(t, "text/event-stream", resp.Header().Get("Content-Type"))

	body := resp.Body.String()
	assert.True(t, strings.Contains(body, "data:"), "expected at least one data: line in SSE body, got: %s", body)
	assert.Contains(t, body, "Job is running")
}
