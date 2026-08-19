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
| 主键 | 规范化后的 **URL**；URL 为空的仓库（`type: user/org`）回退用 `name` |
| name | 允许重复、允许改名，仅用于展示与查询 |
| URL 修改 | 允许修改；修改即身份变更，旧 URL 下的任务历史保持原样但不再被查询关联 |
| URL 重复 | 创建/更新时拒绝（规范化后相同） |
| 查询 | 任务过滤与仓库搜索均支持 name + URL 模糊匹配 |
| API 破坏性变更 | `POST /api/jobs` 请求字段由 name 改为仓库身份键 |

**范围**：DB schema 迁移、`internal/executor`、`internal/server` API、`web` 前端。`cmd/daemon` 仅日志使用 name，无身份逻辑，不动。`storage` 仍按 name 引用，不动。org/user 仓库在 `internal/repository.addRepo` 中展开为具体 URL 仓库的既有行为不变。

## 2. 现状与关键事实

| 位置 | 现状（以 name 为身份） |
|---|---|
| `internal/db/db.go:46` | `executions.job_name TEXT NOT NULL`，无 URL/键列 |
| `internal/db/models.go` | `Job.JobName` 字段 |
| `internal/executor/executor.go:57-104` | `ExecuteJob(jobName)` 按 `repo.Name` 查找；INSERT 只写 `job_name` |
| `internal/server/api.go:43` | `CreateJob` 校验按 `repo.Name == req.Repository` |
| `internal/server/api.go:121-138` | `GetJobs` 过滤按 `job_name LIKE` |
| `internal/server/api.go:186-191` | `GetJobs` 用 `repo.Name == job.Name` 从 config 解析 URL |
| `internal/server/api.go:334-341` | `GetRepositories` 统计 `GROUP BY job_name` |
| `internal/server/api.go:371` | 仓库搜索仅 name `Contains` |
| `internal/server/api.go:390` | 统计关联 `stats[repo.Name]` |
| `internal/server/api.go:462,544` | 仓库 `PUT/DELETE :id` 按 `existing.Name == id` 定位 |
| `web/static/js/main.js` | 操作按钮按 name 编码；`runRepo` 传 name；name 编辑禁用 |

一个既有事实：`type: user/org` 的仓库在 config 里没有 `url`（只有 `orgName`），其身份必须回退。另外 `GetRepositories` 的聚合 SQL 用了 SQLite 的技巧写法（`GROUP BY xxx HAVING start_time = MAX(start_time)` 取每组最后一条），换分组键时保留该写法与 `DATETIME` 列的注释约束（modernc/sqlite 驱动只有声明为 DATETIME 的列才能 scan 成 `time.Time`）。

## 3. 方案选择

**选 A：引入「仓库身份键」（`RepoKey`），贯穿 DB、executor、API、前端。**

- 在 `typedef` 增加 `Repository.Key()` 与 URL 规范化函数，作为唯一判等口径。
- DB `executions` 新增 `repo_key` 列（身份键），保留 `job_name`（执行时快照的展示名）。
- executor、API 全部改为按 `RepoKey` 查找/关联/聚合。
- 前端用同名 `repoKey(r)` 生成操作键，匹配后端。

不选：

- **方案 B（前端只改传参、后端只改 CRUD id，不引入 key 列）**：不引入 `repo_key` 列就无法在 `executions` 上可靠聚合（空 URL 仓库会塌缩到同一组），name 与 URL 的关系无法在历史中稳定表达。否决。
- **方案 C（复用 name 列存 URL，另加 name 列）**：语义混乱，历史数据迁移更复杂。否决。

## 4. 身份键与 URL 规范化（`internal/typedef`）

新增（放在 `repository.go` 或新文件 `key.go`）：

```go
// NormalizeURL 归一化仓库 URL：小写、去 http(s):// 前缀、去尾斜杠、去 .git 后缀。
// 返回 "" 表示没有可用 URL。
func NormalizeURL(url string) string

// Key 返回仓库身份键：URL 非空用 "url:"+NormalizeURL(URL)，否则 "name:"+Name。
func (r Repository) Key() string

// Matches 判断一段「用户输入的身份键」是否命中该仓库。输入有两种形态：
//   - "name:xxx"（空 URL 仓库的兜底键）→ 与 Name 精确比较；
//   - 其余 → 视为仓库 URL，规范化后与 Key() 比较（处理 url: 前缀、协议、尾斜杠、.git）。
func (r Repository) Matches(input string) bool
```

**匹配口径统一用 `repo.Matches(input)`**：前端传原始 URL 或 `name:<name>`，后端统一走 Matches，避免「前端 URL 形态」与「后端 key 形态」对不上的坑。

规范化规则（表驱动可测）：

| 输入 | 规范化结果 |
|---|---|
| `github.com/wnarutou/gitrieve` | `github.com/wnarutou/gitrieve` |
| `https://GITHUB.com/wnarutou/gitrieve/` | `github.com/wnarutou/gitrieve` |
| `http://github.com/wnarutou/gitrieve.git` | `github.com/wnarutou/gitrieve` |
| `git@github.com:wnarutou/gitrieve.git` | `github.com/wnarutou/gitrieve`（可选，先支持 `https/http/裸 host`，`git@` 形式另行说明） |

`git@host:path` 的 SCP 风格先**不支持**（示例配置全部用 `host/owner/repo` 形式）；若后续需要再扩展。规范化逻辑与前端 JS 版本各自独立实现，但**身份判定以后端为准**（API 路径参数在后端也做规范化后再匹配）。

## 5. DB 层（`internal/db`）

### schema

`executions` 增加列：

```sql
repo_key TEXT NOT NULL DEFAULT ''
```

新库在 `CREATE TABLE IF NOT EXISTS executions` 中直接带上该列。

### 迁移与回填

新增 `db.Migrate(db *DB, repos []typedef.Repository) error`，在 server 启动（拿到 config 后）调用一次：

1. `PRAGMA table_info(executions)` 检测 `repo_key` 列是否存在；不存在则 `ALTER TABLE executions ADD COLUMN repo_key TEXT NOT NULL DEFAULT ''`。
2. 回填：对每个配置仓库，`UPDATE executions SET repo_key = ? WHERE job_name = ? AND (repo_key = '' OR repo_key IS NULL)`（用 `repo.Key()` 回填——旧数据按 `job_name` 尽力匹配，匹配不到的置为 `"name:"+job_name` 自我归档，保证各自成组、不互相污染）。
3. 幂等：列已存在时跳过 ALTER；回填语句本身幂等。

依赖方向：`db` 导入 `typedef`（`typedef` 是叶子包，无循环依赖）。

## 6. Executor（`internal/executor`）

`ExecuteJob(repoKey string)`：按 `repo.Key() == repoKey` 查找仓库；INSERT 改为：

```sql
INSERT INTO executions (id, job_name, repo_key, start_time, status)
VALUES (?, ?, ?, ?, ?)
```

`job_name` 写执行时的 `repo.Name`（显示快照），`repo_key` 写 `repo.Key()`。其余（runningJobs 按 jobID、状态更新、取消）不变。

## 7. API（`internal/server`）

### 7.1 `CreateJob`

请求字段改为仓库身份键：

```go
type CreateJobRequest struct {
    RepositoryKey string `json:"repository_key" binding:"required"`
}
```

按 `repo.Matches(req.RepositoryKey)` 查找；找不到 404。空 URL 仓库也能用 `name:` 键触发（执行内部仍会失败——既有局限，不在本子项目修复）。

### 7.2 `GetJobs`

- SELECT 增加 `repo_key`；过滤参数 `repository` 改为对 `(job_name LIKE ? ESCAPE '\' OR repo_key LIKE ? ESCAPE '\')` 匹配。**第二个 LIKE 的搜索词先做 URL 规范化**（`NormalizeURL(q)`）再包 `%`：这样用户输入 `https://github.com/wnarutou/gitrieve/`（带协议/尾斜杠）也能命中规范化后的 `repo_key`（`url:github.com/wnarutou/gitrieve`）。
- `Job.url` 仍从 config 解析，但关联改按 `repo.Matches(job.RepoKey)` 匹配；关联不到（仓库已删/已改名）则 URL 为空、显示 `job_name` 快照。
- `Job` 响应模型不变（`name`/`url` 都在）。

### 7.3 `GetRepositories`

- 统计 SQL 改为 `GROUP BY repo_key`，保留 `HAVING start_time = MAX(start_time)` 取末次运行的写法与注释。
- 统计按 `stats[repo.Key()]` 关联。
- 搜索过滤改为 `name` 或 `url` 任一 `Contains`（大小写折叠）。

### 7.4 仓库 CRUD

- `PUT/DELETE /api/repositories/:id` 的 `:id` 语义变为**身份键**（URL 编码传输）。后端对 `:id` 用 `repo.Matches(id)` 定位（URL 或 `name:` 两种形态都处理）：
  - URL 仓库：id 传 URL（`encodeURIComponent`）；
  - 空 URL 仓库：id 传 `name:<name>`。
- `CreateRepository`：
  - **允许 name 重复**（删除既有按 name 的 409 检查）；
  - **URL 判重**：规范化后（`NormalizeURL`）与现有仓库相同的 URL → 409；空 URL 仓库按 name 判重 → 409（等价于旧行为）。
- `UpdateRepository`：name、URL 都可改（前端表单不再禁用 name）。URL 被改 → 该条目身份键变化，旧 `repo_key` 下的任务历史保持原样但不再被新键关联（孤立，符合决策）。更新后若与**其他**仓库的规范化 URL 冲突 → 409（更新对象自身不与自己比较）。

### 7.5 破坏性变更清单

- `POST /api/jobs` 字段 `repository` → `repository_key`。
- 仓库 `PUT/DELETE` 路径参数由 name 改为身份键。

## 8. 前端（`web/static/js/main.js`）

- 新增 JS 函数 `repoKey(r)`：`r.URL` 非空返回 `r.URL`，否则 `'name:' + r.Name`（规范化交给后端，前端只负责 URL vs name 分支与编码）。
- 仓库表操作（Execute / Edit / Delete）的 `data-*` 与 URL 路径改用 `encodeURIComponent(repoKey(r))`。
- `runRepo` 请求体改 `{ repository_key: repoKey(repo) }`。
- 编辑表单移除 `$('#repo-name').disabled = !!repo`（name 现在可改）。
- 任务页过滤输入占位文案改为 "Filter by repository name or URL…"；仓库页搜索占位改为 "Filter by name or URL…"。
- 展示不变：表格仍 Name 加粗、URL 灰色。

## 9. 失败语义与边界

- **迁移失败**：`Migrate` 出错则 server 启动失败退出（与 db 初始化失败同处理，`ui.ErrorfExit`）——宁可起不来也不要在坏 schema 上跑。
- **孤立历史**：URL 改名/删除仓库后，旧 `repo_key` 的 executions 仍在库中，但 `GetRepositories` 不关联、`GetJobs` 仍能按 job_name/旧键搜到（作为纯历史展示）。这是刻意为之，不清理。
- **明确不做**：
  - `cmd/daemon` 行为改动（无身份逻辑，仅日志）。
  - org/user 仓库在 executor 下运行时 URL 为空的既有失败问题。
  - 前端 JS 的完整 URL 规范化（以后端为准）。
  - 配置导入导出（子项目 B，另行 spec）。

## 10. 测试

新增：

- `internal/typedef`：`NormalizeURL` / `Key()` / `Matches` 表驱动测试（协议、大小写、尾斜杠、`.git`、空 URL、`name:` 回退、`name:` 输入精确匹配、URL 输入规范化匹配）。
- `internal/db`：`Migrate` 测试——构造旧 schema（无 `repo_key` 列）的库，运行后列存在且回填正确；新 schema 库幂等；`job_name` 匹配不到时置为 `name:<job_name>`。
- `internal/executor`：按 `Key()` 查找、INSERT 写入 `repo_key`、找不到返回错误。
- `internal/server`：
  - 按 URL 创建/更新/删除仓库；name 允许重复；URL 重复 409；更新 URL 冲突 409；
  - `CreateJob` 用 `repository_key`；`GetJobs` name/URL 模糊过滤；
  - `GetRepositories` 按 `repo_key` 聚合、搜索 name 或 URL。
- 既有 server 测试（`repository_test.go` 等）中按 name 作为路径 id 的用例需同步改为身份键。

前端无 JS 测试基建，UI 改动靠后端 API 测试 + 手动/浏览器验证。
