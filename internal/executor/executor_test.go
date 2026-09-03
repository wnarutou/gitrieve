package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/syncresult"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func newTestExecutor(t *testing.T) (*Executor, *db.DB) {
	t.Helper()
	return newTestExecutorForRepo(t, typedef.Repository{Name: "test-repo", URL: "github.com/test/repo"}, noOpRunners())
}

func newTestExecutorForRepo(t *testing.T, repo typedef.Repository, runners Runners) (*Executor, *db.DB) {
	t.Helper()
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testDB.Close()) })

	log := logger.NewLogger(testDB)
	cfg := &config.Config{Repository: []typedef.Repository{repo}}
	return NewExecutorWithRunners(log, testDB, cfg, runners), testDB
}

func noOpRunners() Runners {
	run := func(context.Context, typedef.Repository, []typedef.MultiStorage) error { return nil }
	return Runners{Code: run, Release: run, Issue: run, Wiki: run, Discussion: run}
}

type runnerRecorder struct {
	mu     sync.Mutex
	calls  []string
	errors map[string]error
}

func (r *runnerRecorder) runner(name string) SyncFunc {
	return func(context.Context, typedef.Repository, []typedef.MultiStorage) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, name)
		return r.errors[name]
	}
}

func (r *runnerRecorder) runners() Runners {
	return Runners{
		Code:       r.runner("code"),
		Release:    r.runner("release"),
		Issue:      r.runner("issues"),
		Wiki:       r.runner("wiki"),
		Discussion: r.runner("discussion"),
	}
}

func (r *runnerRecorder) recordedCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func waitForJob(t *testing.T, exec *Executor, jobID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for exec.IsJobRunning(jobID) {
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish", jobID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requireExecution(t *testing.T, testDB *db.DB, jobID string, wantStatus ExecutionStatus, wantError string) {
	t.Helper()
	var status, errorMessage string
	require.NoError(t, testDB.QueryRow(
		"SELECT status, COALESCE(error_message, '') FROM executions WHERE id = ?", jobID,
	).Scan(&status, &errorMessage))
	require.Equal(t, string(wantStatus), status)
	require.Equal(t, wantError, errorMessage)
}

type expectedComponent struct {
	name         db.ComponentName
	status       db.ComponentStatus
	errorMessage string
}

func requireComponents(t *testing.T, testDB *db.DB, jobID string, want ...expectedComponent) {
	t.Helper()
	components, err := testDB.ListComponents(context.Background(), jobID)
	require.NoError(t, err)
	require.Len(t, components, len(want))
	for i := range want {
		require.Equal(t, want[i].name, components[i].Component)
		require.Equal(t, want[i].status, components[i].Status)
		require.Equal(t, want[i].errorMessage, components[i].ErrorMessage)
	}
}

func TestExecuteJobWritesBoundLogs(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)
	waitForJob(t, exec, jobIDs[0])

	var count int
	err = testDB.QueryRow(
		"SELECT COUNT(*) FROM logs WHERE execution_id = ? AND message = 'Starting job execution'", jobIDs[0],
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestExecuteJobRunsConfiguredComponents(t *testing.T) {
	recorder := &runnerRecorder{errors: map[string]error{}}
	exec, _ := newTestExecutorForRepo(t, typedef.Repository{
		Name:               "test-repo",
		URL:                "github.com/test/repo",
		DownloadReleases:   true,
		DownloadIssues:     true,
		DownloadWiki:       true,
		DownloadDiscussion: true,
	}, recorder.runners())

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)
	waitForJob(t, exec, jobIDs[0])
	require.Equal(t, []string{"code", "release", "issues", "wiki", "discussion"}, recorder.recordedCalls())
}

func TestExecuteJobComponentsArePlannedBeforeTheFirstRunner(t *testing.T) {
	codeStarted := make(chan struct{})
	releaseCode := make(chan struct{})
	runners := noOpRunners()
	runners.Code = func(context.Context, typedef.Repository, []typedef.MultiStorage) error {
		close(codeStarted)
		<-releaseCode
		return nil
	}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:               "test-repo",
		URL:                "github.com/test/repo",
		DownloadReleases:   true,
		DownloadIssues:     true,
		DownloadWiki:       true,
		DownloadDiscussion: true,
	}, runners)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	<-codeStarted
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentRunning, ""},
		expectedComponent{db.ComponentRelease, db.ComponentPending, ""},
		expectedComponent{db.ComponentIssue, db.ComponentPending, ""},
		expectedComponent{db.ComponentWiki, db.ComponentPending, ""},
		expectedComponent{db.ComponentDiscussion, db.ComponentPending, ""},
	)

	close(releaseCode)
	waitForJob(t, exec, jobIDs[0])
}

func TestExecuteJobComponentFailureRunsLaterComponentsAndFailsOverall(t *testing.T) {
	recorder := &runnerRecorder{errors: map[string]error{"issues": errors.New("rate limit")}}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
		DownloadWiki:   true,
	}, recorder.runners())

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	require.Equal(t, []string{"code", "issues", "wiki"}, recorder.recordedCalls())
	requireExecution(t, testDB, jobIDs[0], StatusFailed, "issue: rate limit")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentIssue, db.ComponentFailed, "rate limit"},
		expectedComponent{db.ComponentWiki, db.ComponentCompleted, ""},
	)
}

func TestExecuteJobOverallFailureContinuesAfterCodeFailure(t *testing.T) {
	recorder := &runnerRecorder{errors: map[string]error{"code": errors.New("clone failed")}}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
	}, recorder.runners())

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	require.Equal(t, []string{"code", "issues"}, recorder.recordedCalls())
	requireExecution(t, testDB, jobIDs[0], StatusFailed, "code: clone failed")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentFailed, "clone failed"},
		expectedComponent{db.ComponentIssue, db.ComponentCompleted, ""},
	)
}

func TestExecuteJobComponentContextErrorWithoutJobCancellationIsFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "runner cancelled", err: context.Canceled},
		{name: "runner deadline exceeded", err: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &runnerRecorder{errors: map[string]error{"code": tt.err}}
			exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
				Name:           "test-repo",
				URL:            "github.com/test/repo",
				DownloadIssues: true,
			}, recorder.runners())

			jobIDs, err := exec.ExecuteJob("github.com/test/repo")
			require.NoError(t, err)
			waitForJob(t, exec, jobIDs[0])

			require.Equal(t, []string{"code", "issues"}, recorder.recordedCalls())
			requireExecution(t, testDB, jobIDs[0], StatusFailed, "code: "+tt.err.Error())
			requireComponents(t, testDB, jobIDs[0],
				expectedComponent{db.ComponentCode, db.ComponentFailed, tt.err.Error()},
				expectedComponent{db.ComponentIssue, db.ComponentCompleted, ""},
			)
		})
	}
}

func TestExecuteJobComponentFailuresUseSemicolonSeparatedSummary(t *testing.T) {
	recorder := &runnerRecorder{errors: map[string]error{
		"issues": errors.New("rate limit"),
		"wiki":   errors.New("permission denied"),
	}}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
		DownloadWiki:   true,
	}, recorder.runners())

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	requireExecution(t, testDB, jobIDs[0], StatusFailed, "issue: rate limit; wiki: permission denied")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentIssue, db.ComponentFailed, "rate limit"},
		expectedComponent{db.ComponentWiki, db.ComponentFailed, "permission denied"},
	)
}

func TestExecuteJobSkipCompletesOverall(t *testing.T) {
	recorder := &runnerRecorder{errors: map[string]error{
		"wiki": fmt.Errorf("wiki preflight: %w", syncresult.Skip("repository has no wiki")),
	}}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:         "test-repo",
		URL:          "github.com/test/repo",
		DownloadWiki: true,
	}, recorder.runners())

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	requireExecution(t, testDB, jobIDs[0], StatusCompleted, "")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentWiki, db.ComponentSkipped, "repository has no wiki"},
	)
}

func TestExecuteJobCancelTerminalizesCurrentAndRemainingComponents(t *testing.T) {
	issueStarted := make(chan struct{})
	runners := noOpRunners()
	runners.Issue = func(ctx context.Context, _ typedef.Repository, _ []typedef.MultiStorage) error {
		close(issueStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
		DownloadWiki:   true,
	}, runners)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	<-issueStarted
	require.NoError(t, exec.CancelJob(jobIDs[0]))
	waitForJob(t, exec, jobIDs[0])

	requireExecution(t, testDB, jobIDs[0], StatusCancelled, "")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentIssue, db.ComponentCancelled, "context canceled"},
		expectedComponent{db.ComponentWiki, db.ComponentCancelled, "context canceled"},
	)
}

func TestExecuteJobCancelStoreFailureFallsBackToFailedComponentAndOverall(t *testing.T) {
	issueStarted := make(chan struct{})
	runners := noOpRunners()
	runners.Issue = func(ctx context.Context, _ typedef.Repository, _ []typedef.MultiStorage) error {
		close(issueStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
		DownloadWiki:   true,
	}, runners)
	_, err := testDB.Exec(`
		CREATE TRIGGER fail_issue_cancellation
		BEFORE UPDATE OF status ON execution_components
		WHEN OLD.component = 'issues' AND NEW.status = 'cancelled'
		BEGIN
			SELECT RAISE(FAIL, 'forced cancellation terminal update failure');
		END;
	`)
	require.NoError(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	<-issueStarted
	require.NoError(t, exec.CancelJob(jobIDs[0]))
	waitForJob(t, exec, jobIDs[0])

	persistenceMessage := fmt.Sprintf(
		`persist cancelled state: finish component "issues" for execution %q: constraint failed: forced cancellation terminal update failure (1811)`,
		jobIDs[0],
	)
	requireExecution(t, testDB, jobIDs[0], StatusFailed, "issue: "+persistenceMessage)
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentIssue, db.ComponentFailed, persistenceMessage},
		expectedComponent{db.ComponentWiki, db.ComponentCancelled, "context canceled"},
	)
}

func TestExecuteJobCancelFailedFallbackRejectionLeavesOverallActive(t *testing.T) {
	issueStarted := make(chan struct{})
	runners := noOpRunners()
	runners.Issue = func(ctx context.Context, _ typedef.Repository, _ []typedef.MultiStorage) error {
		close(issueStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
		DownloadWiki:   true,
	}, runners)
	_, err := testDB.Exec(`
		CREATE TRIGGER fail_issue_cancellation_and_fallback
		BEFORE UPDATE OF status ON execution_components
		WHEN OLD.component = 'issues' AND NEW.status IN ('cancelled', 'failed')
		BEGIN
			SELECT RAISE(FAIL, 'forced cancellation and fallback failure');
		END;
	`)
	require.NoError(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	<-issueStarted
	require.NoError(t, exec.CancelJob(jobIDs[0]))
	waitForJob(t, exec, jobIDs[0])

	require.False(t, exec.IsJobRunning(jobIDs[0]))
	requireExecution(t, testDB, jobIDs[0], StatusRunning, "")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentIssue, db.ComponentRunning, ""},
		expectedComponent{db.ComponentWiki, db.ComponentCancelled, "context canceled"},
	)

	wantFallbackLog := fmt.Sprintf(
		`Failed to record issues cancellation store failure: finish component "issues" for execution %q: constraint failed: forced cancellation and fallback failure (1811)`,
		jobIDs[0],
	)
	var fallbackLogCount int
	require.NoError(t, testDB.QueryRow(`
		SELECT COUNT(*) FROM logs
		WHERE execution_id = ? AND level = 'error' AND message = ?`,
		jobIDs[0], wantFallbackLog,
	).Scan(&fallbackLogCount))
	require.Equal(t, 1, fallbackLogCount)
}

func TestExecuteJobCancelledOverallStoreFailureFallsBackToFailed(t *testing.T) {
	issueStarted := make(chan struct{})
	runners := noOpRunners()
	runners.Issue = func(ctx context.Context, _ typedef.Repository, _ []typedef.MultiStorage) error {
		close(issueStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:           "test-repo",
		URL:            "github.com/test/repo",
		DownloadIssues: true,
		DownloadWiki:   true,
	}, runners)
	_, err := testDB.Exec(`
		CREATE TRIGGER fail_execution_cancellation
		BEFORE UPDATE OF status ON executions
		WHEN NEW.status = 'cancelled'
		BEGIN
			SELECT RAISE(FAIL, 'forced overall cancellation failure');
		END;
	`)
	require.NoError(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	<-issueStarted
	require.NoError(t, exec.CancelJob(jobIDs[0]))
	waitForJob(t, exec, jobIDs[0])

	persistenceMessage := fmt.Sprintf(
		`persist cancelled execution state: update execution %q to "cancelled": constraint failed: forced overall cancellation failure (1811)`,
		jobIDs[0],
	)
	requireExecution(t, testDB, jobIDs[0], StatusFailed, persistenceMessage)
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentIssue, db.ComponentCancelled, "context canceled"},
		expectedComponent{db.ComponentWiki, db.ComponentCancelled, "context canceled"},
	)
}

func TestExecuteJobComponentStoreFailurePreventsCompletedOverall(t *testing.T) {
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{
		Name:         "test-repo",
		URL:          "github.com/test/repo",
		DownloadWiki: true,
	}, noOpRunners())
	_, err := testDB.Exec(`
		CREATE TRIGGER fail_wiki_completion
		BEFORE UPDATE OF status ON execution_components
		WHEN OLD.component = 'wiki' AND NEW.status = 'completed'
		BEGIN
			SELECT RAISE(FAIL, 'forced component terminal update failure');
		END;
	`)
	require.NoError(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	storeMessage := fmt.Sprintf(
		`persist completed state: finish component "wiki" for execution %q: constraint failed: forced component terminal update failure (1811)`,
		jobIDs[0],
	)
	requireExecution(t, testDB, jobIDs[0], StatusFailed, "wiki: "+storeMessage)
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
		expectedComponent{db.ComponentWiki, db.ComponentFailed, storeMessage},
	)
}

func TestExecuteJobOverallTerminalStoreFailureFallsBackToFailed(t *testing.T) {
	exec, testDB := newTestExecutor(t)
	_, err := testDB.Exec(`
		CREATE TRIGGER fail_execution_completion
		BEFORE UPDATE OF status ON executions
		WHEN NEW.status = 'completed'
		BEGIN
			SELECT RAISE(FAIL, 'forced overall completion failure');
		END;
	`)
	require.NoError(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	persistenceMessage := fmt.Sprintf(
		`persist completed execution state: update execution %q to "completed": constraint failed: forced overall completion failure (1811)`,
		jobIDs[0],
	)
	requireExecution(t, testDB, jobIDs[0], StatusFailed, persistenceMessage)
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
	)
}

func TestExecuteJobFailedOverallFallbackRejectionIsDurablyLogged(t *testing.T) {
	exec, testDB := newTestExecutor(t)
	_, err := testDB.Exec(`
		CREATE TRIGGER fail_execution_completion_before_fallback
		BEFORE UPDATE OF status ON executions
		WHEN NEW.status = 'completed'
		BEGIN
			SELECT RAISE(FAIL, 'forced overall completion failure');
		END;

		CREATE TRIGGER fail_execution_failed_fallback
		BEFORE UPDATE OF status ON executions
		WHEN NEW.status = 'failed'
		BEGIN
			SELECT RAISE(FAIL, 'forced overall failed fallback failure');
		END;
	`)
	require.NoError(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	waitForJob(t, exec, jobIDs[0])

	require.False(t, exec.IsJobRunning(jobIDs[0]))
	requireExecution(t, testDB, jobIDs[0], StatusRunning, "")
	requireComponents(t, testDB, jobIDs[0],
		expectedComponent{db.ComponentCode, db.ComponentCompleted, ""},
	)

	wantLog := fmt.Sprintf(
		`Failed to record overall execution store failure: update execution %q to "failed": constraint failed: forced overall failed fallback failure (1811)`,
		jobIDs[0],
	)
	var count int
	require.NoError(t, testDB.QueryRow(`
		SELECT COUNT(*) FROM logs
		WHERE execution_id = ? AND level = 'error' AND message = ?`,
		jobIDs[0], wantLog,
	).Scan(&count))
	require.Equal(t, 1, count)
}

func TestExecuteJobCreatesRecord(t *testing.T) {
	codeStarted := make(chan struct{})
	runners := noOpRunners()
	runners.Code = func(ctx context.Context, _ typedef.Repository, _ []typedef.MultiStorage) error {
		close(codeStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	exec, testDB := newTestExecutorForRepo(t, typedef.Repository{Name: "test-repo", URL: "github.com/test/repo"}, runners)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	assert.NoError(t, err)
	require.Len(t, jobIDs, 1)
	assert.NotEmpty(t, jobIDs[0])
	<-codeStarted

	var status string
	err = testDB.QueryRow("SELECT status FROM executions WHERE id = ?", jobIDs[0]).Scan(&status)
	assert.NoError(t, err)
	assert.Equal(t, "running", status)
	assert.True(t, exec.IsJobRunning(jobIDs[0]))

	require.NoError(t, exec.CancelJob(jobIDs[0]))
	waitForJob(t, exec, jobIDs[0])
}

func TestExecuteJobUnknownRepository(t *testing.T) {
	exec, _ := newTestExecutor(t)

	_, err := exec.ExecuteJob("does-not-exist")
	assert.Error(t, err)
}

func TestCancelNonRunningJob(t *testing.T) {
	exec, _ := newTestExecutor(t)

	err := exec.CancelJob("never-started")
	require.EqualError(t, err, `update execution "never-started" to "cancelled": expected one row, updated 0`)
}

func TestExecuteJobWritesRepoKey(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobIDs, err := exec.ExecuteJob("https://github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)

	var repoKey string
	err = testDB.QueryRow("SELECT repo_key FROM executions WHERE id = ?", jobIDs[0]).Scan(&repoKey)
	assert.NoError(t, err)
	assert.Equal(t, "github.com/test/repo", repoKey)
	waitForJob(t, exec, jobIDs[0])
}

func TestExecuteJobExpandsOrgIntoMultipleJobs(t *testing.T) {
	exec, testDB := newTestExecutor(t)
	exec.cfg.Load().Repository = []typedef.Repository{
		{Name: "acme", URL: "https://github.com/acme", Type: typedef.TypeOrg, OrgName: "acme"},
	}

	old := expandRepos
	t.Cleanup(func() { expandRepos = old })
	expandRepos = func(repo typedef.Repository) []typedef.Repository {
		return []typedef.Repository{
			{Name: "alpha", URL: "github.com/acme/alpha"},
			{Name: "beta", URL: "github.com/acme/beta"},
		}
	}

	jobIDs, err := exec.ExecuteJob("github.com/acme")
	require.NoError(t, err)
	require.Len(t, jobIDs, 2)

	keys := map[string]bool{}
	for _, id := range jobIDs {
		var repoKey string
		require.NoError(t, testDB.QueryRow("SELECT repo_key FROM executions WHERE id = ?", id).Scan(&repoKey))
		keys[repoKey] = true
		waitForJob(t, exec, id)
	}
	assert.True(t, keys["github.com/acme/alpha"])
	assert.True(t, keys["github.com/acme/beta"])
}

func TestRefreshConfigRepointsExecutor(t *testing.T) {
	exec, _ := newTestExecutor(t)

	exec.RefreshConfig(&config.Config{Repository: []typedef.Repository{
		{Name: "other", URL: "github.com/other/repo"},
	}})

	_, err := exec.ExecuteJob("github.com/test/repo")
	require.Error(t, err)

	jobIDs, err := exec.ExecuteJob("github.com/other/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)
	waitForJob(t, exec, jobIDs[0])
}
