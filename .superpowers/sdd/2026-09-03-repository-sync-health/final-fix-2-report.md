# Final branch review - Fix round 2 report

Date: 2026-09-04
Base: `1e9f54c`

## Result

All three Fix Round 2 findings are resolved. GitHub API traffic from old and
new runtime generations now shares one process arbiter, wiki preflight and
user/org listing use the coordinated contextual retry path, and expansion
failures or empty results can no longer be reported as successfully queued.

## Implementation decisions

### Stable process-wide GitHub API arbiter

- `githubapi.Scope` retains only the immutable API config accepted with its job
  generation so job snapshot assertions remain meaningful.
- `Acquire` always delegates to the single coordinator stored in `current`.
  `Publish` updates that coordinator's live config in place instead of replacing
  it, preserving active count, start pacing, resource quota pauses, and the
  secondary-limit pause across configuration publication.
- The old fixed-capacity channel was replaced by mutex-protected `active` state
  and a broadcast `changed` channel. Raising concurrency wakes waiters;
  lowering it admits nothing new until active calls drain below the new limit.
  Cancellation removes no capacity and cannot leak a permit.
- The mutex is held only while inspecting/updating arbiter state. It is released
  before timer waits and before the caller performs network I/O.
- Existing config/GitHub publication read-boundary tests remain in force and
  now assert that publication reconfigures the same coordinator instance.

### Coordinated wiki preflight and user/org listing

- Each wiki repository-existence REST attempt now uses the job context,
  `config.GetRetryConfigContext`, `githubapi.Acquire("core")`, and
  `Done(ObserveREST(...))`. Existing no-wiki skip reason and error presentation
  are unchanged.
- `scm/github.Client.GetReposContext` applies the same per-attempt sequence to
  organization/user listing. The legacy `GetRepos` convenience method delegates
  with a background context for CLI compatibility; executor/API expansion calls
  only the contextual method.
- HTTP-backed regressions prove permit gating, cancellation, job-snapshot retry,
  REST quota observation, and permit release.

### Fallible expansion and enqueue accounting

- `repository.ExpandContext` now returns `([]typedef.Repository, error)` and
  propagates contextual client creation/listing failures. Successful concrete
  repositories still inherit the configured cron/storage/component options.
- Executor production expansion propagates the error before reservation or
  persistence. A zero-result expansion returns typed
  `RepositoryExpansionEmptyError`.
- `NewExecutorWithRunners` retains its existing optional slice-only expander
  injection for compatibility, but zero results through that seam receive the
  same typed failure.
- Bulk's existing default enqueue-error branch counts expansion errors as
  `failed_to_enqueue`. The regression proves an empty user expansion reports
  queued=0, failed_to_enqueue=1, and persists no pending execution. Existing
  normal user/org tests still prove one execution per concrete repository.

## TDD evidence

The behavior tests were written and observed failing before each implementation:

1. Stable arbiter RED:
   - new-generation acquisition returned `nil` error while an old-generation
     permit was held;
   - a new scope bypassed the old scope's secondary pause;
   - a waiter returned before cancellation/resizing because each scope owned an
     independent coordinator.
2. Wiki RED:
   - HTTP was reached while a process permit was held;
   - cancellation did not interrupt a coordinated wait because no wait existed;
   - the first transient failure returned immediately instead of using the job
     snapshot's retry count.
3. Listing/expansion RED:
   - `GetReposContext` was undefined;
   - `ExpandContext` supported only one return value and silently collapsed
     errors into an empty slice;
   - after correcting the user-prefix history fixture and removing the new
     guard for the mutation check, bulk reported `queued=1` for an empty
     expansion. Restoring the typed guard produced the required accounting.

Repeated focused GREEN output:

```text
> go test ./internal/githubapi ./internal/scm/github ./internal/wiki ./internal/repository ./internal/executor ./internal/server -run '^(TestScopesShareOneProcessArbiterAcrossGenerations|TestScopePublicationPreservesObservedSecondaryPause|TestProcessArbiterCancellationAndResizeWakeSafely|TestGetReposContextCancellationInterruptsScopedPermitWait|TestGetReposContextRetriesFromSnapshotAndObservesREST|TestWikiPreflightWaitsForScopedPermitAndReleasesItAfterObservation|TestWikiPreflightCancellationInterruptsPermitWait|TestWikiPreflightRetriesFromJobSnapshot|TestExpandContextPropagatesClientAndListFailures|TestExpandContextPassesCancellationToListing|TestExpandContextReturnsConcreteRepositories|TestExecuteJobPropagatesProductionExpansionFailure|TestExecuteJobRejectsEmptyExpansionAsTypedEnqueueFailure|TestExecuteJobExpandsOrgIntoMultipleJobs|TestBulkEmptyUserExpansionCountsFailedToEnqueue|TestBulkUserRetryUsesUnicodePrefixIndexAndCountsOneCandidateForMultipleExecutions)$' -count=10
ok   github.com/wnarutou/gitrieve/internal/githubapi 3.737s
ok   github.com/wnarutou/gitrieve/internal/scm/github 11.486s
ok   github.com/wnarutou/gitrieve/internal/wiki 4.024s
ok   github.com/wnarutou/gitrieve/internal/repository 3.222s
ok   github.com/wnarutou/gitrieve/internal/executor 3.394s
ok   github.com/wnarutou/gitrieve/internal/server 3.249s
```

## Exact final verification output

### Full tests

```text
> go test ./... -count=1
?    github.com/wnarutou/gitrieve [no test files]
ok   github.com/wnarutou/gitrieve/cmd 5.719s
ok   github.com/wnarutou/gitrieve/cmd/daemon 0.699s
?    github.com/wnarutou/gitrieve/cmd/discussion [no test files]
?    github.com/wnarutou/gitrieve/cmd/issue [no test files]
?    github.com/wnarutou/gitrieve/cmd/release [no test files]
?    github.com/wnarutou/gitrieve/cmd/repository [no test files]
?    github.com/wnarutou/gitrieve/cmd/run [no test files]
ok   github.com/wnarutou/gitrieve/cmd/server 9.151s
?    github.com/wnarutou/gitrieve/cmd/wiki [no test files]
ok   github.com/wnarutou/gitrieve/internal/archive 7.806s
ok   github.com/wnarutou/gitrieve/internal/auth 1.139s
ok   github.com/wnarutou/gitrieve/internal/config 8.089s
ok   github.com/wnarutou/gitrieve/internal/db 6.033s
ok   github.com/wnarutou/gitrieve/internal/discussion 3.536s
ok   github.com/wnarutou/gitrieve/internal/executor 7.890s
ok   github.com/wnarutou/gitrieve/internal/githubapi 6.032s
ok   github.com/wnarutou/gitrieve/internal/issue 2.350s
ok   github.com/wnarutou/gitrieve/internal/lock 2.456s
ok   github.com/wnarutou/gitrieve/internal/logger 1.874s
ok   github.com/wnarutou/gitrieve/internal/monitoring 6.750s
ok   github.com/wnarutou/gitrieve/internal/release 3.573s
ok   github.com/wnarutou/gitrieve/internal/repository 3.039s
ok   github.com/wnarutou/gitrieve/internal/retry 2.484s
?    github.com/wnarutou/gitrieve/internal/scm [no test files]
ok   github.com/wnarutou/gitrieve/internal/scm/github 1.984s
ok   github.com/wnarutou/gitrieve/internal/server 9.670s
?    github.com/wnarutou/gitrieve/internal/storage [no test files]
ok   github.com/wnarutou/gitrieve/internal/syncresult 1.277s
ok   github.com/wnarutou/gitrieve/internal/typedef 0.554s
ok   github.com/wnarutou/gitrieve/internal/ui 0.675s
ok   github.com/wnarutou/gitrieve/internal/wiki 1.618s
?    github.com/wnarutou/gitrieve/web [no test files]
```

### Vet

```text
> go vet ./...
(no output; exit 0)
```

### Race detector

```text
> $env:PATH='C:\Program Files\Vagrant\embedded\mingw64\bin;'+$env:PATH; $env:CGO_ENABLED='1'; go test -race ./internal/config ./internal/db ./internal/githubapi ./internal/repository ./internal/wiki ./internal/executor ./internal/server ./cmd/server -count=1
ok   github.com/wnarutou/gitrieve/internal/config 17.367s
ok   github.com/wnarutou/gitrieve/internal/db 8.035s
ok   github.com/wnarutou/gitrieve/internal/githubapi 1.821s
ok   github.com/wnarutou/gitrieve/internal/repository 9.151s
ok   github.com/wnarutou/gitrieve/internal/wiki 6.467s
ok   github.com/wnarutou/gitrieve/internal/executor 6.729s
ok   github.com/wnarutou/gitrieve/internal/server 10.236s
ok   github.com/wnarutou/gitrieve/cmd/server 6.184s
```

### Repository benchmark

```text
> go test ./internal/server -run '^$' -bench BenchmarkGetRepositories6000 -benchtime=1x -count=1
goos: windows
goarch: amd64
pkg: github.com/wnarutou/gitrieve/internal/server
cpu: AMD Ryzen 5 6600H with Radeon Graphics
BenchmarkGetRepositories6000/attention-12          1  430613500 ns/op  16327296 B/op  331209 allocs/op
BenchmarkGetRepositories6000/failed-12             1  417546100 ns/op  16259952 B/op  330194 allocs/op
BenchmarkGetRepositories6000/overdue-12            1  422060400 ns/op  16260496 B/op  330201 allocs/op
BenchmarkGetRepositories6000/last_success-12       1  416795800 ns/op  16260432 B/op  330199 allocs/op
PASS
ok   github.com/wnarutou/gitrieve/internal/server 18.498s
```

### Frontend syntax, whitespace, and manual-refresh policy

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

The only periodic refresh remains `refreshMetrics`. Repository rendering is
triggered only by navigation, explicit refresh, or successful user mutations;
the job-log SSE block does not reload repository data.
