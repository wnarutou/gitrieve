# Repository Sync Health Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide a trustworthy, manually refreshed health overview and safe retry workflow for approximately 6,000 server-scheduled repositories.

**Architecture:** Keep `executions` as the historical source of truth, add component outcomes, and aggregate an indexed execution snapshot with configured repositories on each overview request. Move actual concurrency admission into `Executor`, then let cron, manual execution, and bulk retry use that same pending queue. Derive health in a focused server package and send only summary counts plus one page to the browser.

**Tech Stack:** Go 1.23, SQLite through `modernc.org/sqlite`, Gin, `robfig/cron/v3`, Viper, Testify, embedded HTML/CSS/vanilla JavaScript.

**Spec:** `docs/superpowers/specs/2026-09-03-repository-sync-health-design.md`

## Global Constraints

- The feature applies to `gitrieve server`; standalone `gitrieve daemon` does not persist executions.
- Primary usage is approximately 6,000 independently configured `type: repo` entries; preserve existing user/org behavior where touched.
- Overall `completed` means every enabled component is `completed` or `skipped`; any enabled component failure makes the execution `failed`.
- Continue later enabled components after an ordinary failure; stop promptly on context cancellation.
- `syncOverdueGrace` defaults to `30m`; `syncStuckThreshold` defaults to `24h`; non-positive values select those defaults.
- `cocurrencyNum` remains the existing, intentionally misspelled configuration key and limits actual running executor work.
- The Repositories page must not poll, schedule a reload, or reload itself on an SSE completion event. Its existing explicit Refresh button remains.
- Do not add outbound alerts, historical charts, organization-member expansion, or a materialized repository-state table.
- Preserve the deletion-safe archive invariants documented in `CLAUDE.md`; do not alter repository fetch/archive behavior.
- Existing SQLite databases migrate additively, and historical executions remain readable without component rows.
- Keep `last_run_time`, `total_runs`, `success_runs`, and `failed_runs` in the repository API for compatibility while adding the new fields.
- Do not modify or delete the existing untracked `.codex-ui-test/` directory.

## Planned File Map

- `internal/config/config.go`, `internal/config/importexport.go`, `internal/server/config_api.go`: own the two health thresholds through load/save/import/reload.
- `internal/db/models.go`, `internal/db/db.go`, `internal/db/migrate.go`, new `internal/db/execution_store.go`: own component persistence, execution snapshots, active checks, and interrupted-run reconciliation.
- New `internal/syncresult/syncresult.go`: defines a skip outcome that is distinguishable from failure.
- `internal/wiki/wiki.go`, `cmd/wiki/wiki.go`, `cmd/daemon/daemon.go`: return and display the skip outcome without treating missing Wiki support as an error.
- New `internal/executor/components.go`, plus `internal/executor/executor.go`: own component plans, final aggregation, pending admission, duplicate suppression, cancellation, and shutdown.
- New `internal/server/repository_health.go`, plus `internal/server/types.go` and `internal/server/api.go`: derive health, filter/sort/paginate snapshots, serve component details, and enqueue bulk retries.
- `cmd/server/server.go`: wires reconciliation, routes, and orderly executor shutdown.
- `web/templates/index.html`, `web/static/js/main.js`, `web/static/css/main.css`: render the health dashboard, component detail, and retry controls with explicit refresh only.
- `docs/api.md`, `docs/web-ui.md`, `README.md`, `README_zh.md`, `CLAUDE.md`, `config/example.config.yaml`: document the behavior and operational settings.

---

### Task 1: Health-threshold configuration lifecycle

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/importexport.go`
- Modify: `internal/config/importexport_test.go`
- Modify: `internal/server/config_api.go`
- Modify: `internal/server/config_api_test.go`
- Modify: `config/example.config.yaml`

**Interfaces:**
- Produces: `config.Config.SyncOverdueGrace time.Duration`
- Produces: `config.Config.SyncStuckThreshold time.Duration`
- Produces: `config.GetSyncOverdueGrace() time.Duration`
- Produces: `config.GetSyncStuckThreshold() time.Duration`
- Produces constants `config.DefaultSyncOverdueGrace` and `config.DefaultSyncStuckThreshold`.

- [ ] **Step 1: Write failing default and getter tests**

Add to `internal/config/config_test.go`:

```go
func TestSyncHealthDefaults(t *testing.T) {
    cfg := &Config{}
    seedDefaults(cfg)
    require.Equal(t, 30*time.Minute, cfg.SyncOverdueGrace)
    require.Equal(t, 24*time.Hour, cfg.SyncStuckThreshold)
}

func TestSyncHealthNonPositiveValuesUseDefaults(t *testing.T) {
    cfg := &Config{SyncOverdueGrace: -time.Second, SyncStuckThreshold: -time.Second}
    seedDefaults(cfg)
    require.Equal(t, DefaultSyncOverdueGrace, cfg.SyncOverdueGrace)
    require.Equal(t, DefaultSyncStuckThreshold, cfg.SyncStuckThreshold)
}
```

- [ ] **Step 2: Write failing export/import and config-API tests**

Extend `internal/config/importexport_test.go` to assert exported YAML contains `syncOverdueGrace: 45m0s` and `syncStuckThreshold: 12h0m0s`, and that `ParseImport` restores both typed durations. Extend `internal/server/config_api_test.go` with preview/apply assertions whose imported values differ from current values and increase `globals_updated` by two.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/config ./internal/server`

Expected: compilation fails because the two fields, constants, and getters do not exist.

- [ ] **Step 4: Implement fields, defaults, persistence, and import/apply**

Add to `internal/config/config.go`:

```go
const (
    DefaultSyncOverdueGrace  = 30 * time.Minute
    DefaultSyncStuckThreshold = 24 * time.Hour
)

type Config struct {
    // existing fields remain unchanged
    SyncOverdueGrace  time.Duration `yaml:"syncOverdueGrace" mapstructure:"syncOverdueGrace"`
    SyncStuckThreshold time.Duration `yaml:"syncStuckThreshold" mapstructure:"syncStuckThreshold"`
}

func GetSyncOverdueGrace() time.Duration { return ins.SyncOverdueGrace }
func GetSyncStuckThreshold() time.Duration { return ins.SyncStuckThreshold }
```

Seed both defaults in `seedDefaults`, write both in `Save`, add `DurationString` fields to `ExportConfig`, copy them in `ExportFrom`, seed them in `seedExportDefaults`, and add exact `diffGlobals`/`applyImport` branches in `internal/server/config_api.go`.
Change `DurationString.UnmarshalYAML` diagnostics from the field-specific `retryBaseDelay` wording to generic `duration` wording so invalid values for either new setting produce an accurate error.

- [ ] **Step 5: Add example values and verify GREEN**

Add to `config/example.config.yaml`:

```yaml
syncOverdueGrace: 30m
syncStuckThreshold: 24h
```

Run: `gofmt -w internal/config/config.go internal/config/config_test.go internal/config/importexport.go internal/config/importexport_test.go internal/server/config_api.go internal/server/config_api_test.go`

Run: `go test ./internal/config ./internal/server`

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/config internal/server/config_api.go internal/server/config_api_test.go config/example.config.yaml
git commit -m "feat(config): add repository health thresholds"
```

### Task 2: Component persistence and interrupted-run reconciliation

**Files:**
- Modify: `internal/db/models.go`
- Modify: `internal/db/db.go`
- Modify: `internal/db/migrate.go`
- Modify: `internal/db/migrations_test.go`
- Create: `internal/db/execution_store.go`
- Create: `internal/db/execution_store_test.go`

**Interfaces:**
- Produces: `db.ComponentName`, `db.ComponentStatus`, and `db.ComponentExecution`.
- Produces: `(*db.DB).CreateComponents(context.Context, string, []db.ComponentName) error`.
- Produces: `(*db.DB).StartComponent(context.Context, string, db.ComponentName, time.Time) error`.
- Produces: `(*db.DB).FinishComponent(context.Context, string, db.ComponentName, db.ComponentStatus, time.Time, string) error`.
- Produces: `(*db.DB).ListComponents(context.Context, string) ([]db.ComponentExecution, error)`.
- Produces: `(*db.DB).ExecutionExists(context.Context, string) (bool, error)`.
- Produces: `(*db.DB).ActiveExecutionExists(context.Context, string) (bool, error)`.
- Produces: `(*db.DB).ReconcileInterrupted(context.Context, time.Time) error`.

- [ ] **Step 1: Write failing fresh-schema and migration tests**

Extend `internal/db/migrations_test.go` to initialize both a fresh database and a rebuilt legacy `executions` database, run `Migrate` twice, then assert these names exist through `sqlite_master`:

```go
for _, name := range []string{
    "execution_components",
    "idx_executions_repo_start",
    "idx_executions_repo_status_end",
    "idx_executions_status",
    "idx_execution_components_execution",
} {
    var got string
    require.NoError(t, testDB.QueryRow(
        `SELECT name FROM sqlite_master WHERE name = ?`, name,
    ).Scan(&got))
    require.Equal(t, name, got)
}
```

- [ ] **Step 2: Write failing store and reconciliation tests**

In `internal/db/execution_store_test.go`, create one execution with code/wiki rows, transition them through running to completed/skipped, and assert ordered results. Add pending and running executions, call `ReconcileInterrupted(fixedNow)`, and assert both executions and active component rows become failed with `end_time=fixedNow` and an interruption message. Assert `ExecutionExists` distinguishes a known ID from an unknown one, and `ActiveExecutionExists` is true only for pending/running rows with the exact normalized `repo_key`.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/db`

Expected: compilation fails because component types and DB methods do not exist.

- [ ] **Step 4: Add the additive schema and indexes**

Add `CREATE TABLE IF NOT EXISTS execution_components` and all four `CREATE INDEX IF NOT EXISTS` statements from the design spec to both fresh initialization and `Migrate`. Define:

```go
type ComponentName string
type ComponentStatus string

const (
    ComponentCode ComponentName = "code"
    ComponentRelease ComponentName = "release"
    ComponentIssue ComponentName = "issues"
    ComponentWiki ComponentName = "wiki"
    ComponentDiscussion ComponentName = "discussion"

    ComponentPending ComponentStatus = "pending"
    ComponentRunning ComponentStatus = "running"
    ComponentCompleted ComponentStatus = "completed"
    ComponentFailed ComponentStatus = "failed"
    ComponentSkipped ComponentStatus = "skipped"
    ComponentCancelled ComponentStatus = "cancelled"
)
```

- [ ] **Step 5: Implement transactional store helpers and reconciliation**

Use `ExecContext` and one transaction for multi-row creation/reconciliation. `CreateComponents` inserts `pending` rows and relies on `UNIQUE(execution_id, component)`. `ListComponents` orders code, release, issues, wiki, discussion through a SQL `CASE`. `ExecutionExists` and `ActiveExecutionExists` use `SELECT EXISTS(...)`. Reconciliation first finalizes active component rows, then executions:

```sql
UPDATE execution_components
SET status = 'failed', end_time = ?, error_message = ?
WHERE status IN ('pending', 'running');

UPDATE executions
SET status = 'failed', end_time = ?, error_message = ?
WHERE status IN ('pending', 'running');
```

- [ ] **Step 6: Verify GREEN**

Run: `gofmt -w internal/db/models.go internal/db/db.go internal/db/migrate.go internal/db/migrations_test.go internal/db/execution_store.go internal/db/execution_store_test.go`

Run: `go test ./internal/db`

Expected: PASS, including idempotent migration.

- [ ] **Step 7: Commit**

```powershell
git add internal/db
git commit -m "feat(db): persist component execution outcomes"
```

### Task 3: Explicit skipped-component contract

**Files:**
- Create: `internal/syncresult/syncresult.go`
- Create: `internal/syncresult/syncresult_test.go`
- Modify: `internal/wiki/wiki.go`
- Modify: `internal/wiki/wiki_test.go`
- Modify: `cmd/wiki/wiki.go`
- Modify: `cmd/daemon/daemon.go`
- Modify: `cmd/daemon/daemon_test.go`

**Interfaces:**
- Produces: `syncresult.Skip(reason string) error`.
- Produces: `syncresult.SkippedReason(error) (string, bool)`.

- [ ] **Step 1: Write failing wrapped-skip tests**

Create `internal/syncresult/syncresult_test.go`:

```go
func TestSkippedReasonSurvivesWrapping(t *testing.T) {
    err := fmt.Errorf("wiki preflight: %w", Skip("repository has no wiki"))
    reason, ok := SkippedReason(err)
    require.True(t, ok)
    require.Equal(t, "repository has no wiki", reason)
    _, ok = SkippedReason(errors.New("network down"))
    require.False(t, ok)
}
```

- [ ] **Step 2: Add a failing Wiki availability test**

Extract a package-private `wikiAvailability(repoURL string, hasWiki bool) error`, then test that `false` returns a recognizable skip and `true` returns nil. This test avoids a network call while fixing the current fall-through that attempts Wiki sync even after GitHub reports no Wiki.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/syncresult ./internal/wiki ./cmd/wiki ./cmd/daemon`

Expected: FAIL because `syncresult` and `wikiAvailability` do not exist.

- [ ] **Step 4: Implement the skip type and Wiki early return**

Implement an error type discoverable through `errors.As`:

```go
type SkippedError struct{ Reason string }

func (e SkippedError) Error() string { return e.Reason }
func Skip(reason string) error { return SkippedError{Reason: reason} }

func SkippedReason(err error) (string, bool) {
    var skipped SkippedError
    if !errors.As(err, &skipped) { return "", false }
    return skipped.Reason, true
}
```

In `wiki.Sync`, check availability before `repository.Sync` without returning on the supported path:

```go
if err := wikiAvailability(repo.URL, gitrepo.GetHasWiki()); err != nil {
    reason, _ := syncresult.SkippedReason(err)
    ui.Printf("Skipped: %s", reason)
    return err
}
```

- [ ] **Step 5: Make CLI/daemon display skip without failure**

In `cmd/wiki` and `cmd/daemon.runStaggered`, branch on `syncresult.SkippedReason(err)`: print `Skipped: <reason>` and return without an error-level message. Add a daemon test with a fake function returning `syncresult.Skip("not available")` and assert it completes without invoking retry behavior.

- [ ] **Step 6: Verify GREEN and commit**

Run: `gofmt -w internal/syncresult/syncresult.go internal/syncresult/syncresult_test.go internal/wiki/wiki.go internal/wiki/wiki_test.go cmd/wiki/wiki.go cmd/daemon/daemon.go cmd/daemon/daemon_test.go`

Run: `go test ./internal/syncresult ./internal/wiki ./cmd/wiki ./cmd/daemon`

Expected: PASS.

```powershell
git add internal/syncresult internal/wiki cmd/wiki cmd/daemon
git commit -m "feat(sync): distinguish skipped components"
```

### Task 4: Component runner injection and truthful overall results

**Files:**
- Create: `internal/executor/components.go`
- Modify: `internal/executor/executor.go`
- Modify: `internal/executor/executor_test.go`

**Interfaces:**
- Produces: `executor.SyncFunc`.
- Produces: `executor.Runners` with `Code`, `Release`, `Issue`, `Wiki`, and `Discussion` fields.
- Produces: `executor.NewExecutorWithRunners(*logger.Logger, *db.DB, *config.Config, executor.Runners) *executor.Executor`.
- Consumes all component DB methods from Task 2 and skip classification from Task 3.

- [ ] **Step 1: Replace network-dependent executor tests with injected runners**

Define test runners that append component names under a mutex and return controlled errors. Add tests for these sequences:

```text
code=completed, issue=failed, wiki=completed -> overall failed, all three ran
code=failed, issue=completed              -> overall failed, issue still ran
code=completed, wiki=skipped              -> overall completed
context cancelled during issue            -> issue/remaining components cancelled, overall cancelled
```

Assert both `executions.status/error_message` and every `execution_components` row. Add a store-failure test with a SQLite trigger that raises `FAIL` before a component terminal update; assert the still-readable overall execution becomes failed rather than completed. Remove the existing 20-second real-network wait from `TestExecuteJobRunsConfiguredComponents`.

- [ ] **Step 2: Run the focused test to verify RED**

Run: `go test ./internal/executor -run 'TestExecuteJob(Component|Overall|Skip|Cancel)' -count=1`

Expected: compilation fails because runners and component persistence integration are missing.

- [ ] **Step 3: Implement component plans and default runners**

Create:

```go
type SyncFunc func(context.Context, typedef.Repository, []typedef.MultiStorage) error

type Runners struct {
    Code SyncFunc
    Release SyncFunc
    Issue SyncFunc
    Wiki SyncFunc
    Discussion SyncFunc
}

type component struct {
    name db.ComponentName
    run  SyncFunc
}
```

`componentPlan(repo, runners)` always includes code and conditionally appends enabled optional components in release, issue, wiki, discussion order. `NewExecutor` delegates to `NewExecutorWithRunners` with production functions.

- [ ] **Step 4: Implement component state transitions and aggregation**

Create all planned component rows before asynchronous execution starts. For each component, mark running, invoke it, then classify cancellation, skip, failure, or completion. Accumulate failures with their component name and update the overall execution only after component rows are terminal. Use semicolon-separated summaries such as `issue: rate limit; wiki: permission denied` in `executions.error_message`.

- [ ] **Step 5: Verify cancellation and best-effort GREEN**

Run: `gofmt -w internal/executor/components.go internal/executor/executor.go internal/executor/executor_test.go`

Run: `go test -race ./internal/executor ./internal/db ./internal/syncresult`

Expected: PASS with no real network calls and no data races.

- [ ] **Step 6: Commit**

```powershell
git add internal/executor
git commit -m "feat(executor): aggregate component outcomes"
```

### Task 5: Executor-owned pending queue and concurrency limit

**Files:**
- Modify: `internal/executor/executor.go`
- Modify: `internal/executor/executor_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/api_test.go`
- Modify: `cmd/server/server.go`
- Modify: `cmd/server/server_test.go`

**Interfaces:**
- Produces: `executor.ErrRepositoryActive`.
- Produces: `(*executor.Executor).Close() error`.
- Preserves: `ExecuteJob(string) ([]string, error)`, `CancelJob(string) error`, and `RefreshConfig(*config.Config)`.
- Changes newly accepted job responses to deterministic status `pending`.

- [ ] **Step 1: Write failing concurrency, pending, duplicate, and cancellation tests**

Use a blocking fake code runner and atomics. With `ConcurrencyNum: 2`, enqueue six repositories, wait until two enter, and assert `maxRunning == 2` while the other four remain pending. Assert a second `ExecuteJob` for an active repo returns `ErrRepositoryActive` without inserting another row. Cancel one queued job and assert it becomes cancelled without entering the runner.

- [ ] **Step 2: Write failing resize and shutdown tests**

Start with limit one, queue three jobs, call `RefreshConfig` with limit two, and assert a second job enters. Call `Close`, assert queued and running contexts are cancelled, wait for all terminal DB updates, and call `Close` again to prove idempotence.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/executor ./internal/server ./cmd/server`

Expected: tests fail because all jobs currently change to running immediately and actual work is not executor-limited.

- [ ] **Step 4: Implement a dispatcher-backed queue**

Add queue state protected by one mutex/condition:

```go
type queuedJob struct {
    id string
    repo typedef.Repository
    ctx context.Context
}

type Executor struct {
    // existing db/config fields remain
    queueMu sync.Mutex
    queueCond *sync.Cond
    pending []*queuedJob
    jobs map[string]*JobContext
    activeByRepo map[string]string
    running int
    limit int
    closed bool
    dispatcherDone chan struct{}
    workers sync.WaitGroup
}
```

The dispatcher starts lazily on the first enqueue, waits until `len(pending)>0 && running<limit`, then starts exactly one admitted goroutine and increments `running`. Lazy startup prevents read-only API tests from leaking an idle dispatcher. Completion removes both ID and repo-key entries, decrements `running`, and broadcasts. `RefreshConfig` atomically publishes config, applies default limit three for nil config or a zero setting, updates `limit`, and broadcasts.

- [ ] **Step 5: Implement pending cancellation, duplicate suppression, and orderly close**

While holding the queue mutex, reject a key found in `activeByRepo` or reported active by `ActiveExecutionExists`, then reserve `activeByRepo[repo.Key()]` before inserting the execution. Roll the reservation back on insert/component-row/enqueue failure. `CancelJob` removes a queued item and finalizes it cancelled immediately; for a running item it only cancels the context and lets the worker write one terminal result. `Close` rejects new work, cancels/detaches pending jobs, cancels running jobs, wakes the dispatcher, and waits for dispatcher/workers before returning. Register `Executor.Close` with `t.Cleanup` in `newTestExecutor` and every server/cmd test that enqueues work.

- [ ] **Step 6: Wire startup reconciliation and server shutdown order**

In `cmd/server.setupRoutes`, call `database.ReconcileInterrupted(context.Background(), time.Now())` after `Migrate` and before creating `Executor`. In `Server.Close`, stop the scheduler, close the executor, then close the database. Change `CreateJobResponse.Status` from hard-coded running to pending and map `ErrRepositoryActive` to HTTP `409`.

- [ ] **Step 7: Verify GREEN and race safety**

Run: `gofmt -w internal/executor/executor.go internal/executor/executor_test.go internal/server/api.go internal/server/api_test.go cmd/server/server.go cmd/server/server_test.go`

Run: `go test -race ./internal/executor ./internal/server ./cmd/server`

Expected: PASS; maximum observed runner concurrency never exceeds `cocurrencyNum`.

- [ ] **Step 8: Commit**

```powershell
git add internal/executor internal/server/api.go internal/server/api_test.go cmd/server
git commit -m "feat(executor): queue server jobs with real concurrency limits"
```

### Task 6: Indexed repository execution snapshot and health derivation

**Files:**
- Modify: `internal/db/models.go`
- Modify: `internal/db/execution_store.go`
- Modify: `internal/db/execution_store_test.go`
- Create: `internal/server/repository_health.go`
- Create: `internal/server/repository_health_test.go`
- Modify: `internal/server/types.go`
- Modify: `internal/server/helpers.go`
- Modify: `internal/server/helpers_test.go`

**Interfaces:**
- Produces: `db.RepositoryRunStats` and `(*db.DB).RepositoryRunStats(context.Context) (map[string]db.RepositoryRunStats, error)`.
- Produces: `server.RepositoryHealthFilter`, extended `server.RepositoryOverview`, and `server.RepositoryHealthSummary`.
- Produces package-private `buildRepositorySnapshot`, `searchRepositorySnapshot`, `filterRepositorySnapshot`, `sortRepositorySnapshot`, and `summarizeRepositorySnapshot` helpers.

- [ ] **Step 1: Write failing execution-snapshot tests**

Insert success then failure rows for one repo, a running row for another, and tied start times for a third. Assert `RepositoryRunStats` returns latest execution ID/status/start/end/error, latest successful end time, and cumulative counts. For ties, assert descending execution ID is the deterministic winner.

Use this exact shape in `internal/db/models.go`:

```go
type RepositoryRunStats struct {
    LatestExecutionID string
    LatestStatus string
    LatestStart time.Time
    LatestEnd *time.Time
    LatestError string
    LastSuccess *time.Time
    TotalRuns int64
    SuccessRuns int64
    FailedRuns int64
}
```

- [ ] **Step 2: Write failing health and cron-boundary table tests**

Use a fixed `now` and table cases for never synced, stuck, pending, running, failed, cancelled, overdue, and healthy. The overdue assertion must use `schedule.Next(lastAttempt) <= now-grace`; never-run and active repos are not overdue. Add timezone/DST cases using `time.LoadLocation("America/New_York")` and invalid-cron cases that populate `schedule_error` without setting overdue.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/db ./internal/server -run 'Repository(RunStats|Health)|Overdue' -count=1`

Expected: compilation fails because the snapshot and health helpers do not exist.

- [ ] **Step 4: Implement one indexed execution aggregation query**

Use a window-ranked latest row plus grouped totals and last success:

```sql
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
WHERE ranked.rn = 1;
```

Scan nullable dates/messages explicitly and return one immutable map used for the whole API response.

- [ ] **Step 5: Implement health projection, filters, summaries, and stable sorting**

Define the reusable filter and DTOs before the API handler consumes them:

```go
type RepositoryHealthFilter struct {
    Search string
    Health string
    Overdue *bool
    Stuck *bool
    Sort string
    Direction string
}

func buildRepositorySnapshot(
    repos []typedef.Repository,
    stats map[string]db.RepositoryRunStats,
    now time.Time,
    overdueGrace time.Duration,
    stuckThreshold time.Duration,
) []RepositoryOverview

func searchRepositorySnapshot(in []RepositoryOverview, search string) []RepositoryOverview
func summarizeRepositorySnapshot(in []RepositoryOverview) RepositoryHealthSummary
func filterRepositorySnapshot(in []RepositoryOverview, filter RepositoryHealthFilter) []RepositoryOverview
func sortRepositorySnapshot(in []RepositoryOverview, sortKey, direction string)
```

Build an overview for every config entry, preserving `EffectiveURL` and current org-prefix rollup behavior. Set `last_run_time` equal to `last_attempt_time` for compatibility. When either threshold on an in-memory config is non-positive, use the exported Task 1 default; this keeps directly constructed test/server configs safe. Derive health in the exact attention order `stuck`, `failed`, `overdue`, `never_synced`, `cancelled`, `pending`, `running`, `healthy`, calculate latest duration, and break sort ties with lowercase name and normalized key. Summary status buckets are mutually exclusive; overdue/stuck totals are independent and may overlap.

- [ ] **Step 6: Verify GREEN**

Run: `gofmt -w internal/db/models.go internal/db/execution_store.go internal/db/execution_store_test.go internal/server/types.go internal/server/repository_health.go internal/server/repository_health_test.go internal/server/helpers.go internal/server/helpers_test.go`

Run: `go test ./internal/db ./internal/server`

Expected: PASS with deterministic fixed-clock assertions.

- [ ] **Step 7: Commit**

```powershell
git add internal/db internal/server/types.go internal/server/repository_health.go internal/server/repository_health_test.go internal/server/helpers.go internal/server/helpers_test.go
git commit -m "feat(server): derive repository sync health"
```

### Task 7: Repository health and component-detail APIs

**Files:**
- Modify: `internal/server/types.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/repository_test.go`
- Modify: `internal/server/export_test.go`
- Modify: `cmd/server/server.go`

**Interfaces:**
- Extends `GET /api/repositories` with `health`, `overdue`, `stuck`, `sort`, and `direction`.
- Produces: `GET /api/jobs/:id/components`.
- Consumes the repository overview/filter/summary DTOs from Task 6 and produces `ListRepositoriesResponse` plus `ListComponentsResponse` JSON responses.

- [ ] **Step 1: Write failing response-shape and filter tests**

Extend `internal/server/repository_test.go` with fixed execution rows and assert:

```go
type repoView struct {
    LastStatus string `json:"last_status"`
    HealthStatus string `json:"health_status"`
    LastAttemptTime *time.Time `json:"last_attempt_time"`
    LastSuccessTime *time.Time `json:"last_success_time"`
    LastDurationSeconds *int64 `json:"last_duration_seconds"`
    LatestExecutionID string `json:"latest_execution_id"`
    LastErrorMessage string `json:"last_error_message"`
    Overdue bool `json:"overdue"`
    Stuck bool `json:"stuck"`
    ScheduleError string `json:"schedule_error"`
}
```

Exercise `health=failed`, `health=syncing`, `overdue=true`, each sort/direction, search plus health, and pagination. Assert summary counts cover every search match rather than only the returned page. Invalid health, booleans, sort, or direction return `400`.

- [ ] **Step 2: Write failing component-detail endpoint tests**

Register the route in `NewTestServerWithExecutor`; assert a known execution returns ordered component rows, a historical execution returns an empty array with `200`, and an unknown execution returns `404`.

- [ ] **Step 3: Run API tests to verify RED**

Run: `go test ./internal/server -run 'TestGetRepositories|TestGetJobComponents' -count=1`

Expected: response fields, filters, and component route are missing.

- [ ] **Step 4: Extend DTOs and replace the old repository aggregation handler**

Keep the legacy fields and add:

```go
type RepositoryHealthSummary struct {
    Total int `json:"total"`
    Healthy int `json:"healthy"`
    Failed int `json:"failed"`
    Pending int `json:"pending"`
    Running int `json:"running"`
    NeverSynced int `json:"never_synced"`
    Cancelled int `json:"cancelled"`
    Overdue int `json:"overdue"`
    Stuck int `json:"stuck"`
}
```

Have `GetRepositories` load one DB snapshot, combine it with config, apply search, compute summary, apply health/boolean filters, sort, and finally paginate. `syncing` matches pending plus running. Use one fixed `now := time.Now()` throughout the response.

- [ ] **Step 5: Add component-detail handler and routes**

Verify the execution exists before reading components; return `ListComponentsResponse{Components: []db.ComponentExecution{}}` for legacy executions. Register `GET /api/jobs/:id/components` in production and test routers.

- [ ] **Step 6: Verify GREEN and commit**

Run: `gofmt -w internal/server/types.go internal/server/api.go internal/server/repository_test.go internal/server/export_test.go cmd/server/server.go`

Run: `go test ./internal/server ./cmd/server`

Expected: PASS.

```powershell
git add internal/server cmd/server/server.go
git commit -m "feat(api): expose repository health and component details"
```

### Task 8: Safe server-side bulk retry

**Files:**
- Modify: `internal/server/types.go`
- Modify: `internal/server/api.go`
- Create: `internal/server/bulk_jobs_test.go`
- Modify: `internal/server/export_test.go`
- Modify: `cmd/server/server.go`

**Interfaces:**
- Produces: `POST /api/jobs/bulk`.
- Produces DTOs `BulkJobSelector`, `BulkCreateJobsRequest`, and `BulkCreateJobsResponse`.
- Consumes `executor.ErrRepositoryActive` and the repository snapshot/filter helpers.

- [ ] **Step 1: Write failing count-conflict and eligibility tests**

Use failed, cancelled, overdue, healthy, pending, and stuck repos. Post a selector and wrong `expected_count`; assert HTTP `409` includes `actual_count` and creates no execution. With the correct count, assert only failed/cancelled/overdue repos are candidates; stuck remains active and is not retryable.

- [ ] **Step 2: Write failing partial-result and concurrency tests**

Use injected runners and one already-active repository. Assert the response reports exact values for `requested`, `queued`, `skipped_active`, `no_longer_eligible`, and `failed_to_enqueue`. Confirm queued work passes through the Task 5 executor and never exceeds its concurrency limit.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/server -run 'TestBulk' -count=1`

Expected: `POST /api/jobs/bulk` is not registered.

- [ ] **Step 4: Implement selector validation and expected-count guard**

Define:

```go
type BulkJobSelector struct {
    Search string `json:"search"`
    Health string `json:"health"`
    Overdue *bool `json:"overdue,omitempty"`
}

type BulkCreateJobsRequest struct {
    Selector BulkJobSelector `json:"selector"`
    ExpectedCount int `json:"expected_count"`
}
```

Rebuild the current snapshot server-side, apply the selector, then intersect with retryable states: failed, cancelled, or `overdue=true`. Return `409` if the eligible count differs from `expected_count`.

- [ ] **Step 5: Enqueue independently and report partial outcomes**

Loop over deterministic name/key order and call `ExecuteJob`. Count `ErrRepositoryActive` separately, continue after individual insertion/enqueue failures, and return aggregate counts without thousands of IDs. Register the protected route in `cmd/server` and test constructors.

- [ ] **Step 6: Verify GREEN and commit**

Run: `gofmt -w internal/server/types.go internal/server/api.go internal/server/bulk_jobs_test.go internal/server/export_test.go cmd/server/server.go`

Run: `go test -race ./internal/server ./internal/executor ./cmd/server`

Expected: PASS.

```powershell
git add internal/server cmd/server/server.go
git commit -m "feat(api): add safe bulk repository retry"
```

### Task 9: Manually refreshed repository health dashboard

**Files:**
- Modify: `web/templates/index.html`
- Modify: `web/static/js/main.js`
- Modify: `web/static/css/main.css`
- Modify: `cmd/server/static_test.go`

**Interfaces:**
- Consumes extended `GET /api/repositories`, `GET /api/jobs/:id/components`, and `POST /api/jobs/bulk`.
- Preserves the explicit `#btn-refresh-repos` refresh control.
- Produces hash query state such as `#/repositories?health=failed&sort=attention`.

- [ ] **Step 1: Write failing embedded-asset regression tests**

Read `web.StaticFS` in `cmd/server/static_test.go` and assert the repository UI contains summary/filter/bulk/detail IDs. Add explicit-refresh guards:

```go
require.NotRegexp(t, `setInterval\([^)]*(renderRepositories|renderApp)`, js)
require.NotContains(t, js, "if (state.logJob === jobId) renderApp()")
require.Contains(t, js, "btn-refresh-repos")
```

The existing `setInterval(refreshMetrics, 15000)` remains allowed because it updates only the top-bar server metric, not the repository page.

- [ ] **Step 2: Run the static test to verify RED**

Run: `go test ./cmd/server -run 'TestStatic|TestRepositoryHealthAssets' -count=1`

Expected: FAIL because health controls and component detail markup do not exist.

- [ ] **Step 3: Add health state, URL synchronization, and server query building**

Extend repository state with `reposHealth`, `reposOverdue`, `reposSort`, and `reposDirection`. Parse the query portion of the hash on navigation and update the hash only on explicit filter/search/sort actions. Build request parameters from that state; do not install timers, observers, or SSE-driven page reloads.

Add focused helpers with these concrete behaviors:

```javascript
function repositoryParams() {
    const p = new URLSearchParams({ page: state.reposPage, limit: 20 });
    if (state.reposSearch) p.set('search', state.reposSearch);
    if (state.reposHealth) p.set('health', state.reposHealth);
    if (state.reposOverdue) p.set('overdue', 'true');
    p.set('sort', state.reposSort || 'attention');
    p.set('direction', state.reposDirection || 'asc');
    return p;
}

function repositoryHealthBadge(repo) {
    return '<span class="badge health-' + esc(repo.health_status) + '">' +
        esc(repo.health_status || 'never_synced') + '</span>';
}

function setRepositoryRoute(next) {
    Object.assign(state, next);
    const p = repositoryParams();
    p.delete('limit');
    location.hash = '#/repositories?' + p.toString();
}

function repositorySummaryCards(summary) {
    const cards = [
        { key: 'total', label: 'All', count: summary.total, health: '' },
        { key: 'healthy', label: 'Healthy', count: summary.healthy, health: 'healthy' },
        { key: 'failed', label: 'Failed', count: summary.failed, health: 'failed' },
        { key: 'syncing', label: 'Syncing', count: summary.pending + summary.running, health: 'syncing' },
        { key: 'never_synced', label: 'Never synced', count: summary.never_synced, health: 'never_synced' },
        { key: 'overdue', label: 'Overdue', count: summary.overdue, overdue: true }
    ];
    return cards.map(card =>
        '<button class="summary-card" data-health="' + esc(card.health || '') +
        '" data-overdue="' + (card.overdue ? 'true' : '') + '">' +
        '<strong>' + esc(card.count) + '</strong><span>' + esc(card.label) + '</span></button>'
    ).join('');
}
```

- [ ] **Step 4: Render the operational table and summary controls**

Render cards for total, healthy, failed, syncing, never synced, and overdue. Render filters for health/overdue and sorts for attention/name/last attempt/last success. Replace configuration-heavy columns with status, name/URL, last attempt, last success, duration, next run, error summary, and actions. Use the API's summary rather than counting the current page.

- [ ] **Step 5: Add component detail and bulk retry interactions**

Add a component section to the existing log modal. `openExecutionDetails(executionID, name)` fetches component rows, shows status/timing/error per component, then opens the existing log stream/history. `retryFilteredRepositories()` confirms the displayed eligible count, posts selector plus `expected_count`, and shows returned aggregate counts. It may render the immediate POST result, but it must not schedule later refreshes.

- [ ] **Step 6: Remove the SSE completion page reload and style the view**

Delete the existing completion-handler call that runs `renderApp()`. Keep closing the EventSource and updating modal state. Add `.summary-grid`, `.summary-card`, health badge variants, component rows, responsive toolbar/table rules, and visible focus styles in `main.css`.

- [ ] **Step 7: Verify embedded assets and manual behavior**

Run: `go test ./cmd/server ./internal/server`

Expected: PASS.

Run the server against a test config, open `#/repositories`, and verify: card/filter URLs are bookmarkable; failure detail opens the matching execution; bulk confirmation displays the server count; clicking Refresh reloads; leaving the page idle and finishing a job does not issue another `/api/repositories` request.

- [ ] **Step 8: Commit**

```powershell
git add web cmd/server/static_test.go
git commit -m "feat(web): add repository sync health dashboard"
```

### Task 10: Performance fixture, documentation, and final verification

**Files:**
- Create: `internal/server/repository_benchmark_test.go`
- Modify: `internal/db/execution_store_test.go`
- Modify: `docs/api.md`
- Modify: `docs/web-ui.md`
- Modify: `README.md`
- Modify: `README_zh.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Documents exact settings, statuses, endpoints, filters, response fields, bulk semantics, and explicit-refresh behavior.
- Produces benchmark `BenchmarkGetRepositories6000` and index-plan regression coverage.

- [ ] **Step 1: Add a 6,000-repository benchmark fixture**

Build 6,000 config entries and insert 100 historical executions per repository inside one transaction before starting the benchmark timer. Benchmark `GET /api/repositories?sort=attention&page=1&limit=20`, failed filtering, overdue filtering, and last-success sorting with `b.Run` sub-benchmarks. Verify response status and decode the response outside the timed setup.

- [ ] **Step 2: Add query-plan assertions**

Use `EXPLAIN QUERY PLAN` for the latest-execution and successful-execution access paths. Join all detail strings and assert they mention `idx_executions_repo_start` and `idx_executions_repo_status_end`; this prevents a future accidental per-repository full scan.

- [ ] **Step 3: Run performance verification**

Run: `go test ./internal/server -run '^$' -bench BenchmarkGetRepositories6000 -benchtime=1x -count=3`

Expected: each overview sub-benchmark reports less than `1s/op` on the supported local SQLite test environment. Record the OS, CPU, Go version, fixture size, and observed values in the commit/PR notes; if the target is missed, optimize the indexed query without introducing the deferred materialized state table.

- [ ] **Step 4: Update API and operations documentation**

Document:

```text
GET /api/repositories?health=failed&overdue=true&sort=attention
GET /api/jobs/:id/components
POST /api/jobs/bulk
syncOverdueGrace: 30m
syncStuckThreshold: 24h
```

Explain last attempt versus last success, skip versus failure, health precedence, summary count scope, expected-count conflicts, executor concurrency, restart reconciliation, and the fact that the Repositories page refreshes only when the user requests it.

- [ ] **Step 5: Format and run static verification**

Run:

```powershell
gofmt -w internal/config/config.go internal/config/config_test.go internal/config/importexport.go internal/config/importexport_test.go internal/db/models.go internal/db/db.go internal/db/migrate.go internal/db/migrations_test.go internal/db/execution_store.go internal/db/execution_store_test.go internal/syncresult/syncresult.go internal/syncresult/syncresult_test.go internal/wiki/wiki.go internal/wiki/wiki_test.go internal/executor/components.go internal/executor/executor.go internal/executor/executor_test.go internal/server/types.go internal/server/api.go internal/server/config_api.go internal/server/config_api_test.go internal/server/repository_health.go internal/server/repository_health_test.go internal/server/repository_test.go internal/server/bulk_jobs_test.go internal/server/repository_benchmark_test.go cmd/wiki/wiki.go cmd/daemon/daemon.go cmd/daemon/daemon_test.go cmd/server/server.go cmd/server/server_test.go cmd/server/static_test.go
```

Run: `go vet ./...`

Expected: both commands succeed without diagnostics.

- [ ] **Step 6: Run the full test and race suites**

Run: `go test ./...`

Run: `go test -race ./internal/db ./internal/executor ./internal/server ./cmd/server`

Expected: PASS with no failures, hangs, leaked workers, or races.

- [ ] **Step 7: Check worktree scope and whitespace**

Run: `git diff --check`

Run: `git status --short`

Expected: no whitespace errors; `.codex-ui-test/` remains untracked and untouched; only planned feature files are changed.

- [ ] **Step 8: Commit**

```powershell
git add internal/server/repository_benchmark_test.go internal/db/execution_store_test.go docs/api.md docs/web-ui.md README.md README_zh.md CLAUDE.md
git commit -m "docs: describe repository sync health operations"
```

## Final Acceptance

- A user can see fleet-wide counts, find failed/never/overdue/stuck repositories, and sort attention-first without loading all 6,000 rows into the browser.
- Every row distinguishes last attempt from last successful backup and links to the exact latest execution/component details.
- Optional component failure makes the execution fail; unsupported capability is recorded as skipped.
- Cron, manual runs, and bulk retries share the same actual executor concurrency cap and duplicate-active protection.
- Server restart cannot leave an execution permanently pending/running.
- Bulk retry confirms the current server-side eligible count and reports partial outcomes safely.
- The repository page performs no automatic refresh or polling.
- Additive migration, full tests, focused race tests, query-plan checks, and the 6,000-repository benchmark all pass.
