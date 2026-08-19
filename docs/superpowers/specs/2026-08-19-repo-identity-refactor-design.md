# 仓库身份重构（URL 为主键）设计文档

日期：2026-08-19

## 1. 背景与目标

当前系统中，仓库（repository）的**身份**是 `name`：

- `executions.job_name` 是任务历史与仓库的唯一关联字段；
- `Executor.ExecuteJob(name)` 按 `repo.Name` 查找；
- `POST /api/jobs` 按 name 触发任务；
- `GET /api/repositories` 按 `job_name` 聚合统计、按 `repo.Name` 关联；
- 仓库 CRUD 的路径参数 `:id` 就是 name；
- 前端所有操作按钮按 name 编码到 URL，且 name 创建后不可修改。

这带来两个问题：

1. **同一个上游仓库可以以不同 name 配置多次**，同一 URL 被归档多份、任务历史无法按 URL 归并——这正是后续「配置导入导出」合并语义（按 URL 判重）的障碍。
2. name 被当身份用，导致 name 不允许重复、不允许改名，限制了用户自由组织本地命名。

**目标**：仓库身份改为 **URL 为主键**；`name` 降级为纯展示/查询标签，允许重复、允许改名；查询（任务过滤、仓库搜索）支持 name 与 URL 的模糊匹配。这是子项目 A，为子项目 B「配置导入导出」打基础。

已确认的需求决策：

| 决策点 | 结论 |
|---|---|
| 主键 | 规范化后的 **URL**；**空 URL 即非法**（创建/导入/启动校验都报错），无 `name:` 兜底 |
| name | 允许重复、允许改名，仅用于展示与查询 |
| 历史数据 | **不做兼容回填**——工具尚未正式使用、无存量数据，`executions` 只新增 `repo_key` 列，不回溯旧数据 |
| org/user 条目 | 保留；URL 为空时按 `orgName` **自动合成** `https://github.com/<orgName>` 作为身份 |
| 执行/记账粒度 | 运行 org/user 条目时**在任务内展开**成具体仓库，每个具体仓库一条 execution（repo_key = 具体仓库 URL）；org 条目统计 = 成员仓库的 **URL 前缀聚合** |
| URL 修改 | 允许修改；修改即身份变更，旧 URL 下的任务历史保持原样但不再被查询关联 |
| URL 重复 | 创建/更新时拒绝（规范化后相同） |
| 查询 | 任务过滤与仓库搜索均支持 name + URL 模糊匹配 |
| API 破坏性变更 | `POST /api/jobs` 请求字段改为仓库身份键；org 展开时响应返回多个 job_id |

**范围**：DB schema、`internal/executor`、`internal/server` API、`web` 前端。`cmd/daemon` 仅日志使用 name，无身份逻辑，不动。`storage` 仍按 name 引用，不动。

## 2. 现状与关键事实

| 位置 | 现状（以 name 为身份） |
|---|---|
| `internal/db/db.go:46` | `executions.job_name TEXT NOT NULL`，无 URL/键列 |
| `internal/db/models.go` | `Job.JobName` 字段 |
| `internal/executor/executor.go:57-104` | `ExecuteJob(jobName)` 按 `repo.Name` 查找；INSERT 只写 `job_name`；org/user 条目 URL 为空时 Sync 会失败（既有局限） |
| `internal/server/api.go:43` | `CreateJob` 校验按 `repo.Name == req.Repository` |
| `internal/server/api.go:121-138` | `GetJobs` 过滤按 `job_name LIKE` |
| `internal/server/api.go:186-191` | `GetJobs` 用 `repo.Name == job.Name` 从 config 解析 URL |
| `internal/server/api.go:334-341` | `GetRepositories` 统计 `GROUP BY job_name` |
| `internal/server/api.go:371` | 仓库搜索仅 name `Contains` |
| `internal/server/api.go:390` | 统计关联 `stats[repo.Name]` |
| `internal/server/api.go:462,544` | 仓库 `PUT/DELETE :id` 按 `existing.Name == id` 定位 |
| `internal/repository/repository.go:40-76` | `addRepo`（私有）把 org/user 展开成具体 URL 仓库；`GetRepositories("")` 遍历展开 |
| `web/static/js/main.js` | 操作按钮按 name 编码；`runRepo` 传 name；name 编辑禁用 |

一个既有事实：`GetRepositories` 的聚合 SQL 用了 SQLite 的技巧写法（`GROUP BY xxx HAVING start_time = MAX(start_time)`，在每组内取 COUNT/SUM 并返回末次运行的 start_time），换分组键时保留该写法与 `DATETIME` 列的注释约束（modernc/sqlite 驱动只有声明为 DATETIME 的列才能 scan 成 `time.Time`）。

## 3. 方案选择

**选 A：引入「仓库身份键」（`Key()` = 规范化 URL），贯穿 DB、executor、API、前端；org/user 在 executor 内展开、按具体仓库记账。**

- `typedef` 提供 `Repository.Key()` 与 URL 规范化，作为唯一判等口径。
- DB `executions` 新增 `repo_key` 列（规范化 URL），保留 `job_name`（执行时快照的展示名）。
- executor、API 全部按身份键查找/关联/聚合。
- org/user 条目身份用合成 URL；执行时经 `repository.Expand` 展开为具体仓库逐个归档。

不选：

- **方案 B（前端只改传参、后端只改 CRUD id，不引入 key 列）**：不引入 `repo_key` 列就无法在 `executions` 上可靠聚合；org/user 无 URL 时身份无法表达。否决。
- **方案 C（复用 name 列存 URL，另加 name 列）**：语义混乱。否决。
- **方案 D（org/user 按条目记账，一条 execution 记整个 org）**：历史无法区分具体归档了哪些仓库，与 daemon 按成员调度的粒度不一致。否决（用户已确认按具体仓库记账）。

## 4. 身份键与 URL 规范化（`internal/typedef`）

新增（放在 `repository.go` 或新文件 `key.go`）：

```go
// NormalizeURL 归一化仓库 URL：小写、去 http(s):// 前缀、去尾斜杠、去 .git 后缀。
// 返回 "" 表示没有可用 URL。
func NormalizeURL(url string) string

// EffectiveURL 返回条目的有效 URL：URL 非空直接用；type 为 user/org 且 orgName 非空时
// 合成 "https://github.com/<orgName>"；否则 ""（非法）。
func (r Repository) EffectiveURL() string

// Key 返回仓库身份键 = NormalizeURL(EffectiveURL())。空 URL 会得到 ""，调用方必须拒绝。
func (r Repository) Key() string

// Matches 判断一段用户输入是否命中该仓库：输入被规范化后与 Key() 比较。空输入返回 false。
func (r Repository) Matches(input string) bool
```

规范化规则（表驱动可测）：

| 输入 | 规范化结果 |
|---|---|
| `github.com/wnarutou/gitrieve` | `github.com/wnarutou/gitrieve` |
| `https://GITHUB.com/wnarutou/gitrieve/` | `github.com/wnarutou/gitrieve` |
| `http://github.com/wnarutou/gitrieve.git` | `github.com/wnarutou/gitrieve` |

`git@host:path` 的 SCP 风格先**不支持**（示例配置全部用 `host/owner/repo` 形式）；若后续需要再扩展。规范化逻辑与前端 JS 版本各自独立实现，但**身份判定以后端为准**（API 路径参数在后端也做规范化后再匹配）。

**空 URL 的约束**：创建/更新仓库、导入、启动配置校验时，凡 `EffectiveURL()` 为空（非 user/org，或无 orgName）即报错拒绝。org/user 条目的合成 URL 在**创建/导入时直接写入条目**（config 里存的是合成后的完整 URL），保证"每个已存条目都有非空 URL"字面成立。

## 5. DB 层（`internal/db`）

### schema

`executions` 增加列：

```sql
repo_key TEXT NOT NULL DEFAULT ''
```

新库在 `CREATE TABLE IF NOT EXISTS executions` 中直接带上该列。

### 迁移（无回填）

新增 `db.Migrate(db *DB) error`，在 server 启动时调用一次：

1. `PRAGMA table_info(executions)` 检测 `repo_key` 列是否存在；不存在则 `ALTER TABLE executions ADD COLUMN repo_key TEXT NOT NULL DEFAULT ''`。
2. **不做数据回填**（无存量数据）。旧开发库中既有行的 `repo_key` 保持 `''`，与任何配置条目都匹配不上，作为纯历史显示即可。

幂等：列已存在时跳过 ALTER。`db` 保持只依赖 `database/sql` 与 sqlite 驱动，不引入 `typedef`（无回填后不再需要仓库类型）。

## 6. Executor（`internal/executor`）

### 查找

`ExecuteJob(repoKey string)`：按 `repo.Matches(repoKey)` 在 config 中定位条目。

### 执行与展开

- **type = repo**：与现在一致，跑单个仓库，产生一条 execution，返回 `[]string{jobID}`。
- **type = user/org**：经 `repository.Expand(repo)`（把私有 `addRepo` 提为导出函数）展开成具体仓库列表，对每个具体仓库**独立执行**：各自生成 jobID、execution 行、日志流、可独立取消。返回 `[]string{jobID, ...}`。execution 的 `repo_key` 一律写**具体仓库**的规范化 URL，`job_name` 写具体仓库名。

INSERT 改为：

```sql
INSERT INTO executions (id, job_name, repo_key, start_time, status)
VALUES (?, ?, ?, ?, ?)
```

`runningJobs` 仍按 jobID 管理，取消/状态更新逻辑不变。

## 7. API（`internal/server`）

### 7.1 `CreateJob`

请求字段改为仓库身份键：

```go
type CreateJobRequest struct {
    RepositoryKey string `json:"repository_key" binding:"required"`
}
```

按 `repo.Matches(req.RepositoryKey)` 查找；找不到 404。响应改为返回**多个 job_id**：

```go
type CreateJobResponse struct {
    JobIDs []string `json:"job_ids"`
    Status string   `json:"status"`
}
```

（type=repo 时长度为 1，行为与现状等价。）

### 7.2 `GetJobs`

- SELECT 增加 `repo_key`；过滤参数 `repository` 改为对 `(job_name LIKE ? ESCAPE '\' OR repo_key LIKE ? ESCAPE '\')` 匹配。**第二个 LIKE 的搜索词先做 URL 规范化**（`NormalizeURL(q)`）再包 `%`：这样用户输入 `https://github.com/wnarutou/gitrieve/`（带协议/尾斜杠）也能命中规范化后的 `repo_key`。
- `Job.url` 仍从 config 解析，但关联改按 `repo.Matches(job.RepoKey)` 匹配；关联不到（仓库已删/已改名/旧数据空键）则 URL 为空、显示 `job_name` 快照。
- `Job` 响应模型不变（`name`/`url` 都在）。

### 7.3 `GetRepositories`

- 统计 SQL 改为 `GROUP BY repo_key`，保留 `HAVING start_time = MAX(start_time)` 写法与注释；每组一行含 `last_run/total/success/failed`。
- 关联：对每个配置条目，取其身份键对应的统计；**type 为 user/org 时**对「键前缀命中」的成员做**求和聚合**——`repo_key == entry.Key()` 或 `repo_key` 以 `entry.Key()` 为路径边界前缀（`HasPrefix` 且下一个字符是 `/` 或结尾，避免 `github.com/wnarutou` 误吞 `github.com/wnarutou2/x`）。`last_run` 取命中成员的 max。
- 搜索过滤改为 `name` 或 `url`（`EffectiveURL()`）任一 `Contains`（大小写折叠）。

### 7.4 仓库 CRUD

- `PUT/DELETE /api/repositories/:id` 的 `:id` 语义变为**身份键**（URL 编码传输），后端用 `repo.Matches(id)` 定位。
- `CreateRepository`：
  - **允许 name 重复**（删除既有按 name 的 409 检查）；
  - **URL 判重**：规范化后（`NormalizeURL(EffectiveURL())`）与现有仓库相同 → 409；
  - **空 URL 拒绝**：非 user/org 或 orgName 为空导致 `EffectiveURL()` 为空的 → 400；user/org 且 URL 为空 → 自动填入合成 URL。
- `UpdateRepository`：name、URL 都可改（前端表单不再禁用 name）。URL 被改 → 该条目身份键变化，旧 `repo_key` 下的任务历史保持原样但不再被新键关联（孤立，符合决策）。更新后若与**其他**仓库的规范化 URL 冲突 → 409（更新对象自身不与自己比较）。

### 7.5 破坏性变更清单

- `POST /api/jobs` 字段 `repository` → `repository_key`；响应 `job_id` → `job_ids`。
- 仓库 `PUT/DELETE` 路径参数由 name 改为身份键。

## 8. 前端（`web/static/js/main.js`）

- 新增 `repoKey(r)`：**就是 `r.URL`**（所有条目都有 URL，包括合成后的 org/user；前端不做规范化，交给后端）。操作按钮的 `data-*` 与 URL 路径改用 `encodeURIComponent(repoKey(r))`。
- `runRepo` 请求体改 `{ repository_key: repoKey(repo) }`；响应是 `job_ids` 数组：
  - 长度 1 → 保持现状：toast + 打开日志弹窗；
  - 长度 >1（org/user 展开）→ toast「Started N jobs」并刷新任务列表，不弹日志窗。
- 编辑表单移除 `$('#repo-name').disabled = !!repo`（name 现在可改）。
- 任务页过滤输入占位文案改为 "Filter by repository name or URL…"；仓库页搜索占位改为 "Filter by name or URL…"。
- 展示不变：表格仍 Name 加粗、URL 灰色。

## 9. 失败语义与边界

- **迁移失败**：`Migrate` 出错则 server 启动失败退出（与 db 初始化失败同处理，`ui.ErrorfExit`）——宁可起不来也不要在坏 schema 上跑。
- **空 URL 配置**：启动时配置校验发现 `EffectiveURL()` 为空的条目（非 user/org，或无 orgName）→ `ui.ErrorfExit` 报错退出。无历史数据，不做宽容处理。
- **孤立历史**：URL 改名/删除仓库后，旧 `repo_key` 的 executions 仍在库中，但 `GetRepositories` 不关联、`GetJobs` 仍能按 job_name/旧键搜到（纯历史展示）。刻意为之，不清理。
- **明确不做**：
  - `cmd/daemon` 行为改动（无身份逻辑，仅日志）。
  - org/user 展开后的每个成员仓库与显式条目并存时的重复归档问题（用户自行避免）。
  - 前端 JS 的完整 URL 规范化（以后端为准）。
  - 配置导入导出（子项目 B，另行 spec）。

## 10. 测试

新增：

- `internal/typedef`：`NormalizeURL` / `EffectiveURL` / `Key` / `Matches` 表驱动测试（协议、大小写、尾斜杠、`.git`、user/org 合成 URL、空输入/空 URL 拒绝）。
- `internal/db`：`Migrate` 测试——旧 schema（无 `repo_key` 列）补列成功；新 schema 幂等；不加回填断言（旧行键为空）。
- `internal/executor`：按 `Key()`/`Matches` 查找、INSERT 写入 `repo_key`、找不到返回错误；**type=repo 返回单 jobID，type=org/user 经展开返回多个 jobID 且各写具体仓库键**。
- `internal/repository`：`Expand` 对 repo/user/org 的展开结果（user/org 需要能 mock 掉 GitHub 客户端）。
- `internal/server`：
  - 按 URL 创建/更新/删除仓库；name 允许重复；URL 重复 409；更新 URL 冲突 409；空 URL 创建 400；
  - `CreateJob` 用 `repository_key`、响应 `job_ids`（repo 单值、org 多值）；
  - `GetJobs` name/URL 模糊过滤（含带协议的完整 URL）；
  - `GetRepositories` 按 `repo_key` 聚合、org 前缀求和、路径边界不误吞、搜索 name 或 URL。
- 既有 server 测试（`repository_test.go` 等）中按 name 作为路径 id 的用例需同步改为身份键。

前端无 JS 测试基建，UI 改动靠后端 API 测试 + 手动/浏览器验证。
