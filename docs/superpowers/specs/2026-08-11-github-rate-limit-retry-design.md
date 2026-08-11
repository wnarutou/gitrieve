# GitHub API 限频/瞬时错误自动重试设计文档

日期：2026-08-11

## 1. 背景与目标

issue / discussion / release 三个组件的同步通过 GitHub API 完成，会受到 GitHub 的**主限频**（primary rate limit，REST 403 / GraphQL `RATE_LIMITED`）与**次限频**（secondary rate limit，REST 429 或 403）限制，也可能遇到 **5xx 服务端瞬时错误**与**网络瞬时错误**。当前代码对这些情况没有任何重试：任何一次 API 调用遇到限频即让整个同步任务失败退出，一个限频错误可能浪费掉一次完整的 cron 触发。

**目标**：每个同步任务中的单次 API 调用，若遇到限频 / 5xx / 网络瞬时错误，等待一段时间后重试；同一调用重试超过配置次数后，任务以失败退出（返回错误、按既有路径上报）。

已确认的需求决策：

| 决策点 | 结论 |
|---|---|
| 重试范围 | 频率限制（主 + 次）+ 5xx / 网络瞬时错误 |
| 重试计数口径 | **按单次调用**计重试次数，同一调用重试超限则任务失败退出 |
| 等待策略 | **优先遵循 GitHub 返回的恢复时间**（Retry-After / X-RateLimit-Reset / retryAfterSeconds），否则指数退避 |
| 可配置性 | config.yaml 增加全局配置项 + 默认值 |

**范围**：仅 `issue.Sync` / `discussion.Sync` / `release.DownloadAllAssets` 三个组件。code / wiki 走 git 协议，不受 GitHub API 限频影响，不纳入。

## 2. 现状与关键事实

| 组件 | 客户端 | 调用点 | 现状 |
|---|---|---|---|
| issue | go-github v56（REST） | `Issues.ListByRepo`、`Issues.ListComments` | `gh.NewClient(...)` 直连，遇错立即 `return err` |
| discussion | shurcooL/githubv4（GraphQL） | discussions 列表 / comments / replies 三层查询 | `githubv4.NewClient` 直连，遇错立即 `return err` |
| release | go-github v56（经 `internal/scm/github` 网关） | `GetReleases`、`GetReleaseAssets`、`DownloadAsset` | 遇错立即 `return err` |

两个底层库**均不内置重试**，但错误类型/文本可区分：

- **go-github**（REST）：
  - 主限频 `*github.RateLimitError`（403 且 `X-RateLimit-Remaining: 0`），带 `Rate.Reset` 时间。
  - 次限频 `*github.AbuseRateLimitError`（403 且 documentation_url 指向 `#abuse-rate-limits` / `#secondary-rate-limits`），带 `RetryAfter`（解析自 `Retry-After` 头）。
  - **429 不会被 go-github 特殊识别**，落入普通 `*github.ErrorResponse`；但它带 `Response *http.Response`，可读 `Retry-After` / `X-RateLimit-Reset` 头。classify 需自行把 429 判为可重试。
  - 5xx 落入 `*github.ErrorResponse`，`Response.StatusCode` 可判。
- **githubv4 / shurcooL/graphql**（GraphQL）：
  - HTTP 非 200 → 返回 `fmt.Errorf("non-200 OK status code: %v body: %q", resp.Status, body)`，**body 嵌入错误文本**，可解析状态码与 `retryAfterSeconds`。
  - 200 但 errors 数组非空 → 返回该 errors 值，`Error()` 取第一条 message（主限频时含 `API rate limit exceeded`）。
  - 无结构化错误类型，需按错误文本匹配。

## 3. 方案选择

**选 A：共享重试 helper（`internal/retry`），在 8 个调用点包裹。**

- 新建 `internal/retry` 包，提供 `Do(ctx, cfg, fn)`；`classify(err)` 是唯一集中的可重试判定 + 等待时长计算逻辑。
- 在 issue 2 处、discussion 3 处、release 经网关 3 处调用点包裹。
- 顺带把各调用点写死的 `context.Background()` 换成调用方传入的 ctx——这是让「一次等待可被 Ctrl+C 中断」的前提（一次主限频 Reset 可能等一小时，不可中断不可接受）。

不选：

- **方案 B（HTTP transport 层重试 RoundTripper）**：单一咽喉点、未来新调用自动覆盖，但需重放请求体（GraphQL 是 POST，要处理 `GetBody`）、区分幂等/非幂等、避免把大体积 release 资产下载整体缓冲进内存重放——复杂度与出错面高，测试难。否决。
- **方案 C（客户端统一网关 + 重试）**：把 issue 也收编进 `internal/scm/github`。收敛性好但改动面大于 A，收益重叠。否决。

## 4. 新包 `internal/retry`

```go
package retry

// Config 控制单次调用的重试行为。
type Config struct {
    MaxRetries int           // 首次尝试之后的额外重试次数（默认 3，即最多 4 次尝试）
    BaseDelay  time.Duration // 指数退避基准（默认 5s），延迟 = BaseDelay * 2^attempt，封顶 2min
}

// Do 执行 fn；遇到可重试错误（限频 / 5xx / 网络）按配置等待并重试。
// 等待时长优先取 GitHub 报告的恢复时间，否则指数退避。
// 等待期间 ctx 取消立即返回 ctx.Err()。重试耗尽返回最后一次错误。
func Do(ctx context.Context, cfg Config, fn func() error) error
```

**执行流**：attempt 0 → `cfg.MaxRetries`。每次失败后：

1. `classify(err)` 判定是否可重试 + 等待时长。
2. 不可重试，或已是最后一次尝试 → 返回最后一次错误。
3. 可重试 → 睡眠（`select` 于 `ctx.Done()` 与 timer），进入下一次尝试。

**`classify(err) (retryable bool, wait time.Duration)`**（内部函数，独立单测）：

| 错误来源 | 判定 | 等待时长 |
|---|---|---|
| REST 主限频 `*github.RateLimitError` | 可重试 | `time.Until(err.Rate.Reset)` |
| REST 次限频 `*github.AbuseRateLimitError` | 可重试 | `err.RetryAfter`（若非 nil） |
| REST 429（`*github.ErrorResponse`） | 可重试 | `Retry-After` 头（若有） |
| REST 500/502/503/504（`*github.ErrorResponse`） | 可重试 | 退避 |
| 网络层 `*url.Error` | 可重试 | 退避 |
| GraphQL：错误文本含 `rate limit` / `RATE_LIMITED` / `retryAfterSeconds`，或 `non-200 OK status code: 429 / 5xx` | 可重试 | 解析出的 `retryAfterSeconds`（若有） |
| 其余（404、400、403 鉴权等） | **不重试**，立即失败 | — |

REST 路径用 `errors.As` 取类型；GraphQL 路径按错误文本匹配（githubv4 只以字符串暴露错误，body 嵌入其中，`retryAfterSeconds` 可从中正则/子串解析）。`wait <= 0` 表示走退避；`BaseDelay * 2^attempt` 封顶 2min。

## 5. 配置项

`internal/config/config.go` 增加两个全局配置字段 + 带默认值的 getter（沿用 `GetReleaseNumLimit` 写法）：

```go
type Config struct {
    // ...现有字段
    RetryMaxCount  int           `yaml:"retryMaxCount"`
    RetryBaseDelay time.Duration `yaml:"retryBaseDelay"`
}
```

- `GetRetryMaxCount() int`：默认 `3`。
- `GetRetryBaseDelay() time.Duration`：默认 `5 * time.Second`。
- `GetRetryConfig() retry.Config`：组装上面两者返回，调用点一行拿到配置。`config` 导入 `retry`，`retry` 不反向依赖 `config`，无循环依赖。
- `Save()` 同步写入这两个字段，保证 server 持久化配置时不丢。

config.yaml 示例：

```yaml
retryMaxCount: 3     # 单次 API 调用在限频/5xx/网络错误后的最大重试次数
retryBaseDelay: 5s   # 指数退避基准时长（每次重试翻倍，封顶 2min）
```

## 6. 各组件接入点

### issue.go（2 处）

`Issues.ListByRepo` 与 `Issues.ListComments` 各包一层 `retry.Do(ctx, config.GetRetryConfig(), ...)`。分页所需 `resp` 通过闭包捕获；`context.Background()` 换成调用方 `ctx`：

```go
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

### discussion.go（3 处）

discussions 列表 / comments / replies 三个 `client.Query(...)` 各包一层：

```go
err := retry.Do(ctx, config.GetRetryConfig(), func() error {
    return client.Query(ctx, &query, discussionVariables)
})
if err != nil {
    ui.Errorf("Error fetching discussions: %s", err)
    return err
}
```

`context.Background()` 换成 `ctx`。GraphQL 限频的文本识别在 `classify` 统一处理。

### release（3 处，经 `internal/scm/github` 网关）

`GetReleases` / `GetReleaseAssets` / `DownloadAsset` 三个方法**增加 `ctx` 参数**，重试包在方法内部：

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

`release.go` 三个调用点传入自己的 `ctx`（`DownloadAllAssets` 已有 ctx 参数）。

> **不做**：`GetRepos`（org/user 枚举，由 `internal/repository.GetRepositories` 调用）不属于三个同步任务，保持签名不变、不加 retry。

## 7. 失败语义与边界

- **"以失败退出任务"**：重试耗尽 → `retry.Do` 返回最后一次错误 → `Sync` / `DownloadAllAssets` return err → CLI 打印错误并继续下一个 repo / daemon、executor 按组件记录失败。上层调用方零改动。
- **幂等恢复**：失败发生在读 API 阶段，不写坏缓存。issue / discussion 的 `.md` 按页写入，中途失败时已写入的保留（`useCache` 下），下次 sync 按文件时间戳续传，不重复已同步内容；release 先列 releases/assets 再下载，列表阶段失败不产生半成品。
- **可中断**：等待期间响应 Ctrl+C（ctx 取消），一次主限频 Reset 的长时间等待不会挂死进程。
- **明确不做**：
  - HTTP transport 层重试（方案 B）。
  - 下载流建立后中途断流的重试——只重试请求/响应头阶段；流中 `io.ReadAll` 失败维持现状。
  - `GetRepos` 加重试。
  - 引入后见之明的全局重试预算/任务级累计计数（用户已确认按单次调用计）。

## 8. 测试

新增 `internal/retry/retry_test.go`：

- `classify`：逐类型断言（是否可重试, 等待时长）——`*github.RateLimitError`、`*github.AbuseRateLimitError`（含/不含 RetryAfter）、`*github.ErrorResponse` 429（含/不含 Retry-After 头）、5xx、`*url.Error`、GraphQL 文本（`non-200 OK status code: 403 ... retryAfterSeconds`、`API rate limit exceeded`、5xx）、以及不可重试错误（404 等）。
- `Do`：首试成功 / 失败一次后成功 / 重试耗尽返回最后一次错误 / 不可重试错误立即返回 / 等待中 ctx 取消返回 `ctx.Err()` / 遵守 `RateLimitError.Reset` 与 `AbuseRateLimitError.RetryAfter`（用短时长 fake）。
- config getter 默认值测试（可选）。

既有测试不受影响：issue / discussion / release 现有测试只覆盖「ctx 已取消」与「锁被占用」路径，均不触发真实 API 调用。
