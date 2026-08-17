# Storage、Issue 与 Docker 时区修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 Storage 保存格式、Issue 首次及增量查询时间语义、Docker 时区支持，并修正当前本地 Docker 配置。

**Architecture:** 保持现有 Config、Issue Sync 和 Docker 构建结构，只在数据进入 YAML、GitHub 查询参数和 Alpine 运行镜像三个边界做最小修复。每个代码问题先添加独立回归测试并验证失败，再实施最小修改；最后修正部署配置并执行 Docker 端到端回归。

**Tech Stack:** Go 1.23、Viper、go-github v56、Testify、Alpine Docker、Docker Compose、PowerShell。

## Global Constraints

- 不兼容或自动迁移历史嵌套 Storage YAML；只保证未来保存格式正确。
- Issue 首次同步不发送 `since`；后续同步使用真实 UTC 时间。
- Issue 保持 `state=all`、`sort=updated`、`direction=asc`、`per_page=100`。
- 不改变 Issue/Pull Request Markdown 文件名、归档路径或 Wiki best-effort 语义。
- `Dockerfile` 与 `Dockerfile.goreleaser` 都必须安装 `tzdata`。
- 当前 Docker 配置统一使用 Storage 名称 `localFile`，不打印或改写 GitHub Token。
- 保持删除安全同步不变量，不修改 `internal/repository/repository.go`。

---

### Task 1: Storage YAML 保存格式

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/typedef/storage.go`

**Interfaces:**
- Consumes: `config.Save()`、`config.Init()`、`typedef.MultiStorage`。
- Produces: `MultiStorage.Storage` 的 `yaml:",inline" mapstructure:",squash"` 标签组合；保存后仍可由现有 Viper 加载路径恢复 `Name/Type/Path`。

- [ ] **Step 1: 添加失败的保存重载测试**

在 `internal/config/config_test.go` 添加：

```go
func TestSaveStorageRoundTripKeepsFlatFields(t *testing.T) {
    writeTmpConfig(t, `storage:
  - name: localFile
    type: file
    path: /app/repo
`)
    require.Len(t, GetIns().Storage, 1)
    require.Equal(t, "localFile", GetIns().Storage[0].Name)

    require.NoError(t, Save())
    saved, err := os.ReadFile(Path)
    require.NoError(t, err)
    require.NotContains(t, string(saved), "- storage:")

    Init()
    require.Len(t, GetIns().Storage, 1)
    require.Equal(t, "localFile", GetIns().Storage[0].Name)
    require.Equal(t, "file", GetIns().Storage[0].Type)
    require.Equal(t, "/app/repo", GetIns().Storage[0].Path)
}
```

- [ ] **Step 2: 运行测试并确认按预期失败**

Run:

```powershell
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test ./internal/config -run TestSaveStorageRoundTripKeepsFlatFields -count=1
```

Expected: FAIL；保存内容包含 `- storage:`，或重新加载后的 `Name/Type/Path` 为空。

- [ ] **Step 3: 添加最小 YAML inline 修复**

把 `internal/typedef/storage.go` 中的嵌入字段改为：

```go
type MultiStorage struct {
    Storage         `yaml:",inline" mapstructure:",squash"`
    Endpoint        string `yaml:"endpoint"`
    Bucket          string `yaml:"bucket"`
    Region          string `yaml:"region"`
    AccessKeyID     string `yaml:"accessKeyID"`
    SecretAccessKey string `yaml:"secretAccessKey"`
}
```

- [ ] **Step 4: 格式化并验证 Storage 测试通过**

Run:

```powershell
gofmt -w internal/typedef/storage.go internal/config/config_test.go
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test ./internal/config -run TestSaveStorageRoundTripKeepsFlatFields -count=1
go test ./internal/config -count=1
```

Expected: 两个命令均 PASS。

- [ ] **Step 5: 提交 Storage 修复**

```powershell
git add -- internal/typedef/storage.go internal/config/config_test.go
git commit -m "fix(config): preserve flat storage yaml"
```

---

### Task 2: Issue 首次与增量查询参数

**Files:**
- Modify: `internal/issue/issue_test.go`
- Modify: `internal/issue/issue.go`

**Interfaces:**
- Consumes: 本地 Markdown 得到的 `lastUpdate time.Time`、`gh.IssueListByRepoOptions`。
- Produces: `newIssueListOptions(lastUpdate time.Time) *gh.IssueListByRepoOptions`；零值时间省略 `since`，非零时间用 `lastUpdate.UTC()`。

- [ ] **Step 1: 添加查询捕获测试工具和两个失败测试**

在 `internal/issue/issue_test.go` 增加标准库导入 `net/http`、`net/http/httptest`、`net/url`，并添加：

```go
func captureIssueListQuery(t *testing.T, opt *gh.IssueListByRepoOptions) url.Values {
    t.Helper()
    var got url.Values
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        got = r.URL.Query()
        w.Header().Set("Content-Type", "application/json")
        _, err := w.Write([]byte("[]"))
        require.NoError(t, err)
    }))
    t.Cleanup(server.Close)

    client := gh.NewClient(server.Client())
    baseURL, err := url.Parse(server.URL + "/")
    require.NoError(t, err)
    client.BaseURL = baseURL
    _, _, err = client.Issues.ListByRepo(context.Background(), "owner", "repo", opt)
    require.NoError(t, err)
    return got
}

func TestNewIssueListOptionsInitialSyncOmitsSince(t *testing.T) {
    query := captureIssueListQuery(t, newIssueListOptions(time.Time{}))
    require.NotContains(t, query, "since")
    require.Equal(t, "all", query.Get("state"))
    require.Equal(t, "updated", query.Get("sort"))
    require.Equal(t, "asc", query.Get("direction"))
    require.Equal(t, "100", query.Get("per_page"))
}

func TestNewIssueListOptionsPreservesInstantInUTC(t *testing.T) {
    shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
    lastUpdate := time.Date(2026, 8, 17, 9, 30, 45, 0, shanghai)
    query := captureIssueListQuery(t, newIssueListOptions(lastUpdate))
    require.Equal(t, "2026-08-17T01:30:45Z", query.Get("since"))
}
```

- [ ] **Step 2: 运行 Issue 测试并确认编译失败**

Run:

```powershell
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test ./internal/issue -run 'TestNewIssueListOptions' -count=1
```

Expected: FAIL，`newIssueListOptions` 未定义。

- [ ] **Step 3: 实现查询 helper 并接入 Sync**

在 `internal/issue/issue.go` 添加：

```go
func newIssueListOptions(lastUpdate time.Time) *gh.IssueListByRepoOptions {
    return &gh.IssueListByRepoOptions{
        State:     "all",
        Since:     lastUpdate.UTC(),
        Sort:      "updated",
        Direction: "asc",
        ListOptions: gh.ListOptions{
            PerPage: 100,
        },
    }
}
```

首次同步分支不再赋值 `time.Unix(0, 0)`，让 `lastUpdate` 保持零值；用下面一行替换内联 `IssueListByRepoOptions`：

```go
opt := newIssueListOptions(lastUpdate)
```

- [ ] **Step 4: 格式化并验证 Issue 测试通过**

Run:

```powershell
gofmt -w internal/issue/issue.go internal/issue/issue_test.go
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test ./internal/issue -run 'TestNewIssueListOptions' -count=1
go test ./internal/issue -count=1
```

Expected: 两个查询测试和现有取消/锁测试全部 PASS。

- [ ] **Step 5: 提交 Issue 修复**

```powershell
git add -- internal/issue/issue.go internal/issue/issue_test.go
git commit -m "fix(issue): omit since on initial sync"
```

---

### Task 3: Docker 运行镜像时区数据

**Files:**
- Create: `dockerfile_test.go`
- Modify: `Dockerfile`
- Modify: `Dockerfile.goreleaser`

**Interfaces:**
- Consumes: 两个 Alpine runtime Dockerfile。
- Produces: 两个运行镜像均包含 `/usr/share/zoneinfo`，可解析 `TZ=Asia/Shanghai`。

- [ ] **Step 1: 添加失败的 Dockerfile 内容测试**

创建根目录 `dockerfile_test.go`：

```go
package main

import (
    "os"
    "regexp"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestRuntimeDockerfilesInstallTZData(t *testing.T) {
    installTZData := regexp.MustCompile(`(?m)^RUN apk add --no-cache[^\r\n]*\btzdata\b`)
    for _, filename := range []string{"Dockerfile", "Dockerfile.goreleaser"} {
        filename := filename
        t.Run(filename, func(t *testing.T) {
            content, err := os.ReadFile(filename)
            require.NoError(t, err)
            require.Regexp(t, installTZData, string(content))
        })
    }
}
```

- [ ] **Step 2: 运行根包测试并确认失败**

Run:

```powershell
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test . -run TestRuntimeDockerfilesInstallTZData -count=1
```

Expected: FAIL，两个 Dockerfile 的 `apk add` 行均缺少 `tzdata`。

- [ ] **Step 3: 在两个运行镜像中安装 tzdata**

将两个 Dockerfile 的运行阶段依赖行改为：

```dockerfile
RUN apk add --no-cache ca-certificates git tzdata
```

- [ ] **Step 4: 格式化测试并验证通过**

Run:

```powershell
gofmt -w dockerfile_test.go
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test . -run TestRuntimeDockerfilesInstallTZData -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交 Docker 时区修复**

```powershell
git add -- Dockerfile Dockerfile.goreleaser dockerfile_test.go
git commit -m "fix(docker): install timezone data"
```

---

### Task 4: 修正当前 Docker 配置

**Files:**
- Modify outside repository: `C:\Users\wnaut\Documents\projects\gitrieve-docker\config\config.yaml`

**Interfaces:**
- Consumes: 当前配置中的一个嵌套 `storage.storage` 对象和仓库 Storage 引用。
- Produces: 一个扁平的 `localFile/file//app/repo` Storage，所有仓库均引用 `localFile`；其他配置字节内容保持不变。

- [ ] **Step 1: 以不输出秘密的方式确认修改前状态**

读取配置到内存，只输出以下计数：嵌套 `- storage:` 为 1、`- local` 为 3、`- localFile` 为 1；确认 GitHub Token 非空但不输出其值。

- [ ] **Step 2: 使用带前置条件的精确替换修正配置**

经沙箱授权后，在 PowerShell 内存中完成两类替换：

```yaml
storage:
    - storage:
        name: localFile
        type: file
        path: /app/repo
```

改为：

```yaml
storage:
    - name: localFile
      type: file
      path: /app/repo
```

并把所有仓库列表中的 `- local` 精确改为 `- localFile`。替换前断言匹配数量为 1 和 3，替换后断言嵌套格式和精确 `- local` 均为 0，再原子写回同一路径。不得输出完整配置。

- [ ] **Step 3: 验证修正后配置可被最新 Go 代码加载**

在后续 Docker 重建后，通过 `/api/storage` 验证返回值，而不是把包含 Token 的配置打印到终端。预期 Storage 为：

```json
{"name":"localFile","type":"file","path":"/app/repo"}
```

当前 Docker 配置不属于 Git 仓库，本任务不为它创建提交。

---

### Task 5: 完整测试、构建与端到端回归

**Files:**
- Verify: entire repository
- Verify: `C:\Users\wnaut\Documents\projects\gitrieve-docker\docker-compose.yml`

**Interfaces:**
- Consumes: Tasks 1-4 的代码与配置。
- Produces: 已测试的 `gitrieve-local:latest`、运行中的最新 Compose 容器、真实组件归档证据。

- [ ] **Step 1: 运行完整 Go 测试和构建**

```powershell
$env:GOCACHE = Join-Path $env:TEMP 'gitrieve-go-build-cache'
go test ./... -count=1
go build -o (Join-Path $env:TEMP 'gitrieve-codex-verify.exe') main.go
```

Expected: 全部测试 PASS，构建退出码 0。

- [ ] **Step 2: 从 Git HEAD 创建无秘密构建上下文并构建镜像**

使用 `git archive --format=tar -o <temp.tar> HEAD` 和 `tar -xf` 创建临时上下文，避免把未跟踪的 `config.yaml`、日志、二进制或 `.git` 发送给 Docker。执行：

```powershell
docker build --pull -t gitrieve-local:latest <clean-context>
```

Expected: exit 0，新镜像含当前 HEAD 的三个修复提交。

- [ ] **Step 3: 重建 Compose 主服务并验证时区、镜像和健康状态**

```powershell
docker compose -f C:\Users\wnaut\Documents\projects\gitrieve-docker\docker-compose.yml up -d --force-recreate
```

验证：容器镜像 ID 等于 `gitrieve-local:latest`；状态为 `running`；`GET http://127.0.0.1:58080/health` 为 200；容器内 `/usr/share/zoneinfo/Asia/Shanghai` 存在，使用该 TZ 格式化的时区偏移为 `+0800`。

- [ ] **Step 4: 验证 Storage API**

请求 `GET http://127.0.0.1:58080/api/storage`，断言唯一 Storage 的 `name=localFile`、`type=file`、`path=/app/repo`，并确认所有仓库 Storage 引用均可在 Storage API 结果中解析。

- [ ] **Step 5: 真实验证 Issue 首次和增量同步**

使用独立临时输出目录和只含 `wnarutou/gitrieve` 的临时配置，通过环境变量注入现有 Token，不把 Token 写入仓库或输出日志：

1. 首次执行 `gitrieve issue component-test`，日志应获取 2 条 Issue/PR，并生成 `issues.tar.gz`。
2. 检查归档包含 `#1.md`、`#3.md`。
3. 保留临时 Issue 缓存再次执行，日志应走非零 `since` 的增量路径且命令成功。

- [ ] **Step 6: 回归 Release、Discussion 与 Wiki**

继续使用隔离配置：

- Release：`wnarutou/gitrieve` 最新一个 Release 仍下载 7 个资产。
- Discussion：仍生成包含 `discussion/2.md`、`discussion/4.md` 的归档。
- Wiki：使用 `golang/go`，仍生成包含 `.git` 历史与页面的 `go_wiki.tar.gz`。

- [ ] **Step 7: 清理并做最终状态检查**

安全删除本任务创建的精确临时目录、临时构建产物和临时测试容器；保留 Compose 主容器和真实 Docker 配置。执行 `git status --short`，确认没有本任务遗留的未提交文件；确认主容器仍运行、使用最新镜像且 `/health` 为 200。

