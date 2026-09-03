package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var monotonicTimeSuffix = regexp.MustCompile(` m=[+-][0-9]+(\.[0-9]+)?$`)

const interruptedExecutionMessage = "previous server process ended before completion"

// PendingExecution describes one execution and its component rows for atomic creation.
type PendingExecution struct {
	ID         string
	JobName    string
	RepoKey    string
	StartTime  time.Time
	Components []ComponentName
}

// CreatePendingExecutions creates a batch of pending executions and all of
// their component rows atomically.
func (d *DB) CreatePendingExecutions(ctx context.Context, executions []PendingExecution) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create pending executions: %w", err)
	}
	defer tx.Rollback()

	for _, execution := range executions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO executions (id, job_name, repo_key, start_time, status)
			VALUES (?, ?, ?, ?, ?)`,
			execution.ID, execution.JobName, execution.RepoKey, execution.StartTime, ComponentPending,
		); err != nil {
			return fmt.Errorf("create pending execution %q: %w", execution.ID, err)
		}
		for _, component := range execution.Components {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO execution_components (execution_id, component, status) VALUES (?, ?, ?)`,
				execution.ID, component, ComponentPending,
			); err != nil {
				return fmt.Errorf("create component %q for execution %q: %w", component, execution.ID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create pending executions: %w", err)
	}
	return nil
}

// DiscardPendingExecutions atomically removes an unaccepted batch that was
// committed but never published to the executor queue.
func (d *DB) DiscardPendingExecutions(ctx context.Context, executionIDs []string) error {
	if len(executionIDs) == 0 {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin discard pending executions: %w", err)
	}
	defer tx.Rollback()

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(executionIDs)), ",")
	args := make([]interface{}, len(executionIDs))
	for i, executionID := range executionIDs {
		args[i] = executionID
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM execution_components WHERE execution_id IN (`+placeholders+`)`, args...,
	); err != nil {
		return fmt.Errorf("discard pending execution components: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM executions WHERE status = 'pending' AND id IN (`+placeholders+`)`, args...,
	)
	if err != nil {
		return fmt.Errorf("discard pending executions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect discarded pending executions: %w", err)
	}
	if deleted != int64(len(executionIDs)) {
		return fmt.Errorf("expected %d pending execution rows, deleted %d", len(executionIDs), deleted)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit discard pending executions: %w", err)
	}
	return nil
}

// CreateComponents creates pending component records for an execution.
func (d *DB) CreateComponents(ctx context.Context, executionID string, components []ComponentName) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create components: %w", err)
	}
	defer tx.Rollback()

	for _, component := range components {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO execution_components (execution_id, component, status) VALUES (?, ?, ?)`,
			executionID, component, ComponentPending,
		); err != nil {
			return fmt.Errorf("create component %q for execution %q: %w", component, executionID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create components: %w", err)
	}
	return nil
}

// StartComponent marks a component as running at startedAt.
func (d *DB) StartComponent(ctx context.Context, executionID string, component ComponentName, startedAt time.Time) error {
	result, err := d.ExecContext(ctx, `
		UPDATE execution_components
		SET status = ?, start_time = ?, end_time = NULL, error_message = NULL
		WHERE execution_id = ? AND component = ? AND status = ?`,
		ComponentRunning, startedAt, executionID, component, ComponentPending,
	)
	if err != nil {
		return fmt.Errorf("start component %q for execution %q: %w", component, executionID, err)
	}
	return requireOneComponentRow(result, "start", executionID, component)
}

// FinishComponent records a component's terminal status and outcome details.
func (d *DB) FinishComponent(ctx context.Context, executionID string, component ComponentName, status ComponentStatus, finishedAt time.Time, errorMessage string) error {
	if !isTerminalComponentStatus(status) {
		return fmt.Errorf("finish component %q for execution %q: invalid terminal status %q", component, executionID, status)
	}

	result, err := d.ExecContext(ctx, `
		UPDATE execution_components
		SET status = ?, end_time = ?, error_message = ?
		WHERE execution_id = ? AND component = ? AND status IN (?, ?)`,
		status, finishedAt, errorMessage, executionID, component, ComponentPending, ComponentRunning,
	)
	if err != nil {
		return fmt.Errorf("finish component %q for execution %q: %w", component, executionID, err)
	}
	return requireOneComponentRow(result, "finish", executionID, component)
}

func isTerminalComponentStatus(status ComponentStatus) bool {
	switch status {
	case ComponentCompleted, ComponentFailed, ComponentSkipped, ComponentCancelled:
		return true
	default:
		return false
	}
}

func requireOneComponentRow(result sql.Result, action, executionID string, component ComponentName) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s component %q for execution %q: inspect affected rows: %w", action, component, executionID, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s component %q for execution %q: expected one pending or running row, updated %d", action, component, executionID, rows)
	}
	return nil
}

// ListComponents returns component execution records in display order.
func (d *DB) ListComponents(ctx context.Context, executionID string) ([]ComponentExecution, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, execution_id, component, status, start_time, end_time, error_message
		FROM execution_components
		WHERE execution_id = ?
		ORDER BY CASE component
			WHEN 'code' THEN 1
			WHEN 'release' THEN 2
			WHEN 'issues' THEN 3
			WHEN 'wiki' THEN 4
			WHEN 'discussion' THEN 5
			ELSE 6
		END, id`, executionID)
	if err != nil {
		return nil, fmt.Errorf("list components for execution %q: %w", executionID, err)
	}
	defer rows.Close()

	components := make([]ComponentExecution, 0)
	for rows.Next() {
		var (
			component ComponentExecution
			startTime sql.NullTime
			endTime   sql.NullTime
			errorText sql.NullString
		)
		if err := rows.Scan(
			&component.ID,
			&component.ExecutionID,
			&component.Component,
			&component.Status,
			&startTime,
			&endTime,
			&errorText,
		); err != nil {
			return nil, fmt.Errorf("scan component for execution %q: %w", executionID, err)
		}
		if startTime.Valid {
			component.StartTime = &startTime.Time
		}
		if endTime.Valid {
			component.EndTime = &endTime.Time
		}
		if errorText.Valid {
			component.ErrorMessage = errorText.String
		}
		components = append(components, component)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate components for execution %q: %w", executionID, err)
	}
	return components, nil
}

// ExecutionExists reports whether an execution ID is known.
func (d *DB) ExecutionExists(ctx context.Context, executionID string) (bool, error) {
	var exists bool
	if err := d.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM executions WHERE id = ?)`, executionID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check execution %q: %w", executionID, err)
	}
	return exists, nil
}

// ActiveExecutionExists reports whether a repository has a pending or running execution.
func (d *DB) ActiveExecutionExists(ctx context.Context, repoKey string) (bool, error) {
	var exists bool
	if err := d.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM executions
			WHERE repo_key = ? AND status IN ('pending', 'running')
		)`, repoKey,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check active execution for repository %q: %w", repoKey, err)
	}
	return exists, nil
}

// RepositoryRunStats loads the latest execution and cumulative outcome counts
// for every repository key in one indexed query.
func (d *DB) RepositoryRunStats(ctx context.Context) (map[string]RepositoryRunStats, error) {
	rows, err := d.QueryContext(ctx, `
		WITH ranked AS (
			SELECT id, repo_key, start_time, end_time, status, error_message,
			       ROW_NUMBER() OVER (
				   PARTITION BY repo_key ORDER BY start_time DESC, id DESC
			       ) AS rn
			FROM executions WHERE repo_key <> ''
		), totals AS (
			SELECT repo_key,
			       COUNT(*) AS total_runs,
			       COALESCE(SUM(status = 'completed'), 0) AS success_runs,
			       COALESCE(SUM(status = 'failed'), 0) AS failed_runs
			FROM executions WHERE repo_key <> '' GROUP BY repo_key
		), successes AS (
			SELECT repo_key, MAX(end_time) AS last_success
			FROM executions
			WHERE repo_key <> '' AND status = 'completed'
			GROUP BY repo_key
		)
		SELECT ranked.id, ranked.repo_key, ranked.start_time, ranked.end_time,
		       ranked.status, ranked.error_message, successes.last_success,
		       totals.total_runs, totals.success_runs, totals.failed_runs
		FROM ranked
		JOIN totals USING (repo_key)
		LEFT JOIN successes USING (repo_key)
		WHERE ranked.rn = 1`)
	if err != nil {
		return nil, fmt.Errorf("query repository run stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]RepositoryRunStats)
	for rows.Next() {
		var (
			key            string
			latest         RepositoryRunStats
			latestEnd      sql.NullTime
			latestError    sql.NullString
			lastSuccessRaw interface{}
		)
		if err := rows.Scan(
			&latest.LatestExecutionID,
			&key,
			&latest.LatestStart,
			&latestEnd,
			&latest.LatestStatus,
			&latestError,
			&lastSuccessRaw,
			&latest.TotalRuns,
			&latest.SuccessRuns,
			&latest.FailedRuns,
		); err != nil {
			return nil, fmt.Errorf("scan repository run stats: %w", err)
		}
		if latestEnd.Valid {
			end := latestEnd.Time
			latest.LatestEnd = &end
		}
		if latestError.Valid {
			latest.LatestError = latestError.String
		}
		lastSuccess, err := nullableAggregateTime(lastSuccessRaw)
		if err != nil {
			return nil, fmt.Errorf("scan last success for repository %q: %w", key, err)
		}
		latest.LastSuccess = lastSuccess
		stats[key] = latest
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository run stats: %w", err)
	}
	return stats, nil
}

func nullableAggregateTime(value interface{}) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if parsed, ok := value.(time.Time); ok {
		return &parsed, nil
	}

	var raw string
	switch value := value.(type) {
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return nil, fmt.Errorf("unsupported timestamp type %T", value)
	}
	// SQLite aggregate expressions lose the DATETIME column type that normally
	// lets the driver scan directly into time.Time. A Go time.Time persisted by
	// the driver can retain its optional monotonic annotation in that text form;
	// it is not part of a wall-clock timestamp and time.Parse cannot read it.
	if suffix := monotonicTimeSuffix.FindStringIndex(raw); suffix != nil {
		raw = raw[:suffix[0]]
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("unsupported timestamp %q", raw)
}

// ReconcileInterrupted marks executions and component rows left active by a
// previous server process as failed.
func (d *DB) ReconcileInterrupted(ctx context.Context, now time.Time) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin interrupted execution reconciliation: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_components
		SET status = 'failed', end_time = ?, error_message = ?
		WHERE status IN ('pending', 'running')`, now, interruptedExecutionMessage); err != nil {
		return fmt.Errorf("reconcile active component executions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions
		SET status = 'failed', end_time = ?, error_message = ?
		WHERE status IN ('pending', 'running')`, now, interruptedExecutionMessage); err != nil {
		return fmt.Errorf("reconcile active executions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit interrupted execution reconciliation: %w", err)
	}
	return nil
}
