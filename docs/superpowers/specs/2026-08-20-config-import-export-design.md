# 配置导入导出（子项目 B）设计文档

**日期**：2026-08-20
**状态**：已与用户逐节确认

## 背景与目标

gitrieve 的配置（`config.yaml`）包含 `repository:`、`storage:`、全局设置（`githubToken`、`cocurrencyNum`、`releaseSizeLimit`、`releaseNumLimit`、`retryMaxCount`、`retryBaseDelay`）与 `server:` 段。当前只能通过 UI 逐条增删改，无法整体备份、迁移或批量导入。

**目标**：在 Web UI 与 API 上提供配置的**导出、导入（带差异对比与逐项选择）、从磁盘重载（刷配置）**能力。

本子项目 B 建立在已合并的子项目 A（URL 为主键）之上：
- 仓库以规范化 URL（`repo.Key()`）为主键，`name` 为可重复的展示/查询标签。
- 导入校验复用子项目 A 的约束：**空 URL 即非法**；org/user 条目的合成 URL 在导入时直接写入条目。

## 全局约束

- **导出格式**：仅 YAML，与 `config.yaml` 同构，导出文件可直接当配置文件使用。不做 JSON。
- **导出范围**：完整配置——`repository` + `storage` + 全局项 + `server:` 段；`githubToken` 与 S3 `secretAccessKey` **明文**导出。
- **密钥打码**：差异预览（preview）中，`githubToken`、`secretAccessKey`、`authToken` 的 existing/imported 显示为 `***`（非空时）。
- **原子导入**：导入先整体校验，任何错误则一个都不应用（返回全部错误列表）。
- **不热应用**：`server:` 段（host/port/authEnabled/authToken/dbPath）变更不热生效，需重启 server；导入时持久化到文件，下次启动生效。daemon 排程需重启 daemon。
- **删除安全**：仅当用户在导入应用时显式选择删除某条目才删除；默认保留。不触碰 `internal/repository/repository.go` 的 `Sync` 删除安全不变式。
- **retryBaseDelay**：以字符串导出（如 `5s`），导入用 `time.ParseDuration` 解析；禁止直接序列化 `time.Duration` 为纳秒整数。

## API 设计

### GET /api/config/export

导出当前配置为完整 YAML。

- 响应（标准信封）：`{ "code": 200, "data": { "yaml": "<完整配置 YAML>" }, "message": "" }`
- YAML 内容 = 内存配置（`config.GetIns()`，即 UI 所见）的 `repository` + `storage` + 全局项，加从 viper 读取的 `server:` 段，按 config.yaml 惯例顺序排布。
- 500：序列化失败。

### POST /api/config/import/preview

导入预览：解析 + 校验 + 计算差异，**不应用任何修改**。

- 请求：`{ "config": "<YAML 字符串>" }`（无 mode 字段——统一单流程）
- 校验失败 → 400，返回全部错误列表，如：
  ```json
  { "code": 400, "message": "导入配置无效", "data": { "errors": [
      "repository '<name>': URL 不能为空",
      "repository '<name>': URL 重复 'github.com/a/b'",
      "storage '<name>': 名称重复" ] } }
  ```
  - YAML 解析失败 → `"无效的 YAML: <err>"`。
  - 任一仓库 `EffectiveURL()` 为空 → 错误。
  - 导入内两个仓库 `Key()`（规范化 URL）相同 → 错误。
  - 导入内 storage 名称重复 → 错误。
  - （不校验现有配置与导入之间的 URL 重复——那是"修改"而非"重复"。）
- 成功 → 200，返回分类差异（见下）。

**预览响应 data**：

```json
{
  "summary": {
    "repositories": { "added": 2, "deleted": 1, "modified": 3 },
    "storages":     { "added": 0, "deleted": 1, "modified": 1 },
    "globals":      { "changed": 1 },
    "server":       { "changed": 2 }
  },
  "repositories": {
    "added":    [ { "key": "github.com/a/b", "name": "b", "url": "github.com/a/b" } ],
    "deleted":  [ { "key": "github.com/c/d", "name": "d", "url": "github.com/c/d" } ],
    "modified": [ { "key": "github.com/a/b", "name": "b", "url": "github.com/a/b",
                    "changes": [ { "field": "cron", "existing": "0 2 * * *", "imported": "0 6 * * *" },
                                 { "field": "allBranches", "existing": false, "imported": true } ] } ]
  },
  "storages": {
    "added":    [ { "name": "newStore", "type": "file", "path": "/x" } ],
    "deleted":  [ { "name": "oldStore" } ],
    "modified": [ { "name": "localFile", "changes": [ { "field": "path", "existing": "./repo", "imported": "/app/repo" } ] } ]
  },
  "globals": [ { "field": "githubToken", "existing": "***", "imported": "***" } ],
  "server":  [ { "field": "authEnabled", "existing": false, "imported": true },
               { "field": "authToken", "existing": "***", "imported": "***" } ],
  "warnings": [
    "server.authEnabled / server.authToken 已变更：该改动不会热生效，需重启 server；若忘记令牌，重启后可能无法访问 Web UI。"
  ]
}
```

- 分类语义：
  - `repositories.added`：导入中有、现有配置无（按 `Key()`）。
  - `repositories.deleted`：现有配置有、导入中无。**仅提示**——是否真正删除由用户在应用时决定。
  - `repositories.modified`：两边都有但字段不同；`changes` 逐字段（对比字段：Name、URL、Cron、Storage、UseCache、Type、OrgName、AllBranches、Depth、DownloadReleases、DownloadIssues、DownloadWiki、DownloadDiscussion；Storage 按集合比较）。
  - 未变化条目不进入任何数组。
  - `globals`/`server`：仅列变更字段（比较 githubToken、cocurrencyNum、releaseSizeLimit、releaseNumLimit、retryMaxCount、retryBaseDelay；server: host、port、authEnabled、authToken、dbPath）。
- `warnings`：服务端生成。当 server 段有变更时至少一条；authEnabled/authToken 变更时必须有"可能启动不起来/锁在门外"提示。

### POST /api/config/import

应用导入。

- 请求：
  ```json
  {
    "config": "<YAML>",
    "choices": {
      "repository_deletions": [ "github.com/c/d" ],
      "repository_choices": { "github.com/a/b": "imported" },
      "storage_deletions": [ "oldStore" ],
      "storage_choices": { "localFile": "existing" },
      "global_choices": { "githubToken": "existing" },
      "server_choices": { "authEnabled": "existing", "authToken": "existing" }
    }
  }
  ```
- 缺省（省略 `choices` 或其中任一键）：新增→导入、修改→采用导入、删除→不删、全局/server→采用导入。
- 选择值：
  - `repository_choices["<key>"]`：`"imported"`（用导入值更新该仓库）| `"existing"`（保留现有，不动）。**只对预览 `modified` 类里的仓库生效**；对 `added`/`deleted`/未变化 key 的值一律忽略。
  - `repository_deletions`：数组，含哪些 key 就删除哪些**现有**仓库（key 必须属于预览的 `deleted` 类；不在 `deleted` 类里的 key 忽略）。
  - `storage_*` 同 repository_*（按 name）。
  - `global_choices["<field>"]` / `server_choices["<field>"]`：`"imported"` | `"existing"`。
- 流程：重新解析 + 校验（同 preview，保证原子）→ 按 choices 应用内存 → `config.Save()`。
  - 校验失败 → 400 + 错误列表，不应用。
  - `config.Save()` 失败 → HTTP 200，`message`："已应用内存但未能持久化：<err>"（与现有 CRUD 一致）。
- 响应 `data`：
  ```json
  { "repositories_added": 1, "repositories_updated": 1, "repositories_deleted": 1,
    "storages_added": 0, "storages_updated": 1, "storages_deleted": 1,
    "globals_updated": 0, "server_updated": 0 }
  ```

### POST /api/config/reload

从磁盘重载配置（刷配置）。

- 行为：调用 `config.Reload()`（见下）；成功后把新配置指针换到 API（`a.config`）与 executor（`e.cfg`）。
- 成功 → 200 `{ "code": 200, "data": null, "message": "" }`。
- 失败（文件缺失/解析错误）→ 400 `{ "code": 400, "message": "重载配置失败: <err>" }`；**内存保持旧配置不变**。
- 边界（文档与 UI 说明）：`server:` 段不热应用，需重启 server；daemon 排程需重启 daemon。

## config 包改动

- `Reload() error`：非致命重读 config.yaml（`ReadInConfig` + `Unmarshal` + 种子默认值，逻辑与 `Init` 相同但**错误不 `ui.ErrorfExit`**，而是保留旧 `ins` 并返回 error）。
- `Export() (string, error)`：组装完整 YAML（repository/storage/全局项来自 `ins`，server 段来自 viper；`retryBaseDelay` 用字符串形式）。
- 导入解析：`yaml.Unmarshal` 到配置形状；`retryBaseDelay` 接受两种形式并固化在测试中：字符串（如 `"5s"`，`time.ParseDuration`）与整数（按纳秒）。
- 并发注意：导入/重载修改的是与现有 CRUD 相同的内存 `ins` 切片，不加额外锁，与现状一致。

## server/executor 改动

- API 新增 4 个 handler：`ExportConfig`、`PreviewImport`、`ApplyImport`、`ReloadConfig`；路由：
  - `GET  /api/config/export`
  - `POST /api/config/import/preview`
  - `POST /api/config/import`
  - `POST /api/config/reload`
- executor 增加极小 setter（如 `RefreshConfig(cfg)`）供 reload 换指针；不影响并发（`ExecuteJob` 的查找是短临界区）。
- 导出 YAML 的 server 段结构体：`{ host, port, authEnabled, authToken, dbPath }`。

## UI 设计（Config 页签）

顶部导航新增 **Config** 链接（路由 `#/config`）。复用现有 `.panel / .btn / .table / .form / .badge` 样式，不引入新 CSS 框架。

### ① 导出面板
- 只读 textarea 展示 `GET /api/config/export` 的 YAML。
- 按钮：**复制**（clipboard）、**下载 config.yaml**（Blob）、**刷新**（重新拉取）。

### ② 导入面板
- 输入：可粘贴 YAML 的 textarea + **选择文件**（文件内容填入 textarea）。
- **预览差异** 按钮。
- 预览结果（摘要 + 可展开分类）：
  - 顶部摘要块：**仓库 新增 N / 删除 M / 修改 K**、**存储 新增 N / 删除 M / 修改 K**、**全局项 变更 N**、**server 段 变更 N**。每类可点击展开/收起；计数为 0 的类不显示。
  - **新增** 列表：条目（name + url），默认加入，无需选择。
  - **修改** 列表：字段级差异（`cron: 0 2 * * * → 0 6 * * *`）+ 每行「采用导入 / 保留现有」单选（默认采用导入）+ 批量按钮 **「全部采用导入」**、**「全部保留」**。
  - **删除** 列表：每行「删除 / 保留」单选（默认保留）+ 批量按钮 **「全部删除」**；标注"导入配置中不存在的现有条目"。
  - **全局项 / server 段**：仅变更字段，逐字段「采用导入 / 保留现有」单选（默认采用导入）。
  - **警告横幅**：渲染 `warnings`（琥珀/红色），如「⚠️ authEnabled / authToken 已变更：不热生效，重启后若忘记令牌将无法访问 Web UI」。
  - **应用导入** 按钮（预览成功后启用）。
- 应用后：toast 汇总（新增/更新/删除数量）+ 刷新 Repositories / Storage / Config 视图。

### ③ 配置操作面板
- **「刷新配置（从磁盘重载）」** 按钮 → `POST /api/config/reload`，成功/失败均 toast 并刷新视图。
- 说明文字：「server 段（host/port/auth/dbPath）改动需重启 server 生效；daemon 排程需重启 daemon。」

## 测试要点

- **server（api_test / 新 config_test）**：
  - export 返回合法 YAML，且 round-trip：export → import → 配置一致。
  - export 含 server 段；`retryBaseDelay` 为 `"5s"` 字符串。
  - preview：added/deleted/modified 分类正确；`changes` 字段级正确；未变化条目不出现。
  - preview 校验错误：无效 YAML / 空 URL / 导入内 URL 重复 / storage 重名 → 400 + 全量错误列表，无副作用。
  - apply：默认全部采用导入；`repository_choices` 尊重 imported/existing；`repository_deletions` 删除指定；`global_choices`/`server_choices` 生效；删除列表外的 key 忽略。
  - apply 校验失败 → 不应用（原子）。
  - reload：换指针生效（后续 GET /api/repositories 反映新配置）；Reload 出错 → 400 且内存保持旧配置。
- **config（config_test.go）**：`Reload()` 非致命；retryBaseDelay 字符串 round-trip。

## 非目标（本次不做）

- 不做 JSON 导入/导出。
- 不做 CLI 命令（`gitrieve config export/import`）——本次仅 UI + API。
- 不实现 daemon 运行时重排程（重载后 daemon 需重启）。
- 不校验 cron 表达式合法性（与现有加载行为一致）。
- 不热应用 server 段。
