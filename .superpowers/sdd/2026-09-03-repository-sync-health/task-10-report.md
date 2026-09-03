# Task 10 Report: Performance fixture, documentation, and final verification

## Status

Complete.

## Implementation

- Added `BenchmarkGetRepositories6000` with 6,000 configured repositories and
  100 historical executions per repository (600,000 execution rows). Fixture
  construction and the single transaction commit occur before the benchmark
  timer. The four sub-benchmarks exercise attention sorting, failed filtering,
  overdue filtering, and last-success sorting; every iteration checks HTTP 200
  and decodes/validates the response payload.
- Added `EXPLAIN QUERY PLAN` regression coverage over the production repository
  statistics query. The joined detail output must mention both
  `idx_executions_repo_start` and `idx_executions_repo_status_end`.
- Reworked `RepositoryRunStats` from a full-history window plus multiple
  aggregate passes to one covering-index totals pass with indexed correlated
  lookups for latest attempt and latest success. `executions` remains the sole
  source of truth; no materialized state table was introduced.
- Updated the API, Web UI, English/Chinese README, and contributor guidance with
  exact health settings/statuses/endpoints/filters/fields, component semantics,
  bulk retry confirmation/partial results, executor concurrency, restart
  reconciliation, and the explicit-refresh-only repository-page invariant.

## TDD evidence

The first representative benchmark run against the original query exceeded the
one-second target:

| Sub-benchmark | Original query |
|---|---:|
| attention | 4.317699600 s/op |
| failed | 4.272863900 s/op |
| overdue | 4.393945600 s/op |
| last_success | 4.325288300 s/op |

After the indexed-query change, the focused run measured 0.413–0.434 s/op and
the repository-stat semantics/index-plan tests passed.

## Benchmark environment and final results

- OS: Microsoft Windows NT 10.0.26200.0 (`windows/amd64`)
- CPU: AMD Ryzen 5 6600H with Radeon Graphics (AMD64 Family 25 Model 68)
- Go: `go1.27.0 windows/amd64`
- Fixture: 6,000 repositories x 100 executions = 600,000 execution rows;
  setup performed in one transaction before timing
- Command: `go test ./internal/server -run '^$' -bench BenchmarkGetRepositories6000 -benchtime=1x -count=3`

| Sub-benchmark | Run 1 | Run 2 | Run 3 | Target |
|---|---:|---:|---:|---:|
| attention | 418.0522 ms/op | 404.5152 ms/op | 404.6631 ms/op | < 1 s/op |
| failed | 401.9312 ms/op | 399.5494 ms/op | 400.9808 ms/op | < 1 s/op |
| overdue | 404.0659 ms/op | 402.1244 ms/op | 410.5300 ms/op | < 1 s/op |
| last_success | 403.0811 ms/op | 404.6101 ms/op | 403.4757 ms/op | < 1 s/op |

All twelve observations meet the target.

## Verification

- Full Task 10 `gofmt -w` file list: PASS, no diagnostics.
- `go vet ./...`: PASS, no diagnostics.
- `go test ./...`: PASS.
- `CGO_ENABLED=1 go test -race ./internal/db ./internal/executor ./internal/server ./cmd/server`
  with `C:\Program Files\Vagrant\embedded\mingw64\bin` first in `PATH`: PASS.
- `git diff --check`: PASS.
- Scope: only Task 10 benchmark/query/tests/documentation plus this report;
  `.codex-ui-test/` was not modified.

## Concerns

None blocking. The benchmark still allocates about 16 MB and 330,000 objects
per request because the endpoint derives health for the full 6,000-repository
snapshot before returning one page. It is comfortably within the specified
latency target on the recorded environment, and changing that architecture was
outside this task.

## Fix Round 1

### Review fixes

- Made the benchmark history generator shared by the benchmark and a focused
  boundary regression. Its newest execution is now two hours before fixture
  `now`, so hourly cron plus a 30-minute grace period is overdue even during the
  first half-hour. The regression fixes `now` at 12:05 and proves the newest
  attempt (10:05) predates the eligible 11:00 occurrence.
- Strengthened the production-query EXPLAIN check from whole-plan substring
  matching to alias-specific nodes. `recent` must be a keyed `SEARCH` using
  `idx_executions_repo_start` with `repo_key=?`; `successful` must be a keyed
  `SEARCH` using `idx_executions_repo_status_end` with both `repo_key=?` and
  `status=?`. A negative plan removes the successful index while leaving the
  totals scan's same index name present, proving that misleading whole-plan
  containment is rejected.
- Corrected API, Web UI, README, Chinese README, and contributor documentation
  to state that stuck classification applies to latest executions remaining
  either `pending` or `running` beyond `syncStuckThreshold`.

### TDD evidence

- Fixture regression RED: build failed with undefined
  `insertRepositoryBenchmarkHistory`; after extracting and using the shared
  deterministic transaction fixture, the early-hour regression passed.
- Plan regression RED: build failed with undefined alias-aware plan helpers;
  after implementing the strict `SEARCH`/index/key matcher and negative plan,
  the focused DB regression passed.

### Performance rerun

Command: `go test ./internal/server -run '^$' -bench BenchmarkGetRepositories6000 -benchtime=1x -count=3`

| Sub-benchmark | Run 1 | Run 2 | Run 3 | Target |
|---|---:|---:|---:|---:|
| attention | 422.9327 ms/op | 408.6844 ms/op | 406.7927 ms/op | < 1 s/op |
| failed | 408.2615 ms/op | 408.0346 ms/op | 406.1325 ms/op | < 1 s/op |
| overdue | 410.3377 ms/op | 404.5534 ms/op | 409.8713 ms/op | < 1 s/op |
| last_success | 414.0160 ms/op | 417.3786 ms/op | 406.5959 ms/op | < 1 s/op |

All twelve Fix Round 1 observations meet the target. The run started at minute
05, exercising the wall-clock boundary that the previous fixture did not
guarantee, and the overdue handler returned a valid non-empty response.

### Fix Round 1 verification

- Focused DB/server regressions: PASS.
- Three-count 6,000 x 100 handler benchmark: PASS, all results below one second.
- `go test ./...`: PASS.
- `go vet ./...`: PASS, no diagnostics.
- `CGO_ENABLED=1 go test -race ./internal/db ./internal/executor ./internal/server ./cmd/server`
  with the MinGW64 toolchain first in `PATH`: PASS.
- `git diff --check`: PASS.
