# Final branch review - Fix round 1 report

Date: 2026-09-04

## Result

All six findings in `final-fix-1-brief.md` are resolved. Repository data remains
explicit-refresh-only; the only periodic browser refresh is the metrics timer.

## Implementation decisions

### One immutable execution generation per job

- `RuntimeConfigSnapshot` now owns a cloned `config.ExecutionSnapshot` and one
  generation-local `githubapi.Scope`.
- Every admitted job binds both objects to its execution context. Repository
  expansion uses that context too. Existing non-executor CLI/daemon callers
  retain their package-global fallback behavior.
- Code/repository expansion, release, issue, wiki, discussion, SCM GitHub,
  retry, release-limit, GitHub-token/API-coordination, repository, and storage
  paths were audited. Executor component paths now read job-bound values;
  resolved storage values were already copied into `JobContext` and remain
  frozen there.
- Jobs in one generation share pacing/quota state through the generation-local
  GitHub coordinator. Publishing a new config creates a new scope; old jobs do
  not switch scopes. No package global is swapped per job, and no global lock
  is held across network work.
- The only remaining production `config.GetIns()` calls under the audited
  component directories are the legacy `repository.GetRepositories` CLI
  lookup path. Executor expansion uses `repository.ExpandContext` instead.

### Reload is read/publish-only

`POST /api/config/reload` now uses a publication-and-schedule-refresh helper
that never calls `SaveSnapshot`. Mutating config endpoints retain the original
publish, persist, refresh order. The regression preserves comments and
operator formatting byte-for-byte.

### Repository health and cards

Primary health precedence is now:

`never_synced`, `stuck`, active `pending`/`running`, terminal
`failed`/`cancelled`, `overdue`, `healthy`.

Attention sorting remains independent. `overdue` and `stuck` remain independent
diagnostic totals. `summary.healthy` counts only primary `healthy` rows, so it
equals the same search with `health=healthy`, including when completed rows are
overdue.

### Bulk eligibility admission

The bulk-only executor entry point prepares and reserves the complete concrete
batch, validates active/generation state, runs the API eligibility callback
while reservation ownership is held, and persists/publishes only when accepted.
Rejection returns `RepositoryNoLongerEligibleError`; the API accounts it in
`no_longer_eligible`. Cancellation/error paths release every reservation and
persist no pending rows. Existing ordinary `ExecuteJob` behavior is unchanged.

### API documentation and refresh invariant

`docs/api.md` now states that repository limits outside 1-100 return HTTP 400,
includes 400 in the status table, and documents healthy-card/filter parity.
The browser audit found one `setInterval`, calling only `refreshMetrics`; job
log SSE does not render or reload repository data.

## TDD evidence

Regressions were added before their implementations and failed for the intended
reasons:

1. Complete job snapshot: RED failed to compile on the deliberately missing
   `config.GetExecutionConfig` and `githubapi.ConfigFromContext` APIs. After
   context/scope propagation, the test pauses a worker, publishes process and
   executor globals from the new generation, and proves all five component
   runners observe the complete old generation and old storage credentials.
2. Reload bytes: RED reported the operator-formatted 133-byte input had become
   a normalized 607-byte file. The strengthened regression deterministically
   writes another operator edit after the read; GREEN preserves those exact
   later bytes while publishing the generation that was read and refreshing
   executor/schedule state.
3. Health precedence/card parity: RED classified cancelled-plus-overdue as
   `overdue` and counted one completed-overdue row as healthy. GREEN classifies
   it as `cancelled` and reports healthy count zero, equal to the filter total.
4. Bulk completion race: RED queued one redundant execution after the seam
   inserted a successful completion between the API recheck and admission.
   The new reserved callback produces queued=0, no_longer_eligible=1, and no
   pending row. Separate RED tests required the missing typed error/admission
   method; GREEN proves typed accounting plus cancellation/reservation cleanup.

Repeated GREEN output:

```text
> go test ./internal/executor -run '^(TestQueuedJobUsesOneCompleteAcceptedConfigGeneration|TestReservedEligibilityRejectionIsTypedAndReleasesAdmission|TestReservedEligibilityCancellationReleasesAdmissionWithoutPersistence)$' -count=10
ok  github.com/wnarutou/gitrieve/internal/executor  1.800s

> go test ./internal/server -run '^(TestReloadConfig|TestRepositoryHealthUsesAttentionPrecedenceAndSafeDurations|TestRepositoryHealthSearchFilterSummaryAndSortAreDeterministic|TestGetRepositoriesHealthFiltersSortsAndSummarizesSearchMatches|TestBulkCompletionBetweenAPIRecheckAndReservedAdmissionIsNotQueued)$' -count=10
ok  github.com/wnarutou/gitrieve/internal/server  2.379s
```

## Exact final verification output

### Full tests

```text
> go test ./... -count=1
?    github.com/wnarutou/gitrieve [no test files]
ok   github.com/wnarutou/gitrieve/cmd 3.455s
ok   github.com/wnarutou/gitrieve/cmd/daemon 1.904s
?    github.com/wnarutou/gitrieve/cmd/discussion [no test files]
?    github.com/wnarutou/gitrieve/cmd/issue [no test files]
?    github.com/wnarutou/gitrieve/cmd/release [no test files]
?    github.com/wnarutou/gitrieve/cmd/repository [no test files]
?    github.com/wnarutou/gitrieve/cmd/run [no test files]
ok   github.com/wnarutou/gitrieve/cmd/server 9.314s
?    github.com/wnarutou/gitrieve/cmd/wiki [no test files]
ok   github.com/wnarutou/gitrieve/internal/archive 6.556s
ok   github.com/wnarutou/gitrieve/internal/auth 1.226s
ok   github.com/wnarutou/gitrieve/internal/config 6.708s
ok   github.com/wnarutou/gitrieve/internal/db 6.601s
ok   github.com/wnarutou/gitrieve/internal/discussion 6.423s
ok   github.com/wnarutou/gitrieve/internal/executor 7.427s
ok   github.com/wnarutou/gitrieve/internal/githubapi 5.435s
ok   github.com/wnarutou/gitrieve/internal/issue 3.418s
ok   github.com/wnarutou/gitrieve/internal/lock 2.251s
ok   github.com/wnarutou/gitrieve/internal/logger 3.571s
ok   github.com/wnarutou/gitrieve/internal/monitoring 2.289s
ok   github.com/wnarutou/gitrieve/internal/release 5.531s
ok   github.com/wnarutou/gitrieve/internal/repository 1.830s
ok   github.com/wnarutou/gitrieve/internal/retry 1.981s
?    github.com/wnarutou/gitrieve/internal/scm [no test files]
ok   github.com/wnarutou/gitrieve/internal/scm/github 1.168s
ok   github.com/wnarutou/gitrieve/internal/server 9.820s
?    github.com/wnarutou/gitrieve/internal/storage [no test files]
ok   github.com/wnarutou/gitrieve/internal/syncresult 0.828s
ok   github.com/wnarutou/gitrieve/internal/typedef 2.234s
ok   github.com/wnarutou/gitrieve/internal/ui 1.746s
ok   github.com/wnarutou/gitrieve/internal/wiki 1.096s
?    github.com/wnarutou/gitrieve/web [no test files]
```

### Vet

```text
> go vet ./...
(no output; exit 0)
```

### Race detector

```text
> $env:PATH='C:\Program Files\Vagrant\embedded\mingw64\bin;'+$env:PATH; $env:CGO_ENABLED='1'; go test -race ./internal/config ./internal/db ./internal/executor ./internal/server ./cmd/server -count=1
ok   github.com/wnarutou/gitrieve/internal/config 4.459s
ok   github.com/wnarutou/gitrieve/internal/db 5.455s
ok   github.com/wnarutou/gitrieve/internal/executor 6.519s
ok   github.com/wnarutou/gitrieve/internal/server 11.911s
ok   github.com/wnarutou/gitrieve/cmd/server 7.472s
```

### Repository benchmark

```text
> go test ./internal/server -run '^$' -bench BenchmarkGetRepositories6000 -benchtime=1x -count=1
goos: windows
goarch: amd64
pkg: github.com/wnarutou/gitrieve/internal/server
cpu: AMD Ryzen 5 6600H with Radeon Graphics
BenchmarkGetRepositories6000/attention-12          1  430908600 ns/op  16327616 B/op  331211 allocs/op
BenchmarkGetRepositories6000/failed-12             1  429474100 ns/op  16260384 B/op  330199 allocs/op
BenchmarkGetRepositories6000/overdue-12            1  428968200 ns/op  16260400 B/op  330200 allocs/op
BenchmarkGetRepositories6000/last_success-12       1  421091400 ns/op  16260016 B/op  330194 allocs/op
PASS
ok   github.com/wnarutou/gitrieve/internal/server 19.173s
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

`TestRepositoryHealthAssets` also passed in the full suite and statically
asserts that timer callbacks and the log SSE block do not call repository
rendering functions.
