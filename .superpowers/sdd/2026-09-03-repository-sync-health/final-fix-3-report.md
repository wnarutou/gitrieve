# Final branch review - Fix round 3 report

Date: 2026-09-04
Base: `425e47f`

## Result

The remaining mixed-policy publication window is closed. API configuration
publication now exposes package-global config, the stable process-wide GitHub
arbiter policy, and the Executor runtime through one reader-observed boundary.
Reload reads and validates an unpublished disk snapshot before using that same
boundary, without writing the file or losing a later external edit.

## Implementation decisions

### Unified publication boundary

- `config.PublishSnapshot` owns the config state lock and delegates to the
  existing `githubapi.Publish` write gate. Its callback stores the immutable
  package config and installs the matching Executor runtime before the stable
  coordinator policy is reconfigured and the gate is released.
- Every API create/update/delete/import publication uses this callback via
  `publishConfigLocked`. Persistence and schedule refresh remain serialized by
  the API generation lock, but stay outside the publication gate.
- The arbiter remains one stable coordinator. Publication only updates its live
  policy; old and new job scopes still share active counts, pacing, and quota
  pauses.
- Runtime snapshot reads used by ordinary and generation-bound admissions now
  take the publication read gate. Reservation takes that gate before `queueMu`
  and releases both before eligibility callbacks, database work, persistence,
  or network expansion.

### Lock order

The production order is:

```text
API configMu -> config stateMu -> publication write gate -> Executor queueMu
                                  publication read gate  -> Executor queueMu
```

No path acquires the publication read gate while already holding `queueMu`.
`githubapi.Acquire` uses the read gate only to select the stable coordinator,
then releases it before permit waits and network operations.

### Unpublished reload snapshots

- `config.ReadReloadSnapshot` reads, unmarshals, applies defaults, and validates
  a fresh viper instance without changing package-global state.
- `config.PublishReloadSnapshot` atomically installs the loaded viper state,
  package config, arbiter policy, and API/Executor runtime callback. It never
  reads or writes `config.yaml` during publication.
- The server reload endpoint now reads the opaque snapshot first and publishes
  it through the unified boundary. A deterministic post-read edit remains
  byte-for-byte unchanged.
- Package-level `config.Reload()` remains compatible and retains its original
  state-lock serialization across the complete disk-read-to-publication span.

## TDD evidence

The Round 3 behavior tests were added before production implementation and the
focused run failed at compile time on the deliberately missing boundaries:

```text
internal/config/importexport_test.go:104:17: undefined: ReadReloadSnapshot
internal/config/importexport_test.go:111:2: undefined: PublishReloadSnapshot
internal/executor/executor_test.go:1740:10: undefined: config.PublishSnapshot
internal/server/config_publication_test.go:187:6: api.afterRuntimePublishForTest undefined
FAIL
```

After implementation, the new focused tests passed:

```text
> go test ./internal/config ./internal/executor ./internal/server -run '^(TestReadReloadSnapshotDoesNotPublishUntilExplicitBoundary|TestReloadConfig|TestConfigPublicationAtomicallyExposesExecutorRuntimeAndTightenedArbiterPolicy|TestUnifiedPublicationDoesNotDeadlockQueueResizeAdmissionReservationAndAcquire)$' -count=1
ok github.com/wnarutou/gitrieve/internal/config
ok github.com/wnarutou/gitrieve/internal/executor
ok github.com/wnarutou/gitrieve/internal/server
```

The first broad `-count=20` run exposed an unrelated fixture assumption in
`TestConcurrentConfigPublicationAndSnapshotReads`: a reload test from a prior
iteration could leave a coherent but out-of-domain global snapshot, which the
test mislabeled as torn. Seeding one allowed generation before its goroutines
start made the test independent of prior test order. Its isolated 20x run and
the complete focused concurrency repetition both passed.

```text
> go test ./internal/config ./internal/githubapi ./internal/executor ./internal/server -run '<focused publication/reload/arbiter/generation/reservation regex>' -count=20
ok github.com/wnarutou/gitrieve/internal/config 2.184s
ok github.com/wnarutou/gitrieve/internal/githubapi 6.044s
ok github.com/wnarutou/gitrieve/internal/executor 2.019s
ok github.com/wnarutou/gitrieve/internal/server 8.020s
```

## Final verification

### Full tests

```text
> go test ./... -count=1
ok github.com/wnarutou/gitrieve/cmd 3.244s
ok github.com/wnarutou/gitrieve/cmd/daemon 1.695s
ok github.com/wnarutou/gitrieve/cmd/server 10.193s
ok github.com/wnarutou/gitrieve/internal/archive 7.430s
ok github.com/wnarutou/gitrieve/internal/auth 3.221s
ok github.com/wnarutou/gitrieve/internal/config 7.710s
ok github.com/wnarutou/gitrieve/internal/db 7.704s
ok github.com/wnarutou/gitrieve/internal/discussion 7.449s
ok github.com/wnarutou/gitrieve/internal/executor 4.308s
ok github.com/wnarutou/gitrieve/internal/githubapi 7.768s
ok github.com/wnarutou/gitrieve/internal/issue 4.754s
ok github.com/wnarutou/gitrieve/internal/lock 3.303s
ok github.com/wnarutou/gitrieve/internal/logger 1.323s
ok github.com/wnarutou/gitrieve/internal/monitoring 1.510s
ok github.com/wnarutou/gitrieve/internal/release 6.406s
ok github.com/wnarutou/gitrieve/internal/repository 1.792s
ok github.com/wnarutou/gitrieve/internal/retry 1.703s
ok github.com/wnarutou/gitrieve/internal/scm/github 2.592s
ok github.com/wnarutou/gitrieve/internal/server 10.387s
ok github.com/wnarutou/gitrieve/internal/syncresult 0.406s
ok github.com/wnarutou/gitrieve/internal/typedef 1.495s
ok github.com/wnarutou/gitrieve/internal/ui 1.844s
ok github.com/wnarutou/gitrieve/internal/wiki 2.696s
```

Packages without tests also completed successfully.

### Vet

```text
> go vet ./...
(no output; exit 0)
```

### Race detector

```text
> PATH='C:\Program Files\Vagrant\embedded\mingw64\bin;...'; CGO_ENABLED=1; go test -race ./internal/config ./internal/db ./internal/githubapi ./internal/repository ./internal/wiki ./internal/executor ./internal/server ./cmd/server -count=1
ok github.com/wnarutou/gitrieve/internal/config 7.788s
ok github.com/wnarutou/gitrieve/internal/db 5.571s
ok github.com/wnarutou/gitrieve/internal/githubapi 5.015s
ok github.com/wnarutou/gitrieve/internal/repository 9.925s
ok github.com/wnarutou/gitrieve/internal/wiki 4.856s
ok github.com/wnarutou/gitrieve/internal/executor 4.446s
ok github.com/wnarutou/gitrieve/internal/server 12.932s
ok github.com/wnarutou/gitrieve/cmd/server 7.749s
```

### 6000 x 100 benchmark

```text
> go test ./internal/server -run '^$' -bench BenchmarkGetRepositories6000 -benchtime=1x -count=1
BenchmarkGetRepositories6000/attention-12       1  425801300 ns/op  16327760 B/op  331212 allocs/op
BenchmarkGetRepositories6000/failed-12          1  400470400 ns/op  16260544 B/op  330200 allocs/op
BenchmarkGetRepositories6000/overdue-12         1  401192900 ns/op  16260496 B/op  330201 allocs/op
BenchmarkGetRepositories6000/last_success-12    1  406633200 ns/op  16260560 B/op  330200 allocs/op
PASS
```

### Frontend syntax, whitespace, and refresh policy

```text
> node --check web/static/js/main.js
(no output; exit 0)

> git diff --check
(no output; exit 0)

> rg -n "setInterval|setTimeout|EventSource" web/static/js/main.js
96:    el._t = setTimeout(() => { el.className = 'toast hidden'; }, 4000);
348:    const es = new EventSource('/api/jobs/' + encodeURIComponent(jobId) + '/logs');
370:    es.onerror = () => { /* EventSource auto-reconnects; dedupe + done event handle the rest */ };
1193:    setInterval(refreshMetrics, 15000);
```

The only periodic refresh remains metrics-only. Repository data still refreshes
only on navigation, explicit refresh, or successful user mutation; the job-log
SSE path does not reload repository data.

## Concerns

None known. The publication callback is intentionally restricted to immutable
in-memory stores; persistence, schedule refresh, permit waiting, and network I/O
remain outside the gate.
