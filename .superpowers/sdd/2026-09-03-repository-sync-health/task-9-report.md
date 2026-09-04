# Task 9 Report: Repository Sync Health Dashboard

## RED / GREEN

- RED: `go test ./cmd/server -run 'TestStatic|TestRepositoryHealthAssets' -count=1` failed because `repositoryParams` and the health dashboard assets did not exist.
- GREEN: the same focused test now passes after the embedded dashboard, component detail, and explicit-refresh guards were added.

## Files

- `web/templates/index.html`: component detail section in the existing log modal.
- `web/static/js/main.js`: bookmarkable repository health state, operational table, bulk retry confirmation/conflict handling, component drill-down, and no SSE completion render.
- `web/static/css/main.css`: summary cards, health/component badges, responsive dashboard/table styling, and visible focus styles.
- `cmd/server/static_test.go`: embedded-asset regression coverage.

## Behavior covered

- Repository data is fetched only on navigation and explicit repository actions. The sole interval remains `refreshMetrics` for the top bar.
- Summary cards use API `summary`; bulk retry uses server-filtered `data.total`, never page rows.
- The retry selector contains only search, health, and optional overdue; a 409 requires a fresh confirmation and retries once.
- Closing the modal or opening ordinary logs invalidates pending component-detail fetches, preventing stale modal changes.

## Verification

- `go test ./cmd/server -run 'TestStatic|TestRepositoryHealthAssets' -count=1`
- `go test ./cmd/server ./internal/server -count=1`
- `go test ./... -count=1`
- `go vet ./cmd/server ./internal/server`
- `git diff --check`

## Concerns

- Browser-level interaction testing was not run in this worktree; embedded static checks and Go API tests cover the required contracts. Existing mojibake in unrelated JavaScript text was left untouched to keep the diff scoped.

## Fix Round 1

### RED / GREEN

- RED: the expanded embedded-asset test failed because bulk retry rebuilt the selector instead of posting a frozen `{selector, expected_count}` attempt and did not have route-epoch guards.
- GREEN: the focused static test now validates frozen selector posting, 409 `actual_count` reconfirmation, route/detail epochs, execution detail controls, EventSource identity checks, and the single allowed metrics interval.

### Fixes

- Bulk retry snapshots the exact selector, expected count, and repository route epoch. A changed route/filter aborts both response handling and the second 409 confirmation.
- Navigation tracks `activeRoute` and a monotonic `routeEpoch`; repository renders, CRUD/run continuations, component details, and detail retry refuse stale updates.
- Execution details now show latest status, attempt time, duration, error, components, logs, and a single-repository retry control only for failed/cancelled/overdue work.
- Closed/replaced EventSources cannot append or update the current log modal.

### Verification

- `node --check web/static/js/main.js`
- `go test ./cmd/server -run 'TestStatic|TestRepositoryHealthAssets' -count=1`
- `go test ./cmd/server ./internal/server -count=1`
- `go test ./... -count=1`
- `go vet ./cmd/server ./internal/server`
- `git diff --check`

## Fix Round 2

- RED: function-scoped static checks exposed the missing stale-modal close path for expanded detail retries.
- GREEN: `renderExecutionDetails` resets the shared retry button, one execution opens its log, expanded results report the exact count and close the stale modal, and zero results report no returned log.
- Static checks now scope repository async guards to each mutating function, validate the `total`-based frozen retry binding, forbid repository renders in the log stream function, and inspect balanced interval/timeout call expressions for forbidden callbacks.

### Verification

- `node --check web/static/js/main.js`
- `go test ./cmd/server -run 'TestStatic|TestRepositoryHealthAssets' -count=1`
- `go test ./cmd/server ./internal/server -count=1`
- `go test ./... -count=1`
- `go vet ./cmd/server ./internal/server`
- `git diff --check`
