package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type bulkResponse struct {
	Code int `json:"code"`
	Data struct {
		ActualCount      int `json:"actual_count"`
		Requested        int `json:"requested"`
		Queued           int `json:"queued"`
		SkippedActive    int `json:"skipped_active"`
		NoLongerEligible int `json:"no_longer_eligible"`
		FailedToEnqueue  int `json:"failed_to_enqueue"`
	} `json:"data"`
}

type bulkBlockingRunner struct {
	entered     chan string
	release     chan struct{}
	releaseOnce sync.Once
	running     atomic.Int32
	maxRunning  atomic.Int32
}

func newBulkBlockingRunner(capacity int) *bulkBlockingRunner {
	return &bulkBlockingRunner{
		entered: make(chan string, capacity),
		release: make(chan struct{}),
	}
}

func (r *bulkBlockingRunner) run(ctx context.Context, repo typedef.Repository, _ []typedef.MultiStorage) error {
	running := r.running.Add(1)
	defer r.running.Add(-1)
	for {
		maximum := r.maxRunning.Load()
		if running <= maximum || r.maxRunning.CompareAndSwap(maximum, running) {
			break
		}
	}
	r.entered <- repo.Key()
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *bulkBlockingRunner) close() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func newBulkServer(t *testing.T, cfg *config.Config, runner *bulkBlockingRunner) (*db.DB, *executor.Executor, http.Handler) {
	t.Helper()
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{Code: runner.run})
	t.Cleanup(func() {
		runner.close()
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	return testDB, exec, server.NewTestServerWithExecutor(testDB, exec, cfg)
}

func insertBulkExecution(t *testing.T, testDB *db.DB, id, repoKey, status string, start time.Time) {
	t.Helper()
	var end interface{}
	if status != "pending" && status != "running" {
		end = start.Add(time.Minute)
	}
	_, err := testDB.Exec(`
		INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, id, repoKey, start, end, status, "fixture error")
	require.NoError(t, err)
}

func postBulk(t *testing.T, handler http.Handler, body string) (*httptest.ResponseRecorder, bulkResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/bulk", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	var decoded bulkResponse
	if json.Valid(resp.Body.Bytes()) {
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &decoded), resp.Body.String())
	}
	return resp, decoded
}

func executionCount(t *testing.T, testDB *db.DB) int {
	t.Helper()
	var count int
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM executions`).Scan(&count))
	return count
}

func TestBulkExpectedCountConflictHasNoSideEffectsAndOnlyRetryableRepositoriesQueue(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{
		ConcurrencyNum:     1,
		SyncOverdueGrace:   30 * time.Minute,
		SyncStuckThreshold: time.Hour,
		Repository: []typedef.Repository{
			{Name: "Zulu Failed", URL: "github.com/bulk/failed"},
			{Name: "alpha Cancelled", URL: "github.com/bulk/cancelled"},
			{Name: "mike Overdue", URL: "github.com/bulk/overdue", Cron: "@every 1h"},
			{Name: "d Healthy", URL: "github.com/bulk/healthy", Cron: "@every 1h"},
			{Name: "e Never", URL: "github.com/bulk/never"},
			{Name: "f Pending", URL: "github.com/bulk/pending"},
			{Name: "g Running", URL: "github.com/bulk/running"},
			{Name: "h Stuck", URL: "github.com/bulk/stuck"},
		},
	}
	runner := newBulkBlockingRunner(3)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-failed", "github.com/bulk/failed", "failed", now.Add(-10*time.Minute))
	insertBulkExecution(t, testDB, "fixture-cancelled", "github.com/bulk/cancelled", "cancelled", now.Add(-9*time.Minute))
	insertBulkExecution(t, testDB, "fixture-overdue", "github.com/bulk/overdue", "completed", now.Add(-3*time.Hour))
	insertBulkExecution(t, testDB, "fixture-healthy", "github.com/bulk/healthy", "completed", now.Add(-10*time.Minute))
	insertBulkExecution(t, testDB, "fixture-pending", "github.com/bulk/pending", "pending", now.Add(-8*time.Minute))
	insertBulkExecution(t, testDB, "fixture-running", "github.com/bulk/running", "running", now.Add(-7*time.Minute))
	insertBulkExecution(t, testDB, "fixture-stuck", "github.com/bulk/stuck", "running", now.Add(-2*time.Hour))

	before := executionCount(t, testDB)
	conflictRecorder, conflict := postBulk(t, handler, `{"selector":{"search":"bulk","health":""},"expected_count":2}`)
	require.Equal(t, http.StatusConflict, conflictRecorder.Code, conflictRecorder.Body.String())
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Equal(t, 3, conflict.Data.ActualCount)
	require.Equal(t, before, executionCount(t, testDB), "count conflict created executions")

	acceptedRecorder, accepted := postBulk(t, handler, `{"selector":{"search":"bulk","health":""},"expected_count":3}`)
	require.Equal(t, http.StatusOK, acceptedRecorder.Code, acceptedRecorder.Body.String())
	require.Equal(t, http.StatusOK, accepted.Code)
	require.Equal(t, 3, accepted.Data.Requested)
	require.Equal(t, 3, accepted.Data.Queued)
	require.Zero(t, accepted.Data.SkippedActive)
	require.Zero(t, accepted.Data.NoLongerEligible)
	require.Zero(t, accepted.Data.FailedToEnqueue)
	require.Equal(t, accepted.Data.Requested, accepted.Data.Queued+accepted.Data.SkippedActive+accepted.Data.NoLongerEligible+accepted.Data.FailedToEnqueue)
	require.NotContains(t, acceptedRecorder.Body.String(), "job_ids")
	require.NotContains(t, acceptedRecorder.Body.String(), "repository_ids")

	rows, err := testDB.Query(`SELECT repo_key FROM executions WHERE id NOT LIKE 'fixture-%' ORDER BY rowid`)
	require.NoError(t, err)
	defer rows.Close()
	var queued []string
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		queued = append(queued, key)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{
		"github.com/bulk/cancelled",
		"github.com/bulk/overdue",
		"github.com/bulk/failed",
	}, queued)
}

func TestBulkRejectsMalformedRequestsBeforeCreatingExecutions(t *testing.T) {
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "failed", URL: "github.com/bulk/failed"}}}
	runner := newBulkBlockingRunner(1)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-failed", "github.com/bulk/failed", "failed", time.Now().Add(-time.Minute))

	for _, body := range []string{
		`{}`,
		`{"selector":null,"expected_count":0}`,
		`{"selector":{},"expected_count":null}`,
		`{"selector":{},"expected_count":-1}`,
		`{"selector":{"health":"unknown"},"expected_count":0}`,
		`{"selector":{"overdue":"true"},"expected_count":0}`,
		`{"selector":{"overdue":null},"expected_count":0}`,
		`{"selector":{"stuck":true},"expected_count":0}`,
		`{"selector":{"repository_ids":["github.com/bulk/failed"]},"expected_count":1}`,
		`{"selector":{},"expected_count":0,"unexpected":true}`,
		`{"selector":{},"expected_count":0}{}`,
	} {
		before := executionCount(t, testDB)
		resp, result := postBulk(t, handler, body)
		require.Equal(t, http.StatusBadRequest, resp.Code, "body %s: %s", body, resp.Body.String())
		require.Equal(t, http.StatusBadRequest, result.Code, "body %s: %s", body, resp.Body.String())
		require.Equal(t, before, executionCount(t, testDB), "body %s created an execution", body)
	}
}

func TestBulkReportsPartialResultsAndRechecksEligibilityAtEnqueueTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{
		ConcurrencyNum: 1,
		Repository: []typedef.Repository{
			{Name: "Alpha queued", URL: "github.com/bulk/alpha"},
			{Name: "bravo active", URL: "github.com/bulk/bravo"},
			{Name: "Charlie changes", URL: "github.com/bulk/charlie"},
			{Name: "delta broken", URL: "github.com/bulk/delta"},
			{Name: "Echo queued", URL: "github.com/bulk/echo"},
		},
	}
	runner := newBulkBlockingRunner(2)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	for _, key := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		insertBulkExecution(t, testDB, "fixture-failed-"+key, "github.com/bulk/"+key, "failed", now)
	}
	insertBulkExecution(t, testDB, "fixture-active-bravo", "github.com/bulk/bravo", "pending", now.Add(-time.Hour))
	_, err := testDB.Exec(`
		CREATE TRIGGER make_charlie_healthy_after_alpha
		AFTER INSERT ON executions
		WHEN NEW.repo_key = 'github.com/bulk/alpha' AND NEW.status = 'pending'
		BEGIN
			INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status)
			VALUES ('became-healthy-charlie', 'charlie', 'github.com/bulk/charlie',
			        '2099-01-01T00:00:00Z', '2099-01-01T00:01:00Z', 'completed');
		END;
		CREATE TRIGGER reject_delta_enqueue
		BEFORE INSERT ON executions
		WHEN NEW.repo_key = 'github.com/bulk/delta' AND NEW.status = 'pending'
		BEGIN
			SELECT RAISE(FAIL, 'forced bulk enqueue failure');
		END;
	`)
	require.NoError(t, err)

	resp, result := postBulk(t, handler, `{"selector":{"search":"bulk"},"expected_count":5}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, http.StatusOK, result.Code)
	require.Equal(t, 5, result.Data.Requested)
	require.Equal(t, 2, result.Data.Queued)
	require.Equal(t, 1, result.Data.SkippedActive)
	require.Equal(t, 1, result.Data.NoLongerEligible)
	require.Equal(t, 1, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.NotContains(t, resp.Body.String(), "job_ids")

	var newlyQueued []string
	rows, err := testDB.Query(`
		SELECT repo_key FROM executions
		WHERE id NOT LIKE 'fixture-%' AND id <> 'became-healthy-charlie'
		ORDER BY rowid`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		newlyQueued = append(newlyQueued, key)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"github.com/bulk/alpha", "github.com/bulk/echo"}, newlyQueued)
}

func TestBulkUsesExecutorConcurrencyLimit(t *testing.T) {
	repositories := make([]typedef.Repository, 6)
	for i := range repositories {
		repositories[i] = typedef.Repository{
			Name: fmt.Sprintf("repo-%d", i),
			URL:  fmt.Sprintf("github.com/bulk/repo-%d", i),
		}
	}
	cfg := &config.Config{ConcurrencyNum: 2, Repository: repositories}
	runner := newBulkBlockingRunner(len(repositories))
	testDB, exec, handler := newBulkServer(t, cfg, runner)
	now := time.Now().UTC().Truncate(time.Second)
	for i := range repositories {
		insertBulkExecution(t, testDB, fmt.Sprintf("fixture-%d", i), repositories[i].Key(), "failed", now)
	}

	resp, result := postBulk(t, handler, `{"selector":{"search":"github.com/bulk/repo"},"expected_count":6}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, http.StatusOK, result.Code)
	require.Equal(t, 6, result.Data.Requested)
	require.Equal(t, 6, result.Data.Queued)

	started := []string{receiveBulkRunner(t, runner.entered), receiveBulkRunner(t, runner.entered)}
	sort.Strings(started)
	require.Equal(t, []string{"github.com/bulk/repo-0", "github.com/bulk/repo-1"}, started)
	var pending, running int
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM executions WHERE status = 'pending'`).Scan(&pending))
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM executions WHERE status = 'running'`).Scan(&running))
	require.Equal(t, 4, pending)
	require.Equal(t, 2, running)
	require.Equal(t, int32(2), runner.running.Load())

	runner.close()
	require.NoError(t, exec.Close())
	require.Equal(t, int32(2), runner.maxRunning.Load())
}

func receiveBulkRunner(t *testing.T, entered <-chan string) string {
	t.Helper()
	select {
	case key := <-entered:
		return key
	case <-time.After(3 * time.Second):
		t.Fatal("bulk runner did not start")
		return ""
	}
}
