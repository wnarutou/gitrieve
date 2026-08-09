# Gitrieve Web UI 改进设计文档

日期：2026-08-09

## 1. 背景与目标

用户对现有 Web UI（`web/`，SPA + hash 路由）有三处不满意，外加一个报错：

1. **Repositories 页**缺少执行按钮、执行时间、执行统计、分页和手动刷新。
2. **Jobs 页**一直在自动刷新（5s 轮询），且带有一个"下拉选仓库执行同步"的功能，该功能要迁移到 Repositories 页；Jobs 页改为输入框模糊过滤。
3. **一个报错**：任务 running/pending 时 `end_time` 为 NULL，`GetJobs` 扫描 NULL 进 `time.Time` 报错。
4. 补充：**日志展示太简陋**——Web 日志窗口只有 3 行通用日志，没有具体的 `ui` 输出详情；代码里日志输出方式不统一（部分走 `ui.`，部分 `fmt.Errorf`/直接调 `logger.Log`），需要统一通过 `ui` 处理。
5. 补充：**Repositories 页也需要一个模糊查询输入框**（仓库多时方便定位）。
6. 补充：**docker-compose 未挂载 `.gitrieve` 缓存目录**，需要补上。

目标：让 Repositories / Jobs 两页的信息完整、可操作、可查询；日志统一走 `ui` 并进入 Web 日志窗口；修复 NULL 扫描 bug；容器内 git 缓存可持久化。

## 2. Bug 修复：`end_time` 为 NULL 的扫描错误

**位置**：`internal/server/api.go` `GetJobs`（约 160-180 行）。

**现状**：`rows.Scan(&job.ID, &job.Name, &startTime, &endTime, ...)` 把 `end_time` 扫进 `time.Time`。SQLite 中 running/pending 任务的 `end_time` 是 NULL（`executor.updateJobStatus` 对非终态不写 `end_time`），扫描 NULL 进 `time.Time` 报 `sql: Scan error ... unsupported Scan, storing driver.Value type <nil> into type *time.Time`。

**改动**：改用 `*time.Time` 接收：

```go
var startTime time.Time
var endTime *time.Time
err := rows.Scan(&job.ID, &job.Name, &startTime, &endTime, &job.Status, &job.ErrorMessage)
job.StartTime = &startTime
job.EndTime = endTime
```

**回归测试**：`TestGetJobs` 新增一个 `end_time` 为 NULL 的 running 任务用例，断言响应 200 且 JSON 中 `end_time` 为 null。

## 3. 后端 API 改动

### 3.1 `GET /api/jobs` 仓库名模糊过滤

- 查询参数 `repository` 从精确匹配（`AND job_name = ?`）改为模糊匹配：

```go
if repository != "" {
    query += " AND job_name LIKE ? ESCAPE '\\'"
    args = append(args, "%"+escapeLike(repository)+"%")
}
```

- 新增小工具 `escapeLike(s string) string`：转义 `%`、`_`、`\`，配合 `ESCAPE '\'`，保证输入被当作字面量。
- 空输入不追加条件 = 查全部。`ORDER BY start_time DESC`、status 过滤、分页逻辑保持不变。
- 现有 `?status=`、`?page=`、`?limit=` 测试继续通过。

### 3.2 `GET /api/repositories` 增强 + 分页 + 搜索

- 新增查询参数：`page`（默认 1）、`limit`（默认 20，上限 100）、`search`（对仓库 `name` 模糊匹配，同样 `LIKE ... ESCAPE`）。
- 返回结构由"裸数组"改为分页结构（见 `internal/server/types.go`）：

```go
type RepositoryOverview struct {
    typedef.Repository
    LastRunTime *time.Time `json:"last_run_time"` // 该仓库最近一次执行 start_time，无则 null
    NextRunTime *time.Time `json:"next_run_time"` // 从 cron 表达式计算的最近一次计划触发时间，无 cron/非法则 null
    TotalRuns   int64      `json:"total_runs"`
    SuccessRuns int64      `json:"success_runs"`
    FailedRuns  int64      `json:"failed_runs"`
}

type ListRepositoriesResponse struct {
    Repositories []RepositoryOverview `json:"repositories"`
    Total        int                  `json:"total"`
    Page         int                  `json:"page"`
    Limit        int                  `json:"limit"`
}
```

- **统计与上次执行时间**：一条聚合查询，按 `job_name` 建 map，合并到配置仓库（没跑过的仓库默认 0/0/0、`last_run_time` 为 null）：

```sql
SELECT job_name,
       MAX(start_time) AS last_run,
       COUNT(*)        AS total,
       COALESCE(SUM(status = 'completed'), 0) AS success,
       COALESCE(SUM(status = 'failed'), 0)    AS failed
FROM executions
GROUP BY job_name
```

- **下次计划执行时间**：新增小函数，用 `github.com/robfig/cron/v3`（已是传递依赖，转正式依赖）解析仓库 `cron` 字段并计算下一次触发；空 cron 或解析失败返回 nil：

```go
func nextRunTime(cronExpr string, now time.Time) *time.Time {
    if cronExpr == "" { return nil }
    sched, err := cron.ParseStandard(cronExpr)
    if err != nil { return nil }
    t := sched.Next(now)
    return &t
}
```

  语义与 daemon 的 gocron 调度一致（同一套标准 5 段 cron + descriptor）。注意：server 进程本身不跑调度器，该字段是"按配置计划应触发的时间"，属于展示语义。

- **分页**：在内存中对 `config.Repository` 切片做（先 `search` 过滤、再分页），仓库数量本身不大。

- **兼容性影响**：`GET /api/repositories` 返回结构变化，需要同步更新 `TestGetRepositories`、`TestCreateRepository`、`TestDeleteRepository` 中对该接口响应的断言（改为读 `data.repositories` 数组）。

## 4. 日志统一：走 `ui` 并进入 Web 日志窗口

### 4.1 现状问题

- 详细进度（clone、checkout、下载 release、归档、文件存储等）全部走 `ui.Printf/Errorf`，**只打终端、不进 DB**。
- 只有 executor 的 3 行（Starting / cancelled|failed / completed）走 `logger.Log` 写入 DB `logs` 表。
- Web 日志窗口（SSE 读 `logs` 表）因此只能看到那 3 行。
- 输出方式不统一：executor 里还有 `fmt.Errorf(...)` 只返回不落日志，以及直接调 `e.logger.Log(...)`。

### 4.2 方案（走 `ui` 统一，改动小、并发归属准确）

**`internal/ui/print.go` 扩展：**

- 新增 `Sink` 接口与全局 sink：

```go
type Sink interface {
    Log(executionID, jobName, level, message string) error
}

var sink Sink
func SetSink(s Sink) { sink = s }
```

- `ui.Printf` / `ui.Errorf` 照旧打终端（颜色不变），同时若当前 goroutine 已绑定任务，则转发给 `sink.Log(execID, jobName, "info"|"error", msg)`。sink 为 nil 时跳过，CLI/daemon 行为与今天完全一致。
- 新增按 goroutine 绑定：`ui.Bind(executionID, jobName string) (unbind func())`。内部用 goroutine ID（从 `runtime.Stack` 解析）做 key，`sync.Mutex` 保护的一个小注册表；`unbind()` 删除该 key。这样并发执行的多个任务日志各归各，不会串到别人的 execution 记录。
- `logger.Logger` 已天然实现 `Sink` 接口（`Log(executionID, jobName, level, message)`），无需改动。

**executor 接入：**

- `NewExecutor` 构造时调用 `ui.SetSink(logger)`（server 传入的是 DB logger；测试里传 nil logger 也没问题，sink 会被设为 nil 或空实现）。
- `executeAsync` 开头 `unbind := ui.Bind(jobID, job.Name)`，`defer unbind()`。
- 把原来直接调 `e.logger.Log(...)` 的几行改成 `ui.Printf` / `ui.Errorf`（"Starting job execution" 移入 goroutine 开头再打，保证已绑定）。避免双写。
- executor 里的 `fmt.Errorf(...)`（如 "failed to create execution record"）在返回错误前同时 `ui.Errorf` 打一份：job goroutine 内的进 DB + 终端，job 外的（`ExecuteJob` 同步段）进终端 + 作为错误返回给接口调用方。

### 4.3 效果

- Web 日志窗口会显示类似：`Starting job execution` → `local branch main has been checked out.` → `Downloading v1.0.0 asset xxx` → `File .../repo.tar.gz stored` → `Job completed successfully` → `Job finished (status: completed)`。
- daemon / CLI 进程：不 `SetSink`、不 `Bind`，输出行为零变化。

> **已考虑、未采用的备选**：把 logger 参数穿透 `repository/release/issue/wiki/discussion` 所有函数签名并逐个替换内部 `ui` 调用（约 80 处）。正确性相同，改动面大，收益相同，不采用。

## 5. 前端：Repositories 页

`web/static/js/main.js` `renderRepositories` 重写。

- **state**：新增 `state.reposPage = 1`、`state.reposSearch = ''`。
- **数据源**：`GET /api/repositories?page=&limit=20&search=` → `{repositories, total, page, limit}`。
- **页头/工具栏**：标题 + 搜索输入框（`#repos-search`，placeholder 如 "Filter by repository name…"）+ **Add Repository** + **Refresh** 按钮。搜索输入防抖 300ms（或回车）触发，变化时页码重置为 1。
- **表格列**：`Name | URL | Type | Cron | Next Run | Last Run | Stats | Storage | Options | Actions`。
  - Next Run / Last Run：`fmtTime(...)`，null 显示 `-`。
  - Stats：紧凑格式 `12 total · 9 ok · 2 fail`。
  - Options：现有 `cache / allBranches / releases / ...` 保持。
  - Actions：新增 **Execute** 按钮（Edit / Delete 之前）。
- **Execute**：点击 → 禁用该行按钮 → `POST /api/jobs` `{repository: name}` → 成功后 `openLogModal(data.job_id, name)`（自动打开实时日志，用户已确认）→ 重新拉取列表。失败 toast 错误。
- **分页**：复用 Jobs 页 `.pagination` 控件模式（`#pg-prev` / 页码信息 / `#pg-next`），每页 20，服务端分页。
- **Refresh**：重新拉取当前页（不自动轮询）。
- **空状态**：无仓库时显示 "No repositories configured. Click **Add Repository**."
- 日志窗口 `done` 事件后刷新逻辑：原代码在 `openLogModal` 的 `done` 里写死调用 `renderJobs()`。改为按当前路由刷新（调用 `renderApp()`），这样从 Repositories 页打开日志、任务结束时不会把用户切到 Jobs 页。

## 6. 前端：Jobs 页

`web/static/js/main.js` `renderJobs` / `jobsToolbar` 重写。

- **去掉自动刷新**：删除 main.js 末尾 5 秒 `setInterval`（约 548-551 行）。只保留手动 **Refresh** 按钮（现有按钮，行为不变）。打开日志窗口时 `done` 事件触发的列表刷新保留（事件驱动，非轮询）。
- **去掉执行功能**：删除 `jobsToolbar` 里的 "Run job" 标签、`#run-repo` 下拉框、`#btn-run` 按钮，及 `runJob()` 函数。执行功能已迁移到 Repositories 页。
- **换输入框过滤**：工具栏左侧放输入框 `#jobs-repo-filter`（placeholder "Filter by repository name…"）。
  - 输入作为 `repository` 参数传给 `GET /api/jobs`（后端按仓库名模糊匹配，见 3.1）。
  - 触发方式：防抖 300ms 自动查询，或回车立即查询；输入变化时页码重置为 1。
  - 空输入 = 不带 `repository` 参数 = 查全部。
- **保留**：状态筛选下拉、分页、Logs / Cancel 按钮、日志 SSE 弹窗。
- **不再需要**：`renderJobs` 里对 `/api/repositories` 的拉取（下拉框已删）。
- **空状态文案**：改为 "No jobs yet. Run a repository from the **Repositories** page, or click **Refresh**."
- **state**：新增 `state.jobsRepo = ''`（输入框当前值）。

## 7. docker-compose 挂载 `.gitrieve` 缓存目录

**位置**：`docker-compose.yml` volumes。

**背景**：Dockerfile `WORKDIR /app`，进程工作目录为 `/app`；`useCache: true` 时 git 缓存（`.git` 对象库 + 工作副本）落在 `/app/.gitrieve`。目前未挂载，容器重建后缓存丢失、需要重新 clone，违背"一旦拉取本地数据永不删除/可恢复"的归档目标。`.gitrieve` 已在 `.gitignore` 中，不会入库。

**改动**：在 `docker-compose.yml` 的 `volumes` 中追加：

```yaml
      # git clone cache directory — persists the local .git object store and
      # working copy across container rebuilds (required when useCache: true).
      # The in-container path resolves under the working directory /app.
      - ./.gitrieve:/app/.gitrieve
```

**文档**：在 README / docker-compose 注释中说明该目录用途。

## 8. 测试

- `internal/server/repository_test.go`：
  - `TestGetRepositories` 改为断言新分页结构；新增 `search` 过滤、分页、统计（预置 executions 数据）用例。
  - `TestCreateRepository` / `TestDeleteRepository` 里用 GET 验证的断言改为读 `data.repositories`。
- `internal/server/api_test.go` `TestGetJobs`：
  - 新增 NULL `end_time`（running 任务）回归用例 → 200 且 `end_time` 为 null。
  - 新增 `?repository=` 模糊匹配用例（部分匹配命中、精确不命中）。
- 新增 `nextRunTime` 单元测试：标准 5 段 cron、descriptor（如 `@daily`）、空/非法表达式 → nil。
- `internal/ui`：新增 `Bind`/`SetSink` 测试——绑定后 `Printf`/`Errorf` 转发到伪 sink、解绑后不再转发、并发两个 goroutine 绑定互不串扰。
- `internal/executor/executor_test.go`：验证任务执行期间日志按 execution 归属写入 `logs` 表。

## 9. 影响范围与不改动点

**改动文件（预计）**：
- `internal/server/api.go`、`internal/server/types.go`
- `internal/ui/print.go`
- `internal/executor/executor.go`
- `web/static/js/main.js`（可能少量 `web/static/css/main.css`）
- `docker-compose.yml`
- 相关 `_test.go`

**明确不改**：
- CLI / daemon 行为（不 `SetSink`、不 `Bind`，输出与今天一致）。
- 路由、`POST /api/jobs` 语义、`typedef.Repository` 字段。
- `repository/release/issue/wiki/discussion` 函数签名（避免 80 处穿透式改动）。
