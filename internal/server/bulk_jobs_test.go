package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func newBulkServer(t *testing.T, cfg *config.Config, runner *bulkBlockingRunner) (*db.DB, *executor.Executor, *server.TestServer) {
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

func serveJSON(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	return resp
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

func TestBulkCompletionBetweenAPIRecheckAndReservedAdmissionIsNotQueued(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "candidate", URL: "github.com/bulk/completion-race"}}}
	runner := newBulkBlockingRunner(1)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-failed", "github.com/bulk/completion-race", "failed", now)

	var rechecks atomic.Int32
	handler.SetBulkRepositoryStats(func(context.Context, typedef.Repository) (map[string]db.RepositoryRunStats, error) {
		if rechecks.Add(1) == 1 {
			insertBulkExecution(t, testDB, "completed-after-api-recheck", "github.com/bulk/completion-race", "completed", now.Add(time.Minute))
			return map[string]db.RepositoryRunStats{
				"github.com/bulk/completion-race": {
					LatestExecutionID: "fixture-failed",
					LatestStatus:      "failed",
					LatestStart:       now,
					TotalRuns:         1,
				},
			}, nil
		}
		completedEnd := now.Add(2 * time.Minute)
		return map[string]db.RepositoryRunStats{
			"github.com/bulk/completion-race": {
				LatestExecutionID: "completed-after-api-recheck",
				LatestStatus:      "completed",
				LatestStart:       now.Add(time.Minute),
				LatestEnd:         &completedEnd,
				LastSuccess:       &completedEnd,
				TotalRuns:         2,
				SuccessRuns:       1,
				FailedRuns:        1,
			},
		}, nil
	})

	resp, result := postBulk(t, handler, `{"selector":{"search":"completion-race"},"expected_count":1}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Equal(t, 1, result.Data.NoLongerEligible)
	require.Zero(t, result.Data.FailedToEnqueue)
	require.Equal(t, int32(2), rechecks.Load(), "eligibility must be checked again while Executor owns the reservation")
	require.Equal(t, 2, executionCount(t, testDB), "no redundant pending execution may be persisted")
}

func TestBulkDoesNotEnqueueAcrossRuntimeConfigGenerations(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "candidate", URL: "github.com/bulk/candidate"}}}
	runner := newBulkBlockingRunner(1)
	testDB, exec, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-candidate", "github.com/bulk/candidate", "failed", now)

	recheckEntered := make(chan struct{})
	recheckRelease := make(chan struct{})
	handler.SetBulkRepositoryStats(func(context.Context, typedef.Repository) (map[string]db.RepositoryRunStats, error) {
		close(recheckEntered)
		<-recheckRelease
		return map[string]db.RepositoryRunStats{
			"github.com/bulk/candidate": {
				LatestExecutionID: "fixture-candidate",
				LatestStatus:      "failed",
				LatestStart:       now,
				TotalRuns:         1,
			},
		}, nil
	})

	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/bulk", bytes.NewBufferString(
			`{"selector":{"search":"candidate"},"expected_count":1}`,
		))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		responseReady <- resp
	}()
	<-recheckEntered
	exec.RefreshConfig(&config.Config{Repository: []typedef.Repository{{Name: "replacement", URL: "github.com/bulk/replacement"}}})
	close(recheckRelease)

	resp := <-responseReady
	var result bulkResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &result))
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Equal(t, 1, result.Data.NoLongerEligible)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 1, executionCount(t, testDB))
}

func TestRepositoryCRUDPublishesExecutorRuntimeSnapshots(t *testing.T) {
	cfg := &config.Config{Repository: []typedef.Repository{
		{Name: "old", URL: "github.com/config/old"},
		{Name: "remove", URL: "github.com/config/remove"},
	}}
	runner := newBulkBlockingRunner(1)
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{Code: runner.run})
	t.Cleanup(func() {
		runner.close()
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	handler := server.NewRepoTestServer(cfg, testDB, exec)
	generation := exec.RuntimeConfigSnapshot().Generation()

	serveJSON(t, handler, http.MethodPost, "/api/repositories", `{"Name":"created","URL":"github.com/config/created"}`)
	snapshot := exec.RuntimeConfigSnapshot()
	require.Greater(t, snapshot.Generation(), generation)
	_, found := snapshot.Repository("github.com/config/created")
	require.True(t, found)
	generation = snapshot.Generation()

	serveJSON(t, handler, http.MethodPut, "/api/repositories/github.com/config/old", `{"URL":"github.com/config/updated"}`)
	snapshot = exec.RuntimeConfigSnapshot()
	require.Greater(t, snapshot.Generation(), generation)
	_, found = snapshot.Repository("github.com/config/old")
	require.False(t, found)
	_, found = snapshot.Repository("github.com/config/updated")
	require.True(t, found)
	generation = snapshot.Generation()

	serveJSON(t, handler, http.MethodDelete, "/api/repositories/github.com/config/remove", "")
	snapshot = exec.RuntimeConfigSnapshot()
	require.Greater(t, snapshot.Generation(), generation)
	_, found = snapshot.Repository("github.com/config/remove")
	require.False(t, found)
}

func TestStorageCRUDPublishesExecutorRuntimeSnapshots(t *testing.T) {
	cfg := &config.Config{Repository: []typedef.Repository{
		{Name: "first", URL: "github.com/config/first", Storage: []string{"archive"}},
		{Name: "second", URL: "github.com/config/second", Storage: []string{"archive"}},
		{Name: "third", URL: "github.com/config/third", Storage: []string{"archive"}},
	}}
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	storagesSeen := make(chan []typedef.MultiStorage, 3)
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{
		Code: func(_ context.Context, _ typedef.Repository, storages []typedef.MultiStorage) error {
			storagesSeen <- append([]typedef.MultiStorage(nil), storages...)
			return nil
		},
	})
	t.Cleanup(func() {
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	handler := server.NewStorageTestServer(cfg, testDB, exec)
	generation := exec.RuntimeConfigSnapshot().Generation()

	serveJSON(t, handler, http.MethodPost, "/api/storage", `{"Name":"archive","Type":"file","Path":"one"}`)
	require.Greater(t, exec.RuntimeConfigSnapshot().Generation(), generation)
	_, err = exec.ExecuteJob("github.com/config/first")
	require.NoError(t, err)
	require.Equal(t, "one", receiveBulkStorages(t, storagesSeen)[0].Path)
	generation = exec.RuntimeConfigSnapshot().Generation()

	serveJSON(t, handler, http.MethodPut, "/api/storage/archive", `{"Path":"two"}`)
	require.Greater(t, exec.RuntimeConfigSnapshot().Generation(), generation)
	_, err = exec.ExecuteJob("github.com/config/second")
	require.NoError(t, err)
	require.Equal(t, "two", receiveBulkStorages(t, storagesSeen)[0].Path)
	generation = exec.RuntimeConfigSnapshot().Generation()

	serveJSON(t, handler, http.MethodDelete, "/api/storage/archive", "")
	require.Greater(t, exec.RuntimeConfigSnapshot().Generation(), generation)
	_, err = exec.ExecuteJob("github.com/config/third")
	require.NoError(t, err)
	require.Empty(t, receiveBulkStorages(t, storagesSeen))
}

func TestBulkUserRetryUsesUnicodePrefixIndexAndCountsOneCandidateForMultipleExecutions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	user := typedef.Repository{Name: "unicode user", URL: "github.com/团队", Type: typedef.TypeUser, OrgName: "团队"}
	cfg := &config.Config{ConcurrencyNum: 2, Repository: []typedef.Repository{user}}
	runner := newBulkBlockingRunner(2)
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{Code: runner.run},
		func(typedef.Repository) []typedef.Repository {
			return []typedef.Repository{
				{Name: "one", URL: "github.com/团队/one"},
				{Name: "two", URL: "github.com/团队/two"},
			}
		},
	)
	t.Cleanup(func() {
		runner.close()
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	handler := server.NewTestServerWithExecutor(testDB, exec, cfg)
	insertBulkExecution(t, testDB, "fixture-unicode-member", "github.com/团队/历史", "failed", now)
	insertBulkExecution(t, testDB, "fixture-prefix-decoy", "github.com/团队0/not-a-member", "failed", now.Add(time.Minute))

	plan, err := handler.BulkRepositoryStatsQueryPlan(context.Background(), user)
	require.NoError(t, err)
	require.Contains(t, plan, "idx_executions_repo_start", plan)

	resp, result := postBulk(t, handler, `{"selector":{"search":"unicode user"},"expected_count":1}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Equal(t, 1, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Zero(t, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 4, executionCount(t, testDB), "one user candidate should create two concrete executions")
	require.NotContains(t, resp.Body.String(), "job_ids")
}

func TestBulkEmptyUserExpansionCountsFailedToEnqueue(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	user := typedef.Repository{Name: "empty user", URL: "github.com/empty", Type: typedef.TypeUser, OrgName: "empty"}
	cfg := &config.Config{Repository: []typedef.Repository{user}}
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{},
		func(typedef.Repository) []typedef.Repository { return nil },
	)
	t.Cleanup(func() {
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	handler := server.NewTestServerWithExecutor(testDB, exec, cfg)
	insertBulkExecution(t, testDB, "fixture-empty-user", "github.com/empty/history", "failed", now)

	resp, result := postBulk(t, handler, `{"selector":{"search":"empty user"},"expected_count":1}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Equal(t, 1, result.Data.FailedToEnqueue)
	require.Equal(t, 1, executionCount(t, testDB), "empty expansion must not persist a pending execution")
}

func TestBulkCancellationAfterRecheckStopsBeforeEnqueueAndAccountsRemainder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{
		{Name: "one", URL: "github.com/cancel/one"},
		{Name: "two", URL: "github.com/cancel/two"},
		{Name: "three", URL: "github.com/cancel/three"},
	}}
	runner := newBulkBlockingRunner(3)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	for _, name := range []string{"one", "two", "three"} {
		insertBulkExecution(t, testDB, "fixture-cancel-"+name, "github.com/cancel/"+name, "failed", now)
	}

	recheckEntered := make(chan struct{})
	recheckRelease := make(chan struct{})
	var rechecks atomic.Int32
	handler.SetBulkRepositoryStats(func(_ context.Context, repository typedef.Repository) (map[string]db.RepositoryRunStats, error) {
		if rechecks.Add(1) == 1 {
			close(recheckEntered)
			<-recheckRelease
		}
		return map[string]db.RepositoryRunStats{
			repository.Key(): {
				LatestExecutionID: "fixture",
				LatestStatus:      "failed",
				LatestStart:       now,
				TotalRuns:         1,
			},
		}, nil
	})

	requestContext, cancel := context.WithCancel(context.Background())
	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/bulk", bytes.NewBufferString(
			`{"selector":{"search":"cancel"},"expected_count":3}`,
		)).WithContext(requestContext)
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		responseReady <- resp
	}()
	<-recheckEntered
	cancel()
	close(recheckRelease)

	resp := <-responseReady
	var result bulkResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &result))
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, int32(1), rechecks.Load(), "bulk continued rechecking after cancellation")
	require.Equal(t, 3, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Equal(t, 3, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 3, executionCount(t, testDB))
}

func TestBulkContextCancelledRecheckStopsAndAccountsRemainderOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{
		{Name: "one", URL: "github.com/recheck/one"},
		{Name: "two", URL: "github.com/recheck/two"},
	}}
	runner := newBulkBlockingRunner(2)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	for _, name := range []string{"one", "two"} {
		insertBulkExecution(t, testDB, "fixture-recheck-"+name, "github.com/recheck/"+name, "failed", now)
	}
	var rechecks atomic.Int32
	handler.SetBulkRepositoryStats(func(context.Context, typedef.Repository) (map[string]db.RepositoryRunStats, error) {
		rechecks.Add(1)
		return nil, context.Canceled
	})

	resp, result := postBulk(t, handler, `{"selector":{"search":"recheck"},"expected_count":2}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, int32(1), rechecks.Load(), "bulk retried after a context-cancelled database read")
	require.Equal(t, 2, result.Data.Requested)
	require.Equal(t, 2, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 2, executionCount(t, testDB))
}

func TestBulkCountsRepositoryThatBecomesActiveAfterGuardAsSkipped(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "candidate", URL: "github.com/race/active"}}}
	runner := newBulkBlockingRunner(1)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-race-failed", "github.com/race/active", "failed", now)
	handler.SetBulkRepositoryStats(func(_ context.Context, repository typedef.Repository) (map[string]db.RepositoryRunStats, error) {
		insertBulkExecution(t, testDB, "became-active-after-guard", repository.Key(), "pending", now.Add(-time.Hour))
		return map[string]db.RepositoryRunStats{
			repository.Key(): {
				LatestExecutionID: "fixture-race-failed",
				LatestStatus:      "failed",
				LatestStart:       now,
				TotalRuns:         2,
			},
		}, nil
	})

	resp, result := postBulk(t, handler, `{"selector":{"search":"race"},"expected_count":1}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Equal(t, 1, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Zero(t, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 2, executionCount(t, testDB))
}

func TestBulkCountsRepositoryMissingAtEnqueueAsNoLongerEligible(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "candidate", URL: "github.com/race/missing"}}}
	runner := newBulkBlockingRunner(1)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-race-missing", "github.com/race/missing", "failed", now)
	handler.SetBulkExecute(func(context.Context, string, uint64) ([]string, error) {
		return nil, executor.ErrRepositoryNotFound
	})

	resp, result := postBulk(t, handler, `{"selector":{"search":"candidate"},"expected_count":1}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Equal(t, 1, result.Data.NoLongerEligible)
	require.Zero(t, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 1, executionCount(t, testDB))
}

func TestBulkCancellationDuringEnqueueStopsAndAccountsUntouchedRemainder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{
		{Name: "one", URL: "github.com/enqueue-cancel/one"},
		{Name: "two", URL: "github.com/enqueue-cancel/two"},
		{Name: "three", URL: "github.com/enqueue-cancel/three"},
	}}
	runner := newBulkBlockingRunner(3)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	for _, name := range []string{"one", "two", "three"} {
		insertBulkExecution(t, testDB, "fixture-enqueue-cancel-"+name, "github.com/enqueue-cancel/"+name, "failed", now)
	}

	enqueueEntered := make(chan struct{})
	var enqueueCalls atomic.Int32
	handler.SetBulkExecute(func(ctx context.Context, _ string, _ uint64) ([]string, error) {
		enqueueCalls.Add(1)
		close(enqueueEntered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	requestContext, cancel := context.WithCancel(context.Background())
	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/bulk", bytes.NewBufferString(
			`{"selector":{"search":"enqueue-cancel"},"expected_count":3}`,
		)).WithContext(requestContext)
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		responseReady <- resp
	}()
	<-enqueueEntered
	cancel()

	resp := <-responseReady
	var result bulkResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &result))
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, int32(1), enqueueCalls.Load())
	require.Equal(t, 3, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Equal(t, 3, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 3, executionCount(t, testDB))
}

func TestConcurrentConfigMutationAndBulkSnapshotUseIsRaceSafe(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	writeFile(t, path, `repository:
  - name: base
    url: github.com/race/base
  - name: update-target
    url: github.com/race/update-target
  - name: delete-target
    url: github.com/race/delete-target
storage:
  - name: update-target
    type: file
    path: old
  - name: delete-target
    type: file
    path: old
`)
	config.Path = path
	config.Init()
	t.Cleanup(func() { config.Path = "" })

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	cfg := config.GetIns()
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{})
	t.Cleanup(func() {
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	handler := server.NewConfigConcurrencyTestServer(cfg, testDB, exec)

	requestStatus := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp.Code
	}
	importDocument, err := json.Marshal(map[string]interface{}{
		"config": "repository:\n  - name: base-imported\n    url: github.com/race/base\n",
	})
	require.NoError(t, err)

	start := make(chan struct{})
	failures := make(chan string, 140)
	var workers sync.WaitGroup
	workers.Add(4)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 20; index++ {
			body := fmt.Sprintf(`{"Name":"repo-%d","URL":"github.com/race/repo-%d"}`, index, index)
			if status := requestStatus(http.MethodPost, "/api/repositories", body); status != http.StatusOK {
				failures <- fmt.Sprintf("create repository %d returned %d", index, status)
			}
		}
		crudRequests := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodPut, "/api/repositories/github.com/race/update-target", `{"Name":"updated"}`},
			{http.MethodDelete, "/api/repositories/github.com/race/delete-target", ""},
			{http.MethodPost, "/api/storage", `{"Name":"created-storage","Type":"file","Path":"one"}`},
			{http.MethodPut, "/api/storage/update-target", `{"Path":"two"}`},
			{http.MethodDelete, "/api/storage/delete-target", ""},
		}
		for _, request := range crudRequests {
			if status := requestStatus(request.method, request.path, request.body); status != http.StatusOK {
				failures <- fmt.Sprintf("%s %s returned %d", request.method, request.path, status)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 10; index++ {
			if status := requestStatus(http.MethodPost, "/api/config/import", string(importDocument)); status != http.StatusOK {
				failures <- fmt.Sprintf("import %d returned %d", index, status)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 10; index++ {
			if status := requestStatus(http.MethodPost, "/api/config/reload", ""); status != http.StatusOK {
				failures <- fmt.Sprintf("reload %d returned %d", index, status)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 100; index++ {
			if status := requestStatus(http.MethodPost, "/api/jobs/bulk", `{"selector":{},"expected_count":0}`); status != http.StatusOK {
				failures <- fmt.Sprintf("bulk %d returned %d", index, status)
			}
			snapshot := handler.Cfg()
			if snapshot == nil || len(snapshot.Repository) == 0 {
				failures <- "observed empty API config snapshot"
			}
		}
	}()
	close(start)
	workers.Wait()
	close(failures)
	for failure := range failures {
		require.Fail(t, failure)
	}
}

func TestBulkCountsClosedExecutorAsFailedWithoutAbortingResponse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "candidate", URL: "github.com/closed/candidate"}}}
	runner := newBulkBlockingRunner(1)
	testDB, exec, handler := newBulkServer(t, cfg, runner)
	insertBulkExecution(t, testDB, "fixture-closed", "github.com/closed/candidate", "failed", now)
	require.NoError(t, exec.Close())

	resp, result := postBulk(t, handler, `{"selector":{"search":"closed"},"expected_count":1}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, result.Data.Requested)
	require.Zero(t, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Equal(t, 1, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 1, executionCount(t, testDB))
}

func TestBulkContinuesAfterOrdinaryRecheckDatabaseFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{ConcurrencyNum: 1, Repository: []typedef.Repository{
		{Name: "alpha", URL: "github.com/db-failure/alpha"},
		{Name: "bravo", URL: "github.com/db-failure/bravo"},
	}}
	runner := newBulkBlockingRunner(1)
	testDB, _, handler := newBulkServer(t, cfg, runner)
	for _, name := range []string{"alpha", "bravo"} {
		insertBulkExecution(t, testDB, "fixture-db-failure-"+name, "github.com/db-failure/"+name, "failed", now)
	}
	handler.SetBulkRepositoryStats(func(_ context.Context, repository typedef.Repository) (map[string]db.RepositoryRunStats, error) {
		if repository.Key() == "github.com/db-failure/alpha" {
			return nil, errors.New("forced recheck read failure")
		}
		return map[string]db.RepositoryRunStats{
			repository.Key(): {
				LatestExecutionID: "fixture-db-failure-bravo",
				LatestStatus:      "failed",
				LatestStart:       now,
				TotalRuns:         1,
			},
		}, nil
	})

	resp, result := postBulk(t, handler, `{"selector":{"search":"db-failure"},"expected_count":2}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 2, result.Data.Requested)
	require.Equal(t, 1, result.Data.Queued)
	require.Zero(t, result.Data.SkippedActive)
	require.Zero(t, result.Data.NoLongerEligible)
	require.Equal(t, 1, result.Data.FailedToEnqueue)
	require.Equal(t, result.Data.Requested, result.Data.Queued+result.Data.SkippedActive+result.Data.NoLongerEligible+result.Data.FailedToEnqueue)
	require.Equal(t, 3, executionCount(t, testDB))
}

func TestBulkReturnsServerErrorWhenExecutorIsUnavailable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "candidate", URL: "github.com/nil/candidate"}}}
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testDB.Close()) })
	insertBulkExecution(t, testDB, "fixture-nil", "github.com/nil/candidate", "failed", now)
	handler := server.NewTestServerWithExecutor(testDB, nil, cfg)

	resp, result := postBulk(t, handler, `{"selector":{"search":"nil"},"expected_count":1}`)
	require.Equal(t, http.StatusInternalServerError, resp.Code, resp.Body.String())
	require.Equal(t, http.StatusInternalServerError, result.Code)
	require.Equal(t, 1, executionCount(t, testDB))
}

func receiveBulkStorages(t *testing.T, calls <-chan []typedef.MultiStorage) []typedef.MultiStorage {
	t.Helper()
	select {
	case storages := <-calls:
		return storages
	case <-time.After(3 * time.Second):
		t.Fatal("executor runner did not receive storage configuration")
		return nil
	}
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
