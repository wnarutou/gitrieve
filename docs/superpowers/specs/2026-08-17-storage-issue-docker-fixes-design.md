# Storage、Issue 与 Docker 时区修复设计

日期：2026-08-17

## 1. 背景与目标

本次修复针对本地 Docker 端到端测试确认的三个问题：

1. `MultiStorage` 保存为 YAML 时，嵌入的 `Storage` 被写成嵌套的 `storage:` 对象，重新加载后 `name`、`type`、`path` 为空。
2. Issue 首次同步发送 `since=1970-01-01T00:00:00Z`，GitHub 返回空结果；增量查询还会把本地墙上时间重新解释为 UTC，存在时区偏移风险。
3. 当前 Docker 配置中部分仓库引用 `local`，实际 Storage 名为 `localFile`；运行镜像也未安装 `tzdata`，无法可靠兑现 Compose 中的 `TZ=Asia/Shanghai` 配置。

目标是让未来保存的 Storage YAML 保持扁平格式、让 Issue 首次同步可靠拉取全部可见记录并保持按更新时间升序处理，以及让当前 Docker 配置和容器时区可用。

## 2. 已确认的产品决策

| 决策点 | 结论 |
|---|---|
| 错误 Storage YAML 的向后兼容 | 不做；目前尚无外部用户，只保证以后不再生成错误格式 |
| Issue 首次同步 | 不发送 `since`，由 GitHub 返回全部有权限访问的记录 |
| Issue 状态 | 保持 `state=all`，包含 Open 与 Closed |
| Pull Request | 保持现状；GitHub Issues API 返回的 Pull Request 继续归档为 PullRequest Markdown |
| Issue 排序 | 保持 `sort=updated&direction=asc`，按最后更新时间从早到晚处理 |
| Wiki 失败语义 | 不修改；元数据组件继续采用 best-effort，Wiki 失败不改变整体退出状态 |

## 3. 方案选择

采用最小、定向修复：

- 给 `MultiStorage.Storage` 增加 `yaml:",inline"`，保留已有 `mapstructure:",squash"`，分别保证 YAML 保存和 Viper 加载使用扁平字段。
- 将 Issue 查询参数创建收敛到一个小型 helper。零值时间代表首次同步，`go-github` 的 `omitempty` 会省略 `since`；非零时间直接调用 `UTC()`，保留同一时刻而不是重建年月日时分秒。
- 在两个发布用 Dockerfile 的 Alpine 运行阶段安装 `tzdata`。
- 一次性把当前 Docker 配置改回扁平 Storage，并统一仓库引用为 `localFile`。

未选择的方案：

- **兼容并迁移旧嵌套 YAML**：需要额外的兼容结构或迁移逻辑；用户已确认当前没有外部用户，不值得增加长期维护面。
- **重写 Config 的自定义 YAML 编解码层**：可以完全控制格式，但对本次单个嵌入字段问题改动过大。

## 4. Storage 数据流

正确格式保持为：

```yaml
storage:
  - name: localFile
    type: file
    path: /app/repo
```

`config.Init()` 继续由 Viper 使用 `mapstructure:",squash"` 解码到嵌入字段；`config.Save()` 把 `ins.Storage` 交给 YAML 编码时，通过 `yaml:",inline"` 将同一组字段写到列表项顶层。保存后重新调用 `Init()`，字段值必须保持不变。

当前 `gitrieve-docker/config/config.yaml` 会一次性展平，并把所有 `repository[].storage` 中的 `local` 改为 `localFile`。Token 和其他配置保持不变。

## 5. Issue 同步数据流

首次同步时，本地 Issue 目录没有 Markdown 文件：

1. `lastUpdate` 保持 `time.Time{}`。
2. 查询参数使用 `state=all`、`sort=updated`、`direction=asc`、`per_page=100`。
3. `Since` 保持零值，因此实际请求不包含 `since`。
4. 按 GitHub 分页顺序从较早更新时间处理到较晚更新时间，为每条 Issue 或 Pull Request 写入独立 Markdown。

增量同步时：

1. 从现有 Markdown 中读取最大的 `Updated Time`。
2. 将该时间通过 `lastUpdate.UTC()` 放入 `Since`，不改变其代表的瞬间。
3. 保持更新时间升序和现有分页、评论下载、覆盖同编号 Markdown、重新归档行为。

文件名继续使用 `#<number>.md`；目录或压缩包展示顺序不承诺按时间排序，只有 API 拉取和处理顺序按 `updated` 升序。

## 6. Docker 时区

`Dockerfile` 与 `Dockerfile.goreleaser` 的运行镜像均安装：

```dockerfile
RUN apk add --no-cache ca-certificates git tzdata
```

Compose 保持 `TZ=Asia/Shanghai`。容器验证需要确认 `/usr/share/zoneinfo/Asia/Shanghai` 存在，并确认应用的 cron 所依赖的本地时区可解析。

## 7. 测试与验收

自动化测试：

- Config 保存前为扁平 Storage，调用 `Save()` 后 YAML 不出现列表项内的嵌套 `storage:`，重新 `Init()` 后 `name/type/path` 完整保留。
- Issue 首次同步查询参数的 `Since` 为零值，编码后的请求不包含 `since`。
- Issue 增量同步把带非 UTC 时区的非零时间转换为同一瞬间的 UTC，不发生八小时时间平移。
- 完整执行 `go test ./...`。

Docker 验收：

- 从修改后的最新代码重新构建 `gitrieve-local:latest` 并重建 Compose 服务。
- 容器使用最新镜像，`/health` 返回 HTTP 200。
- `/api/storage` 返回 `localFile/file//app/repo`。
- 清空隔离测试输出后运行首次 Issue 同步，实际生成 Issue 归档；再次同步验证增量路径。
- 回归 Release、Discussion、Wiki 的既有成功路径。

## 8. 非目标

- 不兼容或自动迁移已经生成的嵌套 Storage YAML。
- 不改变元数据组件 best-effort 和 Wiki 失败返回 0 的现有设计。
- 不改变 Issue Markdown 文件名和归档路径。
- 不引入新的配置字段或依赖。
