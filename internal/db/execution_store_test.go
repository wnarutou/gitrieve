package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreatePendingExecutionsRollsBackWholeBatchWhenLaterComponentInsertFails(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	_, err = testDB.Exec(`
		CREATE TRIGGER reject_beta_component
		BEFORE INSERT ON execution_components
		WHEN NEW.execution_id = 'beta'
		BEGIN
			SELECT RAISE(FAIL, 'forced beta component failure');
		END;`)
	require.NoError(t, err)
	startedAt := time.Date(2026, time.September, 4, 9, 0, 0, 0, time.UTC)
	err = testDB.CreatePendingExecutions(ctx, []PendingExecution{
		{ID: "alpha", JobName: "alpha", RepoKey: "github.com/acme/alpha", StartTime: startedAt, Components: []ComponentName{ComponentCode}},
		{ID: "beta", JobName: "beta", RepoKey: "github.com/acme/beta", StartTime: startedAt, Components: []ComponentName{ComponentCode}},
	})
	require.ErrorContains(t, err, "forced beta component failure")

	var executionCount, componentCount int
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM executions`).Scan(&executionCount))
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM execution_components`).Scan(&componentCount))
	require.Zero(t, executionCount)
	require.Zero(t, componentCount)
}

func TestDiscardPendingExecutionsRollsBackWhenEveryExecutionIsNotPending(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	startedAt := time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC)
	require.NoError(t, testDB.CreatePendingExecutions(ctx, []PendingExecution{
		{ID: "pending", JobName: "pending", RepoKey: "github.com/acme/pending", StartTime: startedAt, Components: []ComponentName{ComponentCode}},
		{ID: "running", JobName: "running", RepoKey: "github.com/acme/running", StartTime: startedAt, Components: []ComponentName{ComponentCode}},
	}))
	_, err = testDB.Exec(`UPDATE executions SET status = 'running' WHERE id = 'running'`)
	require.NoError(t, err)

	err = testDB.DiscardPendingExecutions(ctx, []string{"pending", "running"})
	require.ErrorContains(t, err, "expected 2 pending execution rows, deleted 1")
	var executionCount, componentCount int
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM executions`).Scan(&executionCount))
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM execution_components`).Scan(&componentCount))
	require.Equal(t, 2, executionCount)
	require.Equal(t, 2, componentCount)
}

func TestComponentStorePersistsTransitionsAndListsComponentsInDisplayOrder(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	start := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	insertExecution(t, testDB, "exec-components", "github.com/acme/widgets", ComponentPending)

	require.NoError(t, testDB.CreateComponents(ctx, "exec-components", []ComponentName{ComponentWiki, ComponentCode}))
	require.NoError(t, testDB.StartComponent(ctx, "exec-components", ComponentCode, start))
	require.NoError(t, testDB.FinishComponent(ctx, "exec-components", ComponentCode, ComponentCompleted, end, ""))
	require.NoError(t, testDB.FinishComponent(ctx, "exec-components", ComponentWiki, ComponentSkipped, end, "wiki disabled"))

	components, err := testDB.ListComponents(ctx, "exec-components")
	require.NoError(t, err)
	require.Len(t, components, 2)
	require.Equal(t, ComponentCode, components[0].Component)
	require.Equal(t, ComponentCompleted, components[0].Status)
	require.Equal(t, start, *components[0].StartTime)
	require.Equal(t, end, *components[0].EndTime)
	require.Empty(t, components[0].ErrorMessage)
	require.Equal(t, ComponentWiki, components[1].Component)
	require.Equal(t, ComponentSkipped, components[1].Status)
	require.Nil(t, components[1].StartTime)
	require.Equal(t, end, *components[1].EndTime)
	require.Equal(t, "wiki disabled", components[1].ErrorMessage)
}

func TestReconcileInterruptedFailsActiveExecutionsAndComponents(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	fixedNow := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	insertExecution(t, testDB, "exec-pending", "github.com/acme/pending", ComponentPending)
	insertExecution(t, testDB, "exec-running", "github.com/acme/running", ComponentRunning)
	require.NoError(t, testDB.CreateComponents(ctx, "exec-pending", []ComponentName{ComponentCode}))
	require.NoError(t, testDB.CreateComponents(ctx, "exec-running", []ComponentName{ComponentWiki}))
	require.NoError(t, testDB.StartComponent(ctx, "exec-running", ComponentWiki, fixedNow.Add(-time.Minute)))

	require.NoError(t, testDB.ReconcileInterrupted(ctx, fixedNow))

	for _, id := range []string{"exec-pending", "exec-running"} {
		var status, message string
		var end time.Time
		require.NoError(t, testDB.QueryRow(
			`SELECT status, end_time, error_message FROM executions WHERE id = ?`, id,
		).Scan(&status, &end, &message))
		require.Equal(t, string(ComponentFailed), status)
		require.Equal(t, fixedNow, end)
		require.Equal(t, "previous server process ended before completion", message)

		components, err := testDB.ListComponents(ctx, id)
		require.NoError(t, err)
		require.Len(t, components, 1)
		require.Equal(t, ComponentFailed, components[0].Status)
		require.Equal(t, fixedNow, *components[0].EndTime)
		require.Equal(t, "previous server process ended before completion", components[0].ErrorMessage)
	}
}

func TestExecutionExistenceQueriesUseExactExecutionAndRepositoryKeys(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	insertExecution(t, testDB, "done", "github.com/acme/widgets", ComponentCompleted)
	insertExecution(t, testDB, "pending", "github.com/acme/widgets", ComponentPending)
	insertExecution(t, testDB, "running", "github.com/acme/widgets", ComponentRunning)
	insertExecution(t, testDB, "other", "github.com/acme/widgets-copy", ComponentRunning)
	insertExecution(t, testDB, "completed-only", "github.com/acme/completed-only", ComponentCompleted)

	exists, err := testDB.ExecutionExists(ctx, "done")
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = testDB.ExecutionExists(ctx, "unknown")
	require.NoError(t, err)
	require.False(t, exists)

	active, err := testDB.ActiveExecutionExists(ctx, "github.com/acme/widgets")
	require.NoError(t, err)
	require.True(t, active)
	active, err = testDB.ActiveExecutionExists(ctx, "github.com/acme/widgets-copy")
	require.NoError(t, err)
	require.True(t, active)
	active, err = testDB.ActiveExecutionExists(ctx, "github.com/acme/completed-only")
	require.NoError(t, err)
	require.False(t, active)
	active, err = testDB.ActiveExecutionExists(ctx, "github.com/acme/widget")
	require.NoError(t, err)
	require.False(t, active)
}

func TestRepositoryRunStatsReturnsLatestRowsSuccessTimesAndCumulativeCounts(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	base := time.Date(2026, time.September, 4, 8, 0, 0, 0, time.UTC)
	insertRepositoryRun(t, testDB, "alpha-success", "github.com/acme/alpha", base, ptrTime(base.Add(10*time.Minute)), "completed", nil)
	insertRepositoryRun(t, testDB, "alpha-failure", "github.com/acme/alpha", base.Add(time.Hour), ptrTime(base.Add(time.Hour+5*time.Minute)), "failed", ptrString("network unavailable"))
	insertRepositoryRun(t, testDB, "beta-running", "github.com/acme/beta", base.Add(2*time.Hour), nil, "running", nil)
	insertRepositoryRun(t, testDB, "tie-a", "github.com/acme/tied", base.Add(3*time.Hour), ptrTime(base.Add(3*time.Hour+time.Minute)), "completed", nil)
	insertRepositoryRun(t, testDB, "tie-z", "github.com/acme/tied", base.Add(3*time.Hour), ptrTime(base.Add(3*time.Hour+2*time.Minute)), "failed", ptrString("tie winner"))
	insertRepositoryRun(t, testDB, "legacy", "", base.Add(4*time.Hour), nil, "completed", nil)

	stats, err := testDB.RepositoryRunStats(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 3, "rows without a repository key stay valid but are excluded from repository stats")

	alpha := stats["github.com/acme/alpha"]
	require.Equal(t, "alpha-failure", alpha.LatestExecutionID)
	require.Equal(t, "failed", alpha.LatestStatus)
	require.Equal(t, base.Add(time.Hour), alpha.LatestStart)
	require.NotNil(t, alpha.LatestEnd)
	require.Equal(t, base.Add(time.Hour+5*time.Minute), *alpha.LatestEnd)
	require.Equal(t, "network unavailable", alpha.LatestError)
	require.NotNil(t, alpha.LastSuccess)
	require.Equal(t, base.Add(10*time.Minute), *alpha.LastSuccess)
	require.Equal(t, int64(2), alpha.TotalRuns)
	require.Equal(t, int64(1), alpha.SuccessRuns)
	require.Equal(t, int64(1), alpha.FailedRuns)

	beta := stats["github.com/acme/beta"]
	require.Equal(t, "beta-running", beta.LatestExecutionID)
	require.Equal(t, "running", beta.LatestStatus)
	require.Nil(t, beta.LatestEnd)
	require.Nil(t, beta.LastSuccess)
	require.Zero(t, beta.SuccessRuns)
	require.Zero(t, beta.FailedRuns)

	tied := stats["github.com/acme/tied"]
	require.Equal(t, "tie-z", tied.LatestExecutionID, "descending execution ID breaks equal-start ties")
	require.Equal(t, "failed", tied.LatestStatus)
	require.Equal(t, "tie winner", tied.LatestError)
	require.NotNil(t, tied.LastSuccess)
	require.Equal(t, base.Add(3*time.Hour+time.Minute), *tied.LastSuccess)
	require.Equal(t, int64(2), tied.TotalRuns)
}

func TestRepositoryRunStatsParsesSQLiteAggregateTimeWithMonotonicSuffix(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	start := time.Now()
	end := start.Add(time.Minute)
	insertRepositoryRun(t, testDB, "monotonic-success", "github.com/acme/monotonic", start, &end, "completed", nil)

	stats, err := testDB.RepositoryRunStats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats["github.com/acme/monotonic"].LastSuccess)
	got := *stats["github.com/acme/monotonic"].LastSuccess
	require.Equal(t, end.Round(0), got.Round(0))
}

func TestStartComponentRejectsMissingAndNonPendingRows(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	startedAt := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	insertExecution(t, testDB, "exec-start-transitions", "github.com/acme/widgets", ComponentPending)

	require.Error(t, testDB.StartComponent(ctx, "exec-start-transitions", ComponentCode, startedAt))
	require.NoError(t, testDB.CreateComponents(ctx, "exec-start-transitions", []ComponentName{ComponentCode}))
	require.NoError(t, testDB.StartComponent(ctx, "exec-start-transitions", ComponentCode, startedAt))
	require.Error(t, testDB.StartComponent(ctx, "exec-start-transitions", ComponentCode, startedAt.Add(time.Minute)))
}

func TestFinishComponentRejectsMissingNonterminalAndAlreadyTerminalRows(t *testing.T) {
	ctx := context.Background()
	testDB, err := Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	finishedAt := time.Date(2026, time.September, 3, 10, 2, 0, 0, time.UTC)
	insertExecution(t, testDB, "exec-finish-transitions", "github.com/acme/widgets", ComponentPending)
	require.NoError(t, testDB.CreateComponents(ctx, "exec-finish-transitions", []ComponentName{ComponentCode}))

	require.Error(t, testDB.FinishComponent(ctx, "exec-finish-transitions", ComponentWiki, ComponentCompleted, finishedAt, ""))
	require.Error(t, testDB.FinishComponent(ctx, "exec-finish-transitions", ComponentCode, ComponentRunning, finishedAt, ""))
	require.NoError(t, testDB.FinishComponent(ctx, "exec-finish-transitions", ComponentCode, ComponentFailed, finishedAt, "interrupted"))
	require.Error(t, testDB.FinishComponent(ctx, "exec-finish-transitions", ComponentCode, ComponentCompleted, finishedAt, ""))
}

func insertExecution(t *testing.T, testDB *DB, id, repoKey string, status ComponentStatus) {
	t.Helper()
	_, err := testDB.Exec(
		`INSERT INTO executions (id, job_name, repo_key, start_time, status) VALUES (?, ?, ?, ?, ?)`,
		id, "test-job", repoKey, time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC), status,
	)
	require.NoError(t, err)
}

func insertRepositoryRun(t *testing.T, testDB *DB, id, repoKey string, start time.Time, end *time.Time, status string, message *string) {
	t.Helper()
	_, err := testDB.Exec(
		`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, "test-job", repoKey, start, end, status, message,
	)
	require.NoError(t, err)
}

func ptrTime(value time.Time) *time.Time { return &value }

func ptrString(value string) *string { return &value }
