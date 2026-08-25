# GitHub API Coordination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Coordinate all issue, discussion, and release GitHub API traffic process-wide, expose quota state in logs, add bounded retry jitter, and stagger daemon jobs.

**Architecture:** A new `internal/githubapi` package owns a replaceable process-wide coordinator with concurrency, pacing, per-resource primary-limit, and global secondary-limit state. Existing REST and GraphQL clients keep their public behavior, but every retry attempt acquires the coordinator and reports its result; daemon tasks add a separately testable random pre-run delay.

**Tech Stack:** Go, `google/go-github/v56`, `shurcooL/githubv4`, `go-co-op/gocron/v2`, Viper, Testify.

**Spec:** `docs/superpowers/specs/2026-08-24-github-api-coordination-design.md`

## Global Constraints

- Do not add multiple PATs, credential rotation, GitHub App authentication, webhooks, persistent quota state, cross-process coordination, or ETag support.
- Defaults are `githubApiConcurrency: 2`, `githubMinRequestInterval: 200ms`, `githubLowRemainingThreshold: 100`, and `githubScheduleJitter: 30s`.
- An explicit zero `githubScheduleJitter` disables daemon staggering; zero values for the other three fields select their defaults.
- All waits must return promptly on `context.Context` cancellation.
- Never log tokens or authorization headers.
- Preserve existing retry count and archive/cache semantics.
- Do not modify or delete the existing untracked `.codex-ui-test/` directory.

---

### Task 1: Configuration lifecycle

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/importexport.go`
- Modify: `internal/config/importexport_test.go`
- Modify: `internal/server/config_api.go`
- Modify: `internal/server/config_api_test.go`
- Modify: `config/example.config.yaml`

**Interfaces:**
- Produces primitive getters `GetGitHubAPIConcurrency() uint`, `GetGitHubMinRequestInterval() time.Duration`, and `GetGitHubLowRemainingThreshold() int`.
- Produces: `GetGitHubScheduleJitter() time.Duration`
- Produces config fields `GitHubAPIConcurrency uint`, `GitHubMinRequestInterval time.Duration`, `GitHubLowRemainingThreshold int`, `GitHubScheduleJitter time.Duration`.

- [ ] **Step 1: Write failing configuration default and round-trip tests**

Add tests which initialize a minimal config and assert `2`, `200*time.Millisecond`, `100`, and `30*time.Second`; add an explicit `githubScheduleJitter: 0s` case that remains zero. Extend export/import tests to assert YAML keys and typed values, and config API diff/apply tests to assert all four globals participate.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/config ./internal/server`

Expected: compilation failures for the missing fields/getters, followed by assertion failures until persistence and API diff/apply are wired.

- [ ] **Step 3: Implement the configuration fields and defaults**

Add the fields with exact YAML/mapstructure spellings. Seed defaults in both `seedDefaults` and `seedExportDefaults`; distinguish absent jitter from explicit `0s` by checking Viper key presence during normal config loading and YAML node/key presence during import parsing. Reject negative duration values by replacing them with documented defaults. Extend `Save`, `ExportFrom`, preview globals, and apply globals. Expose the four primitive getters named in the interface block.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/config ./internal/server`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/config internal/server/config_api.go internal/server/config_api_test.go config/example.config.yaml
git commit -m "feat(config): add GitHub API coordination settings"
```

### Task 2: Shared GitHub API coordinator

**Files:**
- Create: `internal/githubapi/coordinator.go`
- Create: `internal/githubapi/coordinator_test.go`
- Modify: `internal/config/config.go`

**Interfaces:**
- Produces: `type Config struct { Concurrency uint; MinRequestInterval time.Duration; LowRemainingThreshold int }`
- Produces: `type Observation struct { Resource string; Limit, Remaining, Used int; Reset time.Time; Secondary bool; RetryAfter time.Duration }`
- Produces: `type Permit interface { Done(Observation) }`
- Produces: `func Acquire(context.Context) (Permit, error)`
- Produces: `func Configure(Config)` for atomically publishing a fresh coordinator.
- Produces: `func ObserveError(error) Observation` for recognized REST rate-limit errors and GraphQL timing text.
- Produces from `internal/config`: `GetGitHubAPIConfig() githubapi.Config`, assembled from Task 1's primitive fields.

- [ ] **Step 1: Write failing coordinator behavior tests**

Use a locally constructed coordinator with injectable `now func() time.Time`, timer/wait hook, and logger callback. Tests must prove: only `Concurrency` permits are live; request starts are separated by `MinRequestInterval`; a canceled waiter returns `context.Canceled`; resources pause independently at/below the threshold; a secondary observation pauses all resources; a later deadline extends but never shortens a pause; and resumption logs once.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/githubapi`

Expected: FAIL because the package and interfaces do not exist.

- [ ] **Step 3: Implement minimal coordinator state and observation parsing**

Use a buffered channel as the concurrency semaphore and a mutex for `nextStart`, resource deadlines, and the global secondary deadline. `Acquire` loops until the relevant deadline and pacing deadline pass, selects every wait against `ctx.Done()`, then returns a permit whose `Done` is idempotent via `sync.Once`. `Configure` publishes an immutable coordinator pointer atomically so reload never mutates an active instance.

`ObserveError` must recognize `*github.RateLimitError`, `*github.AbuseRateLimitError`, `*github.ErrorResponse` with `429`/rate-limit headers, and GraphQL `retryAfterSeconds`; secondary limits without an explicit duration use one minute.

- [ ] **Step 4: Add logging transition tests and implementation**

Assert that routine observations do not log; first low-resource transition, pause extension, and first successful acquisition after expiry do. Route production callbacks through `ui.Printf` without credential material.

- [ ] **Step 5: Verify GREEN and race safety**

Run: `go test -race ./internal/githubapi ./internal/config`

Expected: PASS with no races.

- [ ] **Step 6: Commit**

```powershell
git add internal/githubapi internal/config/config.go
git commit -m "feat(githubapi): coordinate process-wide API traffic"
```

### Task 3: Retry jitter

**Files:**
- Modify: `internal/retry/retry.go`
- Modify: `internal/retry/retry_test.go`

**Interfaces:**
- Preserves: `func Do(context.Context, Config, func() error) error`
- Adds internally testable: `func jitter(time.Duration, func(int64) int64) time.Duration`.

- [ ] **Step 1: Write failing deterministic jitter tests**

Assert fallback waits stay within `[base, 1.5*base]`, remain capped at `maxBackoff`, and use deterministic injected values. Assert exact `RateLimitError` reset and `AbuseRateLimitError.RetryAfter` waits are unchanged.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/retry`

Expected: FAIL because fallback backoff has no jitter seam.

- [ ] **Step 3: Implement bounded jitter**

Apply random jitter only when `classify` returns no authoritative wait. Use a package-private random function variable guarded for tests or pass it through a private `do` helper; keep public `Do` unchanged. Apply the cap after adding jitter.

- [ ] **Step 4: Verify GREEN**

Run: `go test -race ./internal/retry`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/retry
git commit -m "feat(retry): add bounded retry jitter"
```

### Task 4: REST issue and release integration

**Files:**
- Modify: `internal/issue/issue.go`
- Create: `internal/issue/github_api_test.go`
- Modify: `internal/scm/github/github.go`
- Create: `internal/scm/github/github_test.go`
- Modify: `internal/release/release_test.go`

**Interfaces:**
- Consumes: `githubapi.Acquire(ctx)` and `Permit.Done(Observation)`.
- Preserves public issue/release function signatures.
- Changes `github.New()` to construct a client from the current config rather than a `sync.Once` singleton.

- [ ] **Step 1: Write failing HTTP integration tests**

Use `httptest.Server` and real `go-github` clients. Assert issue list and comment attempts acquire/release coordination, REST headers create quota observations, retries acquire again, and two configured concurrent calls respect the shared cap. Add a release-client test that changes `config.SetIns` between two `New()` calls and observes distinct Authorization headers.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/issue ./internal/scm/github ./internal/release`

Expected: FAIL because calls bypass the coordinator and the release singleton retains the first token.

- [ ] **Step 3: Gate every covered REST attempt**

Inside each existing `retry.Do` closure, acquire a permit, perform the call, convert the returned `*github.Response` plus error into an observation, and call `Done` before returning. Cover `Issues.ListByRepo`, `Issues.ListComments`, `Repositories.ListReleases`, and `Repositories.ListReleaseAssets`.

Cover `DownloadReleaseAsset` long enough to obtain its API response/redirect and report its headers, but release the permit before callers consume the returned body. Remove `sync.Once`; each `New()` captures one immutable current config snapshot so in-flight calls remain stable while later calls observe reloads.

- [ ] **Step 4: Verify GREEN and races**

Run: `go test -race ./internal/issue ./internal/scm/github ./internal/release`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/issue internal/scm/github internal/release/release_test.go
git commit -m "feat(githubapi): coordinate issue and release requests"
```

### Task 5: GraphQL discussion integration and quota fields

**Files:**
- Modify: `internal/discussion/discussion.go`
- Create: `internal/discussion/github_api_test.go`

**Interfaces:**
- Consumes: `githubapi.Acquire`, `Permit.Done`, and `githubapi.ObserveError`.
- Adds a shared embedded GraphQL selection with `Cost int`, `Remaining int`, and `ResetAt time.Time` to all three query structs.

- [ ] **Step 1: Write failing GraphQL integration tests**

Use an HTTP test server returning GraphQL JSON. Assert discussions, comments, and replies each pass through the coordinator; successful `rateLimit` data pauses at the configured threshold; a secondary error without timing creates the one-minute global pause; cancellation exits while paused.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/discussion`

Expected: FAIL because query types omit quota data and calls bypass coordination.

- [ ] **Step 3: Add quota selections and gate each query attempt**

Add `RateLimit struct { Cost int; Remaining int; ResetAt time.Time }` to each top-level query. Acquire inside each `retry.Do` closure, execute `client.Query`, translate successful quota fields or the returned error into an observation, then release the permit.

- [ ] **Step 4: Verify GREEN and races**

Run: `go test -race ./internal/discussion ./internal/githubapi`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/discussion
git commit -m "feat(githubapi): coordinate discussion GraphQL requests"
```

### Task 6: Daemon-only schedule jitter

**Files:**
- Modify: `cmd/daemon/daemon.go`
- Create: `cmd/daemon/daemon_test.go`

**Interfaces:**
- Produces: package-private `func stagger(ctx context.Context, max time.Duration, randN func(int64) int64) error`.
- Produces: package-private task wrapper that calls `stagger` before the original function.

- [ ] **Step 1: Write failing stagger tests**

Assert zero disables waiting, deterministic random values select delays within `[0,max]`, cancellation returns `context.Canceled`, and the wrapped task invokes its component only after the delay succeeds. Verify direct component functions remain unwrapped outside daemon job registration.

- [ ] **Step 2: Verify RED**

Run: `go test ./cmd/daemon`

Expected: FAIL because daemon jobs directly reference component functions.

- [ ] **Step 3: Implement and register the daemon wrapper**

Wrap each daemon-created code, release, issue, wiki, and discussion task with the same context-aware staggering helper. Read the current jitter setting when a trigger starts so configuration reload affects later triggers. Do not alter CLI commands or executor-triggered immediate jobs.

- [ ] **Step 4: Verify GREEN**

Run: `go test -race ./cmd/daemon ./cmd/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add cmd/daemon
git commit -m "feat(daemon): stagger scheduled repository jobs"
```

### Task 7: Documentation and full verification

**Files:**
- Modify: `README.md`
- Modify: `README_zh.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Documents the four exact configuration keys, defaults, zero-jitter behavior, quota logging, and distinction between scheduled and manual runs.

- [ ] **Step 1: Update English and Chinese configuration documentation**

Add a compact example and explain that the settings coordinate one process only. State that the feature does not multiply GitHub quota and does not coordinate separate gitrieve processes or hosts.

- [ ] **Step 2: Run formatting and static checks**

Run: `gofmt -w internal/githubapi internal/config internal/retry internal/issue internal/discussion internal/scm/github cmd/daemon`

Run: `go vet ./...`

Expected: both commands succeed without diagnostics.

- [ ] **Step 3: Run the full test suite with race detection**

Run: `go test -race ./...`

Expected: PASS with no races, hangs, or leaked goroutines.

- [ ] **Step 4: Check the diff and worktree scope**

Run: `git diff --check`

Run: `git status --short`

Expected: no whitespace errors; `.codex-ui-test/` remains untracked and untouched; only planned files are changed.

- [ ] **Step 5: Commit documentation**

```powershell
git add README.md README_zh.md CLAUDE.md
git commit -m "docs: describe GitHub API coordination"
```
