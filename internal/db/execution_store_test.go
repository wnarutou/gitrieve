package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
