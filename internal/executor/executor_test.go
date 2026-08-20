package executor

import (
	"os"
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

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)

	// executeAsync binds the goroutine and ui.Printf("Starting job execution")
	// is forwarded to the DB logs for this execution. Poll for that specific
	// row rather than asserting a single snapshot's ordering: under parallel
	// full-suite load a concurrent reader can transiently hold SQLite's write
	// lock and an insert can be BUSY-dropped (sink errors are deliberately
	// discarded), so the row is only guaranteed to appear eventually.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		err := testDB.QueryRow(
			"SELECT COUNT(*) FROM logs WHERE execution_id = ? AND message = 'Starting job execution'", jobIDs[0],
		).Scan(&count)
		require.NoError(t, err)
		if count >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected a 'Starting job execution' log row for execution %s", jobIDs[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecuteJobRunsConfiguredComponents(t *testing.T) {
	// The component syncs (issue/release/wiki/discussion) read the package-global
	// config via config.GetIns() and dereference cfg.GitHubToken, so mirror the
	// server process (where cobra.OnInitialize runs config.Init) to avoid a nil
	// pointer panic.
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString("githubtoken: test-token\n")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	config.Path = tmp.Name()
	config.Init()
	t.Cleanup(func() { config.Path = "" })

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { testDB.Close() })

	log := logger.NewLogger(testDB)
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "test-repo", URL: "github.com/test/repo", DownloadIssues: true},
		},
	}
	exec := NewExecutor(log, testDB, cfg)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)

	// The executor must run the configured component syncs. "Downloading issues"
	// is written only after repository.Sync returns, and that sync does a real
	// clone of the (nonexistent) test repo that can take a while on a slow
	// network — use a generous deadline.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var count int
		err := testDB.QueryRow(
			"SELECT COUNT(*) FROM logs WHERE execution_id = ? AND message = 'Downloading issues'", jobIDs[0],
		).Scan(&count)
		require.NoError(t, err)
		if count >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 'Downloading issues' log row for execution %s", jobIDs[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecuteJobCreatesRecord(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobIDs, err := exec.ExecuteJob("github.com/test/repo")
	assert.NoError(t, err)
	require.Len(t, jobIDs, 1)
	assert.NotEmpty(t, jobIDs[0])

	// A pending/running execution record should exist in the database
	var status string
	err = testDB.QueryRow("SELECT status FROM executions WHERE id = ?", jobIDs[0]).Scan(&status)
	assert.NoError(t, err)
	assert.Contains(t, []string{"pending", "running"}, status)

	// The job should be marked as running in memory
	assert.True(t, exec.IsJobRunning(jobIDs[0]))
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

func TestExecuteJobWritesRepoKey(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobIDs, err := exec.ExecuteJob("https://github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)

	var repoKey string
	err = testDB.QueryRow("SELECT repo_key FROM executions WHERE id = ?", jobIDs[0]).Scan(&repoKey)
	assert.NoError(t, err)
	assert.Equal(t, "github.com/test/repo", repoKey)
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
	}
	assert.True(t, keys["github.com/acme/alpha"])
	assert.True(t, keys["github.com/acme/beta"])
}

func TestRefreshConfigRepointsExecutor(t *testing.T) {
	exec, _ := newTestExecutor(t)

	exec.RefreshConfig(&config.Config{Repository: []typedef.Repository{
		{Name: "other", URL: "github.com/other/repo"},
	}})

	// The old key no longer resolves against the executor's config.
	_, err := exec.ExecuteJob("github.com/test/repo")
	require.Error(t, err)

	// The new key resolves.
	jobIDs, err := exec.ExecuteJob("github.com/other/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)
}
