# GitHub API Coordination Design

## Purpose

gitrieve currently retries individual GitHub REST and GraphQL calls, but concurrent repository jobs do not share rate-limit state. A group of jobs can therefore exhaust the same credential together, sleep independently, and retry in another burst.

The first phase introduces process-wide request coordination, rate-limit observability, retry jitter, and daemon schedule staggering. It does not add multiple credentials or change authentication methods.

## Scope

This phase covers GitHub API calls used by:

- issue and issue-comment synchronization through `go-github`;
- discussion, comment, and reply synchronization through `githubv4`;
- release and release-asset metadata through `go-github`.

Release asset body downloads are excluded from API request gating after GitHub has returned the download location. Manual commands are not delayed by schedule jitter. Git clone/fetch and wiki behavior are unchanged.

## Configuration

The following top-level options are added:

```yaml
githubApiConcurrency: 2
githubMinRequestInterval: 200ms
githubLowRemainingThreshold: 100
githubScheduleJitter: 30s
```

- `githubApiConcurrency` is the maximum number of in-flight GitHub API calls in this process. Zero uses the default of 2.
- `githubMinRequestInterval` is the minimum interval between the start of consecutive API calls. Zero uses the default of 200 milliseconds.
- `githubLowRemainingThreshold` causes a primary rate-limit bucket to pause until reset when its reported remaining value is at or below the threshold. Zero uses the default of 100.
- `githubScheduleJitter` is the maximum random delay applied after a daemon cron trigger. Zero explicitly disables schedule staggering.
- Negative durations are invalid and fall back to their defaults, except that zero jitter remains the documented off switch.

Existing configuration files remain valid. The fields participate in configuration save, export, import preview, apply, and reload just like the existing GitHub retry settings.

## Architecture

### Shared coordinator

A new `internal/githubapi` package owns a process-wide coordinator. It provides a small API for acquiring permission before a request and reporting the result afterward. The coordinator is initialized from the current configuration and can be safely replaced when configuration reload publishes a new complete config instance.

Before every covered API attempt, the caller:

1. waits for any global pause to expire;
2. waits for an API concurrency slot;
3. observes the minimum request-start interval;
4. sends the request;
5. reports response headers or the returned error;
6. releases the concurrency slot.

Waiting is context-aware. Cancellation removes the waiter without leaking a slot or timer.

REST and GraphQL share concurrency and request-start pacing because GitHub's secondary concurrency limit spans both APIs. Primary quota observations are tracked by resource name where available so exhausting one REST bucket does not incorrectly overwrite another bucket's state. A secondary-limit pause is process-wide.

### Rate-limit observation

For REST responses, the coordinator consumes:

- `x-ratelimit-resource`;
- `x-ratelimit-limit`;
- `x-ratelimit-remaining`;
- `x-ratelimit-used`;
- `x-ratelimit-reset`;
- `retry-after`.

For GraphQL calls, query structs include GitHub's `rateLimit` fields (`cost`, `remaining`, and `resetAt`) and report them after successful queries. GraphQL errors continue to be classified by the retry package and may also establish a process-wide secondary pause or a reset-based primary pause when timing information is present.

When a bucket reports remaining capacity at or below `githubLowRemainingThreshold`, later requests for that bucket wait until its reset time. A primary-limit error always pauses until the reported reset time. A secondary-limit response pauses all GitHub API traffic for `Retry-After`; when GitHub supplies no duration, the pause is at least one minute.

Observability is emitted through the existing UI/logger path. Logs are produced when a bucket first becomes low, when a global pause starts or is extended, and when traffic resumes. Tokens and authorization headers are never logged. Routine successful requests do not generate per-request quota logs.

### Retry jitter

`internal/retry` retains responsibility for retry count, retryable-error classification, and exponential backoff. Each retry attempt re-enters the shared coordinator.

Fallback exponential backoff gains bounded random jitter so several jobs do not wake together. Exact server-provided reset and `Retry-After` deadlines remain authoritative and are not shortened by randomness. Randomness is injectable in tests.

### Daemon schedule staggering

Only daemon-triggered jobs receive a random delay in the inclusive range from zero to `githubScheduleJitter`. The delay occurs before component execution and respects scheduler/job cancellation. A zero value disables it. Direct CLI commands and Web/API-triggered immediate executions retain their current start behavior.

## Integration

Existing client libraries and component boundaries remain intact:

- issue wraps each `ListByRepo` and `ListComments` attempt;
- discussion wraps each discussions/comments/replies `Query` attempt and reports GraphQL cost data;
- the GitHub SCM release client wraps release and release-asset metadata attempts;
- the existing retry loop remains outside the individual network attempt, ensuring every retry is gated again.

The release singleton must no longer permanently capture the token and coordinator from the first configuration load. Client construction must observe the current complete configuration after reload, without mutating shared clients while requests are active.

## Error Handling

- Context cancellation always wins over pacing, quota waits, retry sleeps, and daemon jitter.
- `401` remains non-retryable and does not rotate or disable credentials in this phase.
- Permission-related `403` remains non-retryable unless GitHub headers or the known error form identify a primary or secondary rate limit.
- `429`, secondary-limit `403`, primary-limit errors, supported `5xx`, and network errors retain existing retry behavior.
- Invalid or missing rate-limit headers do not fail an otherwise successful API call.
- Continued failures still stop after `retryMaxCount`; global coordination does not create unbounded retries.

## Testing

Unit tests use injected clocks, timers, and random sources where waiting would otherwise make tests slow or flaky. Coverage includes:

- maximum in-flight request enforcement;
- minimum request-start spacing;
- context cancellation while queued or paused;
- REST header parsing and per-resource quota state;
- low-remaining and primary-reset pauses;
- secondary-limit process-wide pause and extension;
- GraphQL cost/remaining/reset reporting;
- jitter bounds and deterministic retry tests;
- daemon jitter range, cancellation, and zero-value disablement;
- defaults, validation, save/import/export/apply/reload behavior for all new fields;
- confirmation that manual commands are not staggered;
- confirmation that release clients observe a reloaded token.

The full `go test ./...` suite is the final verification gate.

## Out of Scope

- multiple PATs or credential rotation;
- GitHub App authentication;
- webhook-driven synchronization;
- persistent quota state across process restarts;
- cross-process or cross-host coordination;
- conditional REST requests with ETags;
- changing archive/cache semantics.

