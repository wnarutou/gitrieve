# Gitrieve 下载组件（wiki/release/issue/discussion）取消支持设计文档

日期：2026-08-10

## 1. 背景与目标

任务执行（Web UI 的 job 取消按钮 → `executor.CancelJob` → `context.CancelFunc()`）目前只在 git 相关操作上生效：`repository.Sync(ctx, …)` 已经在每个同步阶段响应取消（见近期提交 52314d6 / d97f28d / bdbf090 / 0932fff / 71b9c6a）。

但四个元数据/内容下载组件完全无视取消：

- `release.DownloadAllAssets`（`internal/release/release.go`）
- `issue.Sync`（`internal/issue/issue.go`）
- `wiki.Sync`（`internal/wiki/wiki.go`）
- `discussion.Sync`（`internal/discussion/discussion.go`）

它们都不接收 `context.Context`，所有 HTTP 请求都用 `context.Background()`，且分页/逐条循环里没有任何取消检查。后果：一个仓库有大量 issue、下载很久时，点击取消没有任何反应，任务会一直跑到组件自然结束才停下。

**目标**：下载途中点取消后，下载结束（优雅停止，完成当前单元）、任务退出、后续流程不再执行。

**已确认的两项选择**：
1. **优雅停止**：正在进行的请求/正在下载的资源/正在拉取的那一页会跑完，然后停止；不中断在途传输，也不下载后续内容。
2. **CLI 也支持 Ctrl-C**：`gitrieve release/issue/wiki/discussion` 命令加 `signal.NotifyContext` 处理 `os.Interrupt`。

## 2. 方案选择

**选 A：给四个下载函数加 `ctx context.Context` 参数，在"单元边界"做优雅取消。**

理由：
- 与 `repository.Sync(ctx, …)` 已有的取消模式完全一致，代码读起来统一。
- 优雅语义下，**不把正在取消的 ctx 塞进 HTTP 请求本身**——否则会中断在途请求/传输，违背"优雅停止"。底层 go-github / githubv4 请求保持现状（`context.Background()`），只在单元边界检查 `ctx.Err()`。

备选并否决：
- **B：只在外层循环检查 ctx、不传参**——取消只在组件之间生效，正是现状，满足不了"下载途中取消"。
- **C：包级全局 cancel、签名不动**——全局可取消副作用难测、易泄漏。

## 3. 优雅取消的核心语义

- 每个自然单元（一页、一个资源、一个 issue/discussion 文件）**结束后**检查 `ctx.Err()`。
- 一旦取消：返回 `ctx.Err()`，退出该组件；**跳过归档上传**；release 末尾的 **storage 删除循环跳过**（取消时绝不能删除已有数据）。
- `useCache=false` 时取消也要清理临时目录（与正常完成的清理一致），不留垃圾；`useCache=true` 保留缓存（下次从现有文件推导 `lastUpdate`，可继续增量）。
- 每个函数**顶部加早退检查**：`if ctx.Err() != nil { return ctx.Err() }`。符合 executor 现有写法，也让"预取消 ctx"的单测无需联网即可通过。

## 4. 各组件改动

公共签名变更（四者统一）：第一个参数变为 `ctx context.Context`。

### 4.1 `release.DownloadAllAssets(ctx, repo, storages)`（`internal/release/release.go`）

检查点（单元边界）：
- 拉完 releases 列表后。
- 拉完某 release 的 assets 后。
- **每个资源下载并写入所有目标 storage 后**（`io.ReadAll` + `PutObject` 是"当前单元"，让其完整跑完）。

取消时：
- 停止后续资源/后续 release 的下载。
- **跳过末尾的 storage 删除循环**（`for _, s := range storages { … DeleteObject … }`）。
- 返回 `ctx.Err()`。

注意：release 资源是整体读入内存后才写存储，优雅停止不会在存储上留下半截文件。

### 4.2 `issue.Sync(ctx, repo, storages)`（`internal/issue/issue.go`）

检查点（单元边界）：
- 每拉完一页 issue 后（`for { issues, resp, err := client.Issues.ListByRepo(...) }` 外层循环的每次迭代结束后）。
- **每个 issue 及其评论写完 `.md` 文件后**（内层逐 issue 处理结束）。

取消时：
- 停止后续 issue/分页。
- **跳过归档与上传**（`isUpdated` 分支里的 `archives.FilesFromDisk` / `format.Archive` / `backend.PutObject`）。
- `useCache=false`：执行与正常完成相同的 `os.RemoveAll(gitDir)` 清理。
- 返回 `ctx.Err()`。

### 4.3 `discussion.Sync(ctx, repo, storages)`（`internal/discussion/discussion.go`）

与 `issue.Sync` 完全同构：discussion 分页循环、逐 discussion（含其评论/回复查询）写完 `.md` 文件后检查。取消时跳过归档上传、`useCache=false` 清理临时目录、返回 `ctx.Err()`。

### 4.4 `wiki.Sync(ctx, repo, storages)`（`internal/wiki/wiki.go`）

- 把 `ctx` 透传给内层 `repository.Sync(ctx, repo, true, storages)`——git 层已支持优雅取消，覆盖 wiki 仓库的 clone/fetch。
- `client.Repositories.Get(context.Background(), …)` 保持现状：单次元数据请求、耗时短，不必改。

## 5. executor 改动（`internal/executor/executor.go`）

`downloadComponents` 的 `run` 闭包把任务 `ctx` 传给四个组件：

```go
run("releases",   job.DownloadReleases,   func() error { return release.DownloadAllAssets(ctx, job, storages) })
run("issues",     job.DownloadIssues,     func() error { return issue.Sync(ctx, job, storages) })
run("wiki",       job.DownloadWiki,       func() error { return wiki.Sync(ctx, job, storages) })
run("discussion", job.DownloadDiscussion, func() error { return discussion.Sync(ctx, job, storages) })
```

日志区分"取消"与"真失败"（`Downloading X` 这行保留，executor 测试依赖它）：

```go
run := func(name string, enabled bool, fn func() error) {
    if !enabled || ctx.Err() != nil {
        return
    }
    ui.Printf("Downloading %s", name)
    if err := fn(); err != nil {
        if ctx.Err() != nil {
            ui.Printf("%s download cancelled", name)
        } else {
            ui.Errorf("Failed to download %s: %v", name, err)
        }
    }
}
```

组件之间已通过闭包顶部的 `ctx.Err()` 短路：一个组件取消后，后续组件不再执行。`executeAsync` 在 `downloadComponents` 之后已有 `if ctx.Err() != nil { … StatusCancelled … }` 检查，**无需改动**。`downloadComponents` 保持无返回值。

## 6. CLI 支持 Ctrl-C

`cmd/release/release.go`、`cmd/issue/issue.go`、`cmd/wiki/wiki.go`、`cmd/discussion/discussion.go` 的 run 函数：

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()
```

把 `ctx` 传给对应组件函数。取消时打印 `cancelled`（而不是 `Error running X, context canceled`），并跳出仓库循环。四个命令文件结构一致（各自 `for _, repo := range repository.GetRepositories(repoName)`）。

## 7. daemon 与其余调用点

- `cmd/daemon/daemon.go`：gocron 任务因签名变化传 `context.Background()`（调度任务无取消入口，行为不变）：
  ```go
  gocron.NewTask(release.DownloadAllAssets, context.Background(), repo, storages)
  gocron.NewTask(issue.Sync,   context.Background(), repo, storages)
  gocron.NewTask(wiki.Sync,    context.Background(), repo, storages)
  gocron.NewTask(discussion.Sync, context.Background(), repo, storages)
  ```
- `cmd/run`、`cmd/repository` 不调用这四个函数，不受影响。
- `internal/scm/github` 封装（`GetReleases` / `GetReleaseAssets` / `DownloadAsset`）不改——优雅语义下不需要请求级取消。

## 8. 测试

- 四个组件各加一个单测：传入**已取消的 ctx**（`ctx, cancel := context.WithCancel(...); cancel()`），断言**立即**返回 `context.Canceled`。因有顶部早退检查，无需任何网络。
- 现有 executor 测试保持通过：`TestExecuteJobRunsConfiguredComponents` 依赖 `Downloading issues` 日志行，本改动保留该行。

## 9. 不做的事（明确范围外）

- 不修改 `internal/scm/github` 封装（无请求级取消）。
- 不给下载组件加超时（优雅停止意味着在途请求跑完；请求挂死是既有行为，非本次引入）。
- `gitrieve run` / `gitrieve repository` 命令不加 Ctrl-C（它们只走 git 同步，已有 ctx 支持）。
- release 覆盖旧归档的已知限制（CLAUDE.md 已记录）不在本次范围。
