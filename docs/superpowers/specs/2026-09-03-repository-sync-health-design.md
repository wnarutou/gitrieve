# Repository Sync Health Design

## Purpose

gitrieve can schedule thousands of repositories through `gitrieve server`, but the Repositories page does not provide a reliable fleet-level answer to three operational questions:

1. How many repositories are healthy or need attention?
2. Which repositories failed, never ran, missed their schedule, or are still active?
3. When did each repository last produce a fully successful backup?

The existing page already reports `last_run_time`, `next_run_time`, and cumulative success/failure counts. This phase turns that data into an operational health view for roughly 6,000 independently configured `type: repo` entries. It also makes an execution successful only when every enabled component succeeds or is legitimately skipped.

## Confirmed Decisions

- The server scheduler is the only scheduling path in scope; standalone `gitrieve daemon` execution is unchanged.
- Every configured item is a concrete `type: repo` entry. Organization/user expansion does not need a new detail view in this phase.
- Code, release, issue, wiki, and discussion synchronization contribute to the overall execution result when enabled.
- An enabled component that the upstream does not support is `skipped` and does not fail the execution.
- The first phase provides an overview and rapid troubleshooting, not outbound alerts.
- The repository overview never refreshes itself on a timer or when a job-completion event arrives. Users refresh it explicitly.

## Scope

This phase adds:

- component-level execution outcomes;
- correct overall success/failure aggregation;
- executor-level concurrency admission shared by cron, single execution, and bulk retry;
- repository health fields, summary counts, server-side filters, and server-side sorting;
- missed-schedule and long-running detection;
- a repository health dashboard within the existing Repositories page;
- execution/component drill-down and safe bulk retry;
- startup reconciliation for executions left active by an interrupted server process;
- indexes and performance coverage appropriate for 6,000 repositories.

## Out of Scope

- outbound notifications, webhooks, email, or chat alerts;
- automatic page refresh or polling;
- a separate monitoring service;
- historical charts and success-rate trends;
- daemon-mode execution persistence;
- organization/user member-repository expansion;
- cross-process or cross-host job queues.

## Status Model

### Execution status

The existing execution statuses remain:

- `pending`: accepted and waiting for an executor concurrency slot;
- `running`: admitted and currently executing;
- `completed`: every enabled component completed or was skipped;
- `failed`: at least one enabled component failed, or an interrupted process left the execution incomplete;
- `cancelled`: cancelled by the user.

An execution continues attempting the remaining enabled components after one component fails. This preserves the current best-effort behavior while making the final status truthful.

### Component status

Each enabled component has one record with one of:

- `pending`;
- `running`;
- `completed`;
- `failed`;
- `skipped`;
- `cancelled`.

Code is always an enabled component. Release, issue, wiki, and discussion records are created only when their corresponding repository options are enabled. A component returns a recognizable unsupported/not-available outcome when the upstream does not expose that capability; the executor records it as `skipped`. Disabled components have no row.

### Repository health

The API exposes both the latest raw execution status and a display-oriented health value. Health categories are mutually exclusive and use this precedence:

1. `never_synced`: the repository has no execution;
2. `stuck`: the latest execution is `running` beyond `syncStuckThreshold`;
3. `pending` or `running`: the latest execution is active and not stuck;
4. `failed` or `cancelled`: the latest terminal execution has that status;
5. `overdue`: the latest execution completed, but no later execution was accepted for the latest cron occurrence outside the grace window;
6. `healthy`: the latest execution completed and is not overdue.

The response also includes independent `overdue` and `stuck` booleans so clients do not need to reverse-engineer the precedence.

For a repository with a cron expression, let `E` be the most recent scheduled occurrence no later than `now - syncOverdueGrace`. The repository is overdue when it has execution history, has no active execution, and its latest attempt started before `E`. A never-run repository stays `never_synced` rather than also being counted as overdue. Manual executions after `E` satisfy the schedule for this calculation.

Two global duration settings are added:

```yaml
syncOverdueGrace: 30m
syncStuckThreshold: 24h
```

The grace window avoids reporting a job as missed immediately after its cron time. The stuck threshold flags abnormally long running work. Both settings participate in validation, save, import/export, apply, and configuration reload. Non-positive values use their documented defaults.

## Persistence

The existing `executions` table remains the source of overall and historical execution state. Add:

```sql
CREATE TABLE execution_components (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id TEXT NOT NULL,
    component TEXT NOT NULL,
    status TEXT NOT NULL,
    start_time DATETIME,
    end_time DATETIME,
    error_message TEXT,
    UNIQUE(execution_id, component),
    FOREIGN KEY (execution_id) REFERENCES executions(id)
);
```

Add indexes supporting latest-execution, last-success, active-job, and component-detail lookups:

```sql
CREATE INDEX idx_executions_repo_start
    ON executions(repo_key, start_time DESC);
CREATE INDEX idx_executions_repo_status_end
    ON executions(repo_key, status, end_time DESC);
CREATE INDEX idx_executions_status
    ON executions(status);
CREATE INDEX idx_execution_components_execution
    ON execution_components(execution_id);
```

The migration is additive and does not rewrite historical execution rows. Historical tasks have no component detail, which the API represents as an empty component list.

A separate materialized repository-state table is intentionally deferred. The first implementation extends the current query-on-read aggregation and measures it against representative history. This avoids a second mutable source of truth. If the performance target cannot be met with indexed queries, adopting a rebuildable summary table requires a follow-up design rather than an implicit change in this phase.

## Execution Architecture

### Shared concurrency admission

The scheduler currently limits concurrent callback invocations, but `ExecuteJob` starts asynchronous work and returns quickly. That callback limit therefore does not reliably bound the actual synchronization work.

Concurrency control moves into `Executor` and applies to all server-originated work:

```text
cron / single execute / bulk retry
              |
              v
   insert execution as pending
              |
              v
       executor wait queue
              |
       acquire cocurrencyNum
              |
              v
       mark execution running
              |
              v
   run enabled components and aggregate
```

Pending and running jobs remain cancellable. A repository with an existing pending or running execution is not enqueued again. The existing per-repository filesystem lock remains a final safety layer, but duplicate suppression happens before a redundant execution is created.

The executor must not create an unbounded number of simultaneously running goroutines. Queue storage is process-local and bounded in active work by the existing `cocurrencyNum` setting. If enqueueing fails after the database row is created, the row is finalized as failed with the enqueue error.

### Component aggregation

For each execution, the executor:

1. creates rows for code and every enabled optional component;
2. marks a component running immediately before invoking it;
3. records completed, failed, skipped, or cancelled with timing and error detail;
4. continues to subsequent enabled components after ordinary failure;
5. stops promptly on context cancellation;
6. writes the overall terminal status only after all component terminal rows are persisted.

`executions.error_message` contains a concise aggregate suitable for list display. Full component-specific messages remain in `execution_components`, and detailed output remains in `logs`.

### Interrupted-process reconciliation

After database migration and before accepting work, server startup marks leftover `pending` and `running` executions as failed, sets `end_time`, and records that the previous server process ended before completion. Corresponding active component rows are also finalized as failed. This prevents jobs from appearing active forever after a crash or restart.

## Repository Health API

`GET /api/repositories` retains its paginated response and existing search behavior. It gains these query parameters:

- `health`: one health category, with `syncing` as an alias for `pending` plus `running`;
- `overdue`: optional boolean filter;
- `stuck`: optional boolean filter;
- `sort`: `attention`, `name`, `last_attempt`, or `last_success`;
- `direction`: `asc` or `desc`.

The default sort is `attention`, then repository name. Attention order is stuck, failed, overdue, never synced, cancelled, pending, running, and healthy. Stable name/key tie-breakers prevent rows moving unpredictably between pages.

Each repository overview adds:

```json
{
  "last_status": "failed",
  "health_status": "failed",
  "last_attempt_time": "2026-09-03T10:00:00Z",
  "last_success_time": "2026-09-01T10:02:30Z",
  "last_duration_seconds": 151,
  "latest_execution_id": "...",
  "last_error_message": "issues: API rate limit exceeded",
  "overdue": false,
  "stuck": false
}
```

`last_attempt_time` is the latest execution start. `last_success_time` is the end time of the latest overall `completed` execution. Keeping both prevents a recent failed attempt from making an old backup appear fresh.

The paginated response also includes fleet summary counts for total, healthy, failed, pending, running, never synced, and cancelled. It additionally returns independent overdue and stuck totals; these diagnostic totals can overlap terminal/active status counts. Summary counts apply the name/URL search but are computed before health, overdue, and stuck filters, so the cards remain useful for switching categories. Counts cover all search-matched repositories, not only the current page.

Add a read endpoint for an execution's component rows. Existing log endpoints remain authoritative for full logs.

## Repositories Page

The page header contains summary cards for total, healthy, failed, syncing, never synced, and overdue. Clicking a card applies the corresponding server-side filter. Stuck work appears within the syncing area with a distinct warning and is available as a dedicated filter.

The toolbar supports:

- name/URL search;
- latest health filter;
- overdue-only filtering;
- attention, name, last-attempt, and last-success sorting;
- explicit refresh;
- add repository.

The primary table columns are:

- health/status;
- repository name and URL;
- last attempt;
- last successful completion;
- latest duration;
- next scheduled run;
- latest error summary;
- actions.

Less operational configuration remains available in the edit dialog instead of consuming the primary scan path. Failed rows link directly to the latest execution details and offer a single-repository retry.

The detail view shows overall timing and status, component status/timing/error rows, historical or live logs, and a retry action. Historical executions created before this feature show "component details unavailable" instead of synthesizing misleading component results.

The Repositories page performs no periodic polling and does not reload in response to an SSE completion event. Navigation and explicit user actions may render their immediate response, but later server-side changes appear only after the user clicks Refresh or revisits the page.

Filters and sorting are encoded in the hash/query state so an operator can bookmark an exception view.

## Safe Bulk Retry

Bulk retry is enabled for a filtered failed, cancelled, or overdue set. A stuck execution is still active and must be cancelled before it becomes retryable. The UI first obtains the eligible count and asks for explicit confirmation with that number.

The bulk endpoint accepts the same repository selector plus the confirmed expected count. It recomputes eligibility server-side. If the count changed, it returns a conflict with the new count and requires confirmation again. It never trusts a page-sized client list as the full result set.

For each eligible repository, the server:

- skips repositories already pending or running;
- creates a pending execution and enqueues it through the shared executor;
- continues processing other repositories if one enqueue fails.

The response reports requested, queued, skipped-active, no-longer-eligible, and failed-to-enqueue counts. Thousands of job IDs are not returned in the summary response; the Jobs page remains the place to inspect individual executions.

## Error Handling

- Component failures retain their component name and original error while the overall execution receives a concise summary.
- Unsupported upstream capabilities use an explicit skipped outcome; arbitrary permission, authentication, rate-limit, network, storage, and parsing errors are failures.
- Database failure while recording a component terminal result prevents the execution from being reported completed.
- Cancellation wins while queued, while waiting for admission, and during component work.
- Invalid cron expressions do not crash overview generation. The row reports the schedule error and cannot be classified overdue until corrected.
- A repository deleted from configuration remains in job history but is absent from the current repository overview.
- Summary and list queries use one consistent database snapshot so counts and page contents do not disagree within a response.

## Performance

Filtering, health derivation, sorting, and pagination happen on the server. The browser receives only one page plus summary counts.

Performance verification uses at least 6,000 configured repositories and representative execution history, including multiple historical runs per repository. The default overview, failed filter, overdue filter, attention sort, and last-success sort are benchmarked. The acceptance target is a sub-second API response on the project's supported local SQLite deployment under this fixture, with the exact benchmark environment recorded alongside the result.

The query plan must use the new indexes for latest and successful execution lookup. A full unindexed scan per configured repository is not acceptable.

## Testing

Tests cover:

- overall failure when any enabled component fails;
- continued best-effort execution of later components after a failure;
- skipped unsupported components not failing the overall execution;
- disabled components producing no component row;
- last-attempt and last-success semantics across success/failure sequences;
- health precedence and summary counts;
- overdue cron boundary, grace, timezone, and daylight-saving behavior;
- stuck-threshold behavior;
- combined search, health, overdue, sorting, and pagination;
- stable pagination tie-breakers;
- summary counts covering the full match set rather than the current page;
- historical executions with no component rows;
- startup reconciliation of orphaned pending/running executions;
- cancellation while pending and running;
- duplicate active-execution suppression per repository;
- actual executor concurrency never exceeding `cocurrencyNum` for cron, manual, and bulk work;
- bulk retry count conflicts and partial enqueue results;
- explicit-refresh-only frontend behavior;
- additive migration from existing SQLite databases;
- the 6,000-repository performance fixture and query-plan assertions.

The full `go test ./...` suite is the final verification gate.
