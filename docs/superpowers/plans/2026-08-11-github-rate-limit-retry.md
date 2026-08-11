# GitHub API 限频/瞬时错误自动重试 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 issue / discussion / release 三个同步组件中的单次 GitHub API 调用，在遇到限频 / 5xx / 网络瞬时错误时按配置等待并重试，同一调用重试超限后任务以失败退出（返回错误）。

**Architecture:** 新建共享重试包 `internal/retry`，提供 `Do(ctx, cfg, fn)`；`classify(err)` 集中判定「是否可重试 + 等待时长」（REST 用 go-github 类型，GraphQL 用错误文本）。在 issue 2 处、discussion 3 处、release 经 `internal/scm/github` 网关 3 处共 8 个调用点包裹，并把写死的 `context.Background()` 换成调用方 ctx。配置经 `internal/config` 暴露 `retryMaxCount` / `retryBaseDelay` 两个全局项。

**Tech Stack:** Go 1.23、`github.com/google/go-github/v56`（REST）、`github.com/shurcooL/githubv4`（GraphQL）、`spf13/viper`（配置）、`stretchr/testify`（测试）。

## Global Constraints

- **范围**：仅 `issue.Sync` / `discussion.Sync` / `release.DownloadAllAssets` 三个组件。code / wiki 走 git 协议，不纳入。`internal/scm/github` 的 `GetRepos`（org/user 枚举）**不改签名、不加重试**。
- **可重试错误**：REST 主限频 `*github.RateLimitError`、次限频 `*github.AbuseRateLimitError`、429、5xx（500/502/503/504）、`*url.Error` 网络错误；GraphQL 文本含 `rate limit` / `RATE_LIMITED` / `retryAfterSeconds` 或 `non-200 OK status code: 429/5xx`。其余（404、400、403 鉴权等）**立即失败不重试**。
- **等待时长**：优先取 GitHub 报告的恢复时间（`RateLimitError.Rate.Reset`、`AbuseRateLimitError.RetryAfter`、429 的 `Retry-After` 头、GraphQL 的 `retryAfterSeconds`），否则指数退避 `BaseDelay * 2^attempt`，**封顶 2min**。
- **计数口径**：按单次调用。`retryMaxCount`（默认 3）表示首次之后额外重试次数，共 1+3=4 次尝试；耗尽返回最后一次错误。
- **可中断**：等待期间 `select` 于 `ctx.Done()`，ctx 取消立即返回 `ctx.Err()`。所有调用点把 `context.Background()` 换成调用方 ctx。
- **依赖方向**：`internal/retry` 只依赖 go-github + 标准库；`internal/config` 导入 `internal/retry`；不允许反向依赖/循环依赖。
- **既有测试不得破坏**：issue/discussion/release 现有测试只覆盖「ctx 已取消」与「锁被占用」路径，不触发真实 API 调用。
- Go 版本 go 1.23；验证命令 `go build ./...`、`go test ./...`、`go vet ./...`。

## File Structure

| 文件 | 操作 | 职责 |
|---|---|---|
| `internal/retry/retry.go` | 新建 | `Config`、`Do`、`classify`、`backoff`、`sleep` |
| `internal/retry/retry_test.go` | 新建 | classify 与 Do 的单测 |
| `internal/config/config.go` | 修改 | 加 `RetryMaxCount`/`RetryBaseDelay` 字段、getter、`GetRetryConfig`、`Save` |
| `internal/config/config_test.go` | 新建 | getter 默认值与配置覆盖测试 |
| `internal/issue/issue.go` | 修改 | 包裹 `ListByRepo`、`ListComments` 两个调用点 |
| `internal/scm/github/github.go` | 修改 | `GetReleases`/`GetReleaseAssets`/`DownloadAsset` 加 `ctx` 参数 + 内部 retry |
| `internal/release/release.go` | 修改 | 三个调用点传入 `ctx` |
| `internal/discussion/discussion.go` | 修改 | 包裹 discussions/comments/replies 三个 `client.Query` |
| `CLAUDE.md` | 修改 | 配置 schema 文档补充 `retryMaxCount` / `retryBaseDelay` |

---

### Task 1: `internal/retry` — classify 判定逻辑

**Files:**
- Create: `internal/retry/retry.go`
- Test: `internal/retry/retry_test.go`

**Interfaces:**
- Consumes: go-github 错误类型（`github.RateLimitError`、`github.AbuseRateLimitError`、`github.ErrorResponse`）。
- Produces: 内部函数 `func classify(err error) (retryable bool, wait time.Duration)`。`wait <= 0` 表示用指数退避。

- [ ] **Step 1: Write the failing test**

`internal/retry/retry_test.go`:

```go
package retry

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
)

func resp(status int) *http.Response {
	return &http.Response{StatusCode: status}
}

func TestClassifyRateLimitError(t *testing.T) {
	// Primary REST rate limit: 403 with rate-limit reset in ~2 minutes.
	reset := time.Now().Add(2 * time.Minute)
	err := &github.RateLimitError{
		Rate: github.Rate{Reset: github.Timestamp{Time: reset}},
	}
	retryable, wait := classify(err)
	require.True(t, retryable)
	require.GreaterOrEqual(t, wait, 100*time.Second)
	require.LessOrEqual(t, wait, 2*time.Minute)
}

func TestClassifyAbuseRateLimitError(t *testing.T) {
	// Secondary rate limit with RetryAfter.
	ra := 45 * time.Second
	err := &github.AbuseRateLimitError{RetryAfter: &ra}
	retryable, wait := classify(err)
	require.True(t, retryable)
	require.Equal(t, ra, wait)

	// Secondary rate limit without RetryAfter -> retryable, backoff.
	err = &github.AbuseRateLimitError{}
	retryable, wait = classify(err)
	require.True(t, retryable)
	require.Equal(t, time.Duration(0), wait)
}

func TestClassifyErrorResponse429(t *testing.T) {
	// 429 with Retry-After header -> wait the header.
	h := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	h.Header.Set("Retry-After", "30")
	err := &github.ErrorResponse{Response: h}
	retryable, wait := classify(err)
	require.True(t, retryable)
	require.Equal(t, 30*time.Second, wait)

	// 429 without Retry-After -> retryable, backoff.
	err = &github.ErrorResponse{Response: resp(http.StatusTooManyRequests)}
	retryable, wait = classify(err)
	require.True(t, retryable)
	require.Equal(t, time.Duration(0), wait)
}

func TestClassifyErrorResponse5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		err := &github.ErrorResponse{Response: resp(code)}
		retryable, wait := classify(err)
		require.True(t, retryable, "status %d should be retryable", code)
		require.Equal(t, time.Duration(0), wait)
	}
	// 404 is not retryable.
	err := &github.ErrorResponse{Response: resp(http.StatusNotFound)}
	retryable, _ := classify(err)
	require.False(t, retryable)
}

func TestClassifyURLError(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://api.github.com/x", Err: errors.New("connection reset")}
	retryable, _ := classify(err)
	require.True(t, retryable)
}

func TestClassifyGraphQL(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		retry    bool
		wantWait time.Duration
	}{
		{
			name:  "secondary limit with retryAfterSeconds",
			msg:   `non-200 OK status code: 403 Forbidden body: {"errors":[{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.","extensions":{"retryAfterSeconds":60}}]}`,
			retry: true,
			wantWait: 60 * time.Second,
		},
		{
			name:  "primary limit via errors array",
			msg:   "API rate limit exceeded for user ID 12345.",
			retry: true,
		},
		{
			name:  "429 non-200",
			msg:   "non-200 OK status code: 429 Too Many Requests body: {\"message\":\"rate limit\"}",
			retry: true,
		},
		{
			name:  "503 non-200",
			msg:   "non-200 OK status code: 503 Service Unavailable body: {}",
			retry: true,
		},
		{
			name:  "non-retryable 404",
			msg:   "non-200 OK status code: 404 Not Found body: {\"message\":\"Not Found\"}",
			retry: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryable, wait := classify(errors.New(tc.msg))
			require.Equal(t, tc.retry, retryable)
			if tc.wantWait > 0 {
				require.Equal(t, tc.wantWait, wait)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/retry/ -run TestClassify -v`
Expected: FAIL — `undefined: classify`。

- [ ] **Step 3: Write minimal implementation**

`internal/retry/retry.go`:

```go
// Package retry provides rate-limit- and transient-error-aware retry for
// GitHub API calls (REST via go-github, GraphQL via githubv4).
package retry

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
)

// Config controls per-call retry behavior.
type Config struct {
	MaxRetries int           // retries after the first attempt; 0 = no retry
	BaseDelay  time.Duration // base for exponential backoff (doubles per retry)
}

// maxBackoff caps the exponential-backoff fallback wait.
const maxBackoff = 2 * time.Minute

// classify reports whether err is retryable and, when GitHub reported an exact
// reset/retry time, how long to wait. A returned wait <= 0 means "use
// exponential backoff".
func classify(err error) (retryable bool, wait time.Duration) {
	// REST primary rate limit (403 with rate-limit headers).
	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return true, time.Until(rateLimitErr.Rate.Reset.Time)
	}

	// REST secondary rate limit (403 abuse / secondary-limit response).
	var abuseErr *github.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		if abuseErr.RetryAfter != nil {
			return true, *abuseErr.RetryAfter
		}
		return true, 0
	}

	// Other REST error responses: 429 and 5xx are transient, rest are not.
	var respErr *github.ErrorResponse
	if errors.As(err, &respErr) {
		switch respErr.Response.StatusCode {
		case 429:
			return true, retryAfterHeader(respErr.Response.Header.Get("Retry-After"))
		case 500, 502, 503, 504:
			return true, 0
		default:
			return false, 0
		}
	}

	// Network-level errors (connection reset, timeout, ...).
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true, 0
	}

	// GraphQL (githubv4) surfaces errors only as strings: either the embedded
	// "non-200 OK status code: <code> body: <body>" error, or the errors array
	// whose Error() is the first GraphQL error message.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "retryAfterSeconds"):
		return true, parseRetryAfterSeconds(msg)
	case strings.Contains(msg, "API rate limit exceeded"),
		strings.Contains(msg, "secondary rate limit"),
		strings.Contains(msg, "rate limit"):
		return true, 0
	case strings.Contains(msg, "non-200 OK status code: 429"),
		strings.Contains(msg, "non-200 OK status code: 500"),
		strings.Contains(msg, "non-200 OK status code: 502"),
		strings.Contains(msg, "non-200 OK status code: 503"),
		strings.Contains(msg, "non-200 OK status code: 504"):
		return true, 0
	default:
		return false, 0
	}
}

// retryAfterHeader parses a Retry-After header value (seconds) into a duration.
// An empty or unparseable value yields 0, meaning "use backoff".
func retryAfterHeader(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// parseRetryAfterSeconds extracts retryAfterSeconds from a GraphQL error body
// embedded in the error string. Returns 0 when absent/unparseable.
func parseRetryAfterSeconds(msg string) time.Duration {
	const key = "retryAfterSeconds"
	i := strings.Index(msg, key)
	if i < 0 {
		return 0
	}
	rest := msg[i+len(key):]
	start := -1
	for j := 0; j < len(rest); j++ {
		if rest[j] >= '0' && rest[j] <= '9' {
			start = j
			break
		}
	}
	if start < 0 {
		return 0
	}
	end := start
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	secs, err := strconv.Atoi(rest[start:end])
	if err != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/retry/ -run TestClassify -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/retry/retry.go internal/retry/retry_test.go
git commit -m "feat(retry): classify retryable GitHub API errors
Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: `internal/retry` — Do 重试循环

**Files:**
- Modify: `internal/retry/retry.go`（追加 `Do`、`backoff`、`sleep`）
- Test: `internal/retry/retry_test.go`

**Interfaces:**
- Consumes: `Config`（Task 1 产出）、`classify`。
- Produces: `func Do(ctx context.Context, cfg Config, fn func() error) error`。

- [ ] **Step 1: Write the failing test**

在 `internal/retry/retry_test.go` 追加：

```go
func TestDoSuccessFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		if calls < 3 {
			return &github.ErrorResponse{Response: resp(http.StatusInternalServerError)}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

func TestDoExhaustsRetries(t *testing.T) {
	base := time.Millisecond
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 2, BaseDelay: base}, func() error {
		calls++
		return &github.ErrorResponse{Response: resp(http.StatusBadGateway)}
	})
	require.Error(t, err)
	require.Equal(t, 3, calls) // 1 initial + 2 retries
}

func TestDoNonRetryableImmediate(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		return &github.ErrorResponse{Response: resp(http.StatusNotFound)}
	})
	require.Error(t, err)
	require.Equal(t, 1, calls) // no retry on 404
}

func TestDoContextCancelledDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	firstCall := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- Do(ctx, Config{MaxRetries: 3, BaseDelay: 100 * time.Millisecond}, func() error {
			calls++
			if calls == 1 {
				close(firstCall)
			}
			return &github.ErrorResponse{Response: resp(http.StatusInternalServerError)}
		})
	}()
	<-firstCall // wait until the first attempt actually ran
	cancel()    // then cancel while Do is waiting on backoff
	require.ErrorIs(t, <-errCh, context.Canceled)
	require.Equal(t, 1, calls) // cancelled while waiting, no further attempts
}

func TestDoHonorsRateLimitReset(t *testing.T) {
	// RateLimitError with a far reset must wait ~that long, not the backoff.
	// Use a short reset so the test stays fast; assert the total elapsed
	// roughly equals the reset delta (within tolerance) and succeeds.
	reset := time.Now().Add(150 * time.Millisecond)
	calls := 0
	start := time.Now()
	err := Do(context.Background(), Config{MaxRetries: 1, BaseDelay: time.Second}, func() error {
		calls++
		if calls == 1 {
			return &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}}
		}
		return nil
	})
	require.NoError(t, err)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 120*time.Millisecond)
	require.Less(t, elapsed, time.Second) // waited for reset, not the 1s backoff
}

func TestDoHonorsAbuseRetryAfter(t *testing.T) {
	ra := 150 * time.Millisecond
	calls := 0
	start := time.Now()
	err := Do(context.Background(), Config{MaxRetries: 1, BaseDelay: time.Second}, func() error {
		calls++
		if calls == 1 {
			return &github.AbuseRateLimitError{RetryAfter: &ra}
		}
		return nil
	})
	require.NoError(t, err)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 120*time.Millisecond)
	require.Less(t, elapsed, time.Second)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/retry/ -run TestDo -v`
Expected: FAIL — `undefined: Do`。

- [ ] **Step 3: Write minimal implementation**

追加到 `internal/retry/retry.go`（并给 import 增加 `"context"`）：

```go
// Do runs fn, retrying when the error is classified as retryable (GitHub rate
// limit or transient 5xx/network error). The wait between retries honors the
// reset time GitHub reports when available, else exponential backoff from
// BaseDelay. Waits are interrupted when ctx is done. Returns the last error
// after cfg.MaxRetries retries, or ctx.Err() if cancelled.
func Do(ctx context.Context, cfg Config, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		retryable, wait := classify(lastErr)
		if !retryable || attempt == cfg.MaxRetries {
			return lastErr
		}
		if wait <= 0 {
			wait = backoff(cfg.BaseDelay, attempt)
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
	return lastErr
}

// backoff returns BaseDelay * 2^attempt, capped at maxBackoff. A zero BaseDelay
// falls back to 1s so backoff never sleeps zero.
func backoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	d := base << uint(attempt)
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// sleep waits for d, returning ctx.Err() if ctx is cancelled first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/retry/ -v`
Expected: PASS（Task 1 + Task 2 全部用例）。

- [ ] **Step 5: Commit**

```bash
git add internal/retry/retry.go internal/retry/retry_test.go
git commit -m "feat(retry): add Do retry loop with backoff and ctx awareness
Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: 配置项 `retryMaxCount` / `retryBaseDelay`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`（新建）
- Modify: `CLAUDE.md`（Configuration Schema 补充两项）

**Interfaces:**
- Consumes: `retry.Config`（Task 1 产出）。
- Produces: `GetRetryMaxCount() int`、`GetRetryBaseDelay() time.Duration`、`GetRetryConfig() retry.Config`；Config 新增字段 `RetryMaxCount int`、`RetryBaseDelay time.Duration`。

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`：

```go
package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeTmpConfig points the package global at a temp config file and loads it,
// mirroring the pattern used by internal/release tests. Cleanup resets Path.
func writeTmpConfig(t *testing.T, content string) {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	Path = tmp.Name()
	Init()
	t.Cleanup(func() { Path = "" })
}

func TestGetRetryDefaults(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\n")
	require.Equal(t, 3, GetRetryMaxCount())
	require.Equal(t, 5*time.Second, GetRetryBaseDelay())
	rc := GetRetryConfig()
	require.Equal(t, 3, rc.MaxRetries)
	require.Equal(t, 5*time.Second, rc.BaseDelay)
}

func TestGetRetryFromConfig(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\nretryMaxCount: 7\nretryBaseDelay: 10s\n")
	require.Equal(t, 7, GetRetryMaxCount())
	require.Equal(t, 10*time.Second, GetRetryBaseDelay())
	rc := GetRetryConfig()
	require.Equal(t, 7, rc.MaxRetries)
	require.Equal(t, 10*time.Second, rc.BaseDelay)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestGetRetry -v`
Expected: FAIL — `undefined: GetRetryMaxCount` 等。

- [ ] **Step 3: Write minimal implementation**

`internal/config/config.go`：
- import 增加 `"github.com/wnarutou/gitrieve/internal/retry"`。
- `Config` 结构体追加：

```go
	RetryMaxCount  int           `yaml:"retryMaxCount"`
	RetryBaseDelay time.Duration `yaml:"retryBaseDelay"`
```

- 追加 getter（放在 `GetConcurrencyNum` 之后）：

```go
// GetRetryMaxCount returns the configured max retries per API call. If not
// configured (zero), default to 3 retries after the first attempt.
func GetRetryMaxCount() int {
	if ins.RetryMaxCount == 0 {
		ins.RetryMaxCount = 3
	}
	return ins.RetryMaxCount
}

// GetRetryBaseDelay returns the exponential-backoff base delay. If not
// configured (zero), default to 5 seconds.
func GetRetryBaseDelay() time.Duration {
	if ins.RetryBaseDelay == 0 {
		ins.RetryBaseDelay = 5 * time.Second
	}
	return ins.RetryBaseDelay
}

// GetRetryConfig assembles the retry configuration used by every GitHub API
// call site in the issue/discussion/release syncs.
func GetRetryConfig() retry.Config {
	return retry.Config{
		MaxRetries: GetRetryMaxCount(),
		BaseDelay:  GetRetryBaseDelay(),
	}
}
```

- `Save()` 追加两行（`vp.Set("retryMaxCount", ...)` / `vp.Set("retryBaseDelay", ...)`），与既有字段并列。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS。

- [ ] **Step 5: Update CLAUDE.md config schema**

在 `internal/config/config.go` 的 `Configuration Schema` 段（CLAUDE.md 顶层 `cocurrencyNum` / `releaseSizeLimit` / `releaseNumLimit` 附近）补充：

```markdown
retryMaxCount: <int, default 3>    # per-call max retries on rate-limit/5xx/network errors
retryBaseDelay: <duration, default 5s>  # exponential-backoff base (doubles per retry)
```

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go CLAUDE.md
git commit -m "feat(config): add retryMaxCount/retryBaseDelay options
Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: issue.go 接入（2 个调用点）

**Files:**
- Modify: `internal/issue/issue.go`

**Interfaces:**
- Consumes: `retry.Do`（Task 2）、`config.GetRetryConfig`（Task 3）。
- Produces: `issue.Sync` 中 `ListByRepo` / `ListComments` 两个调用被重试包裹；`context.Background()` 换成调用方 `ctx`。**签名不变**。

> 说明：issue.go 的 go-github client 在 `Sync` 内部构造、不可注入，无法为这层接线写单元测试。本任务的验证靠 `go build` + 既有 `internal/issue` 测试全绿；重试行为本身已由 Task 1–2 单测覆盖。

- [ ] **Step 1: Modify imports**

`internal/issue/issue.go` 的 import 增加：

```go
"github.com/wnarutou/gitrieve/internal/retry"
```

- [ ] **Step 2: Wrap `ListByRepo`**

原代码（`issue.go` ~L148）：

```go
	for {
		issues, resp, err := client.Issues.ListByRepo(context.Background(), r.Owner, r.Name, opt)
		if err != nil {
			ui.Errorf("Error fetching issues, %s", err)
			return err
		}
```

改为：

```go
	for {
		var (
			issues []*gh.Issue
			resp   *gh.Response
		)
		err := retry.Do(ctx, config.GetRetryConfig(), func() error {
			var apiErr error
			issues, resp, apiErr = client.Issues.ListByRepo(ctx, r.Owner, r.Name, opt)
			return apiErr
		})
		if err != nil {
			ui.Errorf("Error fetching issues, %s", err)
			return err
		}
```

- [ ] **Step 3: Wrap `ListComments`**

原代码（`issue.go` ~L167）：

```go
			var allComments []*gh.IssueComment
			for {
				comments, resp, err := client.Issues.ListComments(context.Background(), r.Owner, r.Name, issue.GetNumber(), commentsOpt)
				if err != nil {
					ui.Errorf("Error fetching comments of issue %d, %s", issue.GetNumber(), err)
					return err
				}
```

改为：

```go
			var allComments []*gh.IssueComment
			for {
				var (
					comments []*gh.IssueComment
					resp     *gh.Response
				)
				err := retry.Do(ctx, config.GetRetryConfig(), func() error {
					var apiErr error
					comments, resp, apiErr = client.Issues.ListComments(ctx, r.Owner, r.Name, issue.GetNumber(), commentsOpt)
					return apiErr
				})
				if err != nil {
					ui.Errorf("Error fetching comments of issue %d, %s", issue.GetNumber(), err)
					return err
				}
```

- [ ] **Step 4: Verify build and existing tests**

Run: `go build ./... && go test ./internal/issue/ -v`
Expected: BUILD OK；`TestSyncCancelledContextReturnsImmediately`、`TestSyncBlocksWhileLockHeld` PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/issue/issue.go
git commit -m "feat(issue): retry rate-limited issue API calls
Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: release 接入（`internal/scm/github` 网关 + release.go）

**Files:**
- Modify: `internal/scm/github/github.go`
- Modify: `internal/release/release.go`

**Interfaces:**
- Consumes: `retry.Do`、`config.GetRetryConfig`。
- Produces: `GetReleases(ctx, owner, repo)`、`GetReleaseAssets(ctx, owner, repo, id)`、`DownloadAsset(ctx, owner, repo, id)`——**均新增首参 `ctx context.Context`**。`GetRepos` 保持原签名不变。

- [ ] **Step 1: Modify `internal/scm/github/github.go`**

- import 增加 `"github.com/wnarutou/gitrieve/internal/retry"`（`context`、`internal/config` 已导入）。
- `GetReleases`：

```go
func (c *Client) GetReleases(ctx context.Context, owner, repo string) ([]*github.RepositoryRelease, error) {
	var (
		list []*github.RepositoryRelease
		err  error
	)
	err = retry.Do(ctx, config.GetRetryConfig(), func() error {
		var apiErr error
		list, _, apiErr = c.c.Repositories.ListReleases(ctx, owner, repo, nil)
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}
```

- `GetReleaseAssets`：

```go
func (c *Client) GetReleaseAssets(ctx context.Context, owner, repo string, id int64) ([]*github.ReleaseAsset, error) {
	var (
		list []*github.ReleaseAsset
		err  error
	)
	err = retry.Do(ctx, config.GetRetryConfig(), func() error {
		var apiErr error
		list, _, apiErr = c.c.Repositories.ListReleaseAssets(ctx, owner, repo, id, nil)
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}
```

- `DownloadAsset`：

```go
func (c *Client) DownloadAsset(ctx context.Context, owner, repo string, id int64) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := retry.Do(ctx, config.GetRetryConfig(), func() error {
		var apiErr error
		rc, _, apiErr = c.c.Repositories.DownloadReleaseAsset(ctx, owner, repo, id, http.DefaultClient)
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}
```

> 注：`DownloadReleaseAsset` 返回 `(io.ReadCloser, string, error)`——第二返回值是 redirectURL，弃用。

- [ ] **Step 2: Update `internal/release/release.go` 三个调用点**

- `release.go` ~L57：`releases, err := c.GetReleases(r.Owner, r.Name)` → `releases, err := c.GetReleases(ctx, r.Owner, r.Name)`
- `release.go` ~L84：`assets, err := c.GetReleaseAssets(r.Owner, r.Name, release.GetID())` → `assets, err := c.GetReleaseAssets(ctx, r.Owner, r.Name, release.GetID())`
- `release.go` ~L142：`rc, err := c.DownloadAsset(r.Owner, r.Name, asset.GetID())` → `rc, err := c.DownloadAsset(ctx, r.Owner, r.Name, asset.GetID())`

- [ ] **Step 3: Verify build and existing tests**

Run: `go build ./... && go test ./internal/release/ ./internal/repository/ -v`
Expected: BUILD OK；release 与 repository 既有测试 PASS（`repository.GetRepositories` 调用 `GetRepos` 未被改动）。

- [ ] **Step 4: Commit**

```bash
git add internal/scm/github/github.go internal/release/release.go
git commit -m "feat(release): retry rate-limited release API calls
Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: discussion.go 接入（3 个调用点）

**Files:**
- Modify: `internal/discussion/discussion.go`

**Interfaces:**
- Consumes: `retry.Do`、`config.GetRetryConfig`。
- Produces: `discussion.Sync` 中 discussions/comments/replies 三个 `client.Query` 调用被重试包裹；`context.Background()` 换成调用方 `ctx`。**签名不变**。

- [ ] **Step 1: Modify imports**

`internal/discussion/discussion.go` 的 import 增加：

```go
"github.com/wnarutou/gitrieve/internal/retry"
```

- [ ] **Step 2: Wrap discussions 列表查询**

原代码（`discussion.go` ~L264）：

```go
	for {
		var query discussionsQuery
		err := client.Query(context.Background(), &query, discussionVariables)
		if err != nil {
			ui.Errorf("Error fetching discussions: %s", err)
			return err
		}
```

改为：

```go
	for {
		var query discussionsQuery
		err := retry.Do(ctx, config.GetRetryConfig(), func() error {
			return client.Query(ctx, &query, discussionVariables)
		})
		if err != nil {
			ui.Errorf("Error fetching discussions: %s", err)
			return err
		}
```

- [ ] **Step 3: Wrap comments 查询**

原代码（`discussion.go` ~L308）：

```go
			for {
				var commentsQuery commentsQuery
				err := client.Query(context.Background(), &commentsQuery, commentVariables)
				if err != nil {
					ui.Errorf("Error fetching comments for discussion %d: %s", discussion.Number, err)
					return err
				}
```

改为：

```go
			for {
				var commentsQuery commentsQuery
				err := retry.Do(ctx, config.GetRetryConfig(), func() error {
					return client.Query(ctx, &commentsQuery, commentVariables)
				})
				if err != nil {
					ui.Errorf("Error fetching comments for discussion %d: %s", discussion.Number, err)
					return err
				}
```

- [ ] **Step 4: Wrap replies 查询**

原代码（`discussion.go` ~L339）：

```go
					for {
						var repliesQuery repliesQuery
						err := client.Query(context.Background(), &repliesQuery, replyVariables)
						if err != nil {
							ui.Errorf("Error fetching replies for comment %d: %s", comment.DatabaseId, err)
							return err
						}
```

改为：

```go
					for {
						var repliesQuery repliesQuery
						err := retry.Do(ctx, config.GetRetryConfig(), func() error {
							return client.Query(ctx, &repliesQuery, replyVariables)
						})
						if err != nil {
							ui.Errorf("Error fetching replies for comment %d: %s", comment.DatabaseId, err)
							return err
						}
```

- [ ] **Step 5: Verify build and existing tests**

Run: `go build ./... && go test ./internal/discussion/ -v`
Expected: BUILD OK；`TestSyncCancelledContextReturnsImmediately`、`TestSyncBlocksWhileLockHeld` PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/discussion/discussion.go
git commit -m "feat(discussion): retry rate-limited GraphQL queries
Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7: 全仓验证

**Files:**
- 无新增/修改（验证任务）。

- [ ] **Step 1: 全量构建 + 测试 + vet**

Run: `go build ./...`
Expected: 无错误。

Run: `go test ./...`
Expected: 全部 PASS。

Run: `go vet ./...`
Expected: 无告警。

- [ ] **Step 2: 回归确认范围**

确认以下路径未被改动：
- `internal/scm/github` 的 `GetRepos` 签名未变。
- code / wiki 同步（`internal/repository`、`internal/wiki`）未触碰。
- 无任何 `context.Background()` 残留在 issue/discussion 的 GitHub API 调用处（`grep -n "context.Background()" internal/issue/issue.go internal/discussion/discussion.go internal/scm/github/github.go internal/release/release.go` 应无输出）。

- [ ] **Step 3: 最终提交（如 Step 1/2 有遗留改动）**

Run: `git status`
Expected: 工作区干净（各任务已分别提交）；若有遗漏改动，追加一次提交。

---

## Self-Review

**Spec 覆盖核对：**

| Spec 需求 | 对应任务 |
|---|---|
| `internal/retry` 包：`Config` / `Do` / `classify` | Task 1、2 |
| classify 判定表（REST 类型 + GraphQL 文本 + 5xx/网络 + 不重试项） | Task 1 |
| Do 循环、退避、ctx 可中断、耗尽返回最后错误 | Task 2 |
| `retryMaxCount`/`retryBaseDelay` 配置 + 默认值 + `GetRetryConfig` + `Save` | Task 3 |
| issue 2 个调用点接入 + `context.Background()` → ctx | Task 4 |
| release 经网关 3 个调用点接入（方法加 ctx） | Task 5 |
| discussion 3 个调用点接入 | Task 6 |
| 失败语义（重试耗尽返回错误 → Sync return err，上层零改动） | Task 2 的 `Do` 语义 + Task 4–6 保持 return err |
| 明确不做：GetRepos、wiki/code、transport 层重试 | Global Constraints + Task 5 Step 2 回归确认 |
| 测试：classify / Do 单测；既有测试不破坏 | Task 1、2 + Task 4–6 Step 验证 |
| CLAUDE.md 配置 schema 文档 | Task 3 Step 5 |

**占位符扫描：** 所有步骤均含实际代码/命令，无 TBD/TODO/「适当处理」类描述。

**类型一致性：** `Config{MaxRetries, BaseDelay}` 在 Task 1/2 定义、Task 3 `GetRetryConfig` 组装、Task 4–6 使用，命名一致。`GetReleases(ctx, owner, repo)` / `GetReleaseAssets(ctx, owner, repo, id)` / `DownloadAsset(ctx, owner, repo, id)` 签名在 Task 5 定义并与 `release.go` 调用点一致。
