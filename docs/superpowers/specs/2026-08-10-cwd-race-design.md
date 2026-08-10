# Gitrieve os.Chdir 并发 cwd 竞态修复设计文档

日期：2026-08-10

## 1. 背景与目标

`executor`（server）对每个 job 执行 `go e.executeAsync(...)`（无信号量、goroutine 数不限）；`daemon` 用 `gocron.WithLimitConcurrentJobs(cocurrencyNum)`（默认 `cocurrencyNum = 3`）。多个 job 会**并发**运行。

三处同步代码在归档前依赖进程全局 cwd：

- `internal/repository/repository.go`（code 与 wiki 两种模式，wiki 经 `internal/wiki/wiki.go` 转调）
- `internal/issue/issue.go`
- `internal/discussion/discussion.go`

它们都是同一个模式：

```go
currentDir, _ := os.Getwd()
...
os.Chdir(path.Dir(gitDir))                    // 切换到仓库父目录
files, _ := archives.FilesFromDisk(..., map[string]string{"code": r.Name})  // 相对路径 key
archive := ...                                // 打包
os.Chdir(currentDir)                          // 切回
```

**问题**：`os.Chdir` 修改进程全局状态。并发 goroutine A chdir 到 A 的仓库父目录后，goroutine B 再 chdir，A 的 `FilesFromDisk` 相对路径 key 会解析到 B 的目录 → **归档内容错乱**；且 `os.Chdir(currentDir)` 的 restore 基于过期的保存值。这是架构层面的竞态。

**顺带存在的隐藏 bug**（单线程也触发）：`FilesFromDisk` 或 `format.Archive` 出错时，三个文件的错误路径都直接 `return err`，**没有 restore cwd** → 进程 cwd 被永久留在仓库父目录，后续 job 的 `.gitrieve` 相对路径与相对 storage path（`s.Path`）全部错位。

**目标**：彻底移除 `os.Chdir` 依赖，让并发 job 归档天然安全、归档内容互不串扰、进程 cwd 恒定不变；并新增并发回归测试。

**已确认的范围**：
1. 修复采用「删 `os.Chdir`、`FilesFromDisk` 改用绝对路径 `gitDir`」。
2. 附带并发回归测试（对着共享 helper 写，端到端 `Sync` 硬编码 `https://` 必须连网，无法用于单测）。
3. 「同仓库同组件并发写同一 `.git`」的竞态（go-git 无锁）**不在本次范围**，记为 follow-up。

## 2. 方案选择

**选 A：删除 `os.Chdir`，`FilesFromDisk` 的 map key 直接用绝对路径 `gitDir`。**

依据（已读 `github.com/mholt/archives v0.1.1` 源码验证）：

- 三个文件的归档源目录**恰好就是 `gitDir`**：`gitDir = workingDir/host/owner/name/{code|wiki|issues|discussion}`，而相对 key（`code` 等）解析到 `path.Dir(gitDir)/<key>` = `gitDir`。
- `archives.FilesFromDisk` 支持绝对路径 key。其 `nameOnDiskToNameInArchive` 用 `strings.TrimPrefix(nameOnDisk, rootOnDisk)` 截掉源前缀再 `path.Join(rootInArchive, filepath.ToSlash(truncPath))`——绝对 key 与相对 key 产出**完全一致的归档条目**（`target/...`），根目录条目行为也相同（`info.IsDir() && nameInArchive == ""` 判断不受影响）。
- Windows 下 `filepath.WalkDir` 与 `TrimPrefix` 两侧同为 `\` 分隔，行为一致。

选 A 的收益：
- 架构层面根治：不再有进程级全局可变状态，goroutine 天然安全，无需任何锁。
- 归档条目布局与内容不变（兼容已存储的历史归档）。
- 顺带修掉「错误路径 cwd 泄漏」隐藏 bug；相对 storage path 解析因 cwd 恒定而变得确定。

备选并否决：
- **B：包级 `sync.Mutex` 包住 Chdir 窗口（defer restore）**——串行化所有并发 job 的磁盘读取阶段（大仓库可达几十秒）；仍依赖进程级全局 cwd 这种脆弱状态；且 `-race` 检测不到 cwd 竞态（它是内核态进程状态，不是共享内存），回归只能靠内容断言。属补丁而非根治。
- **C：子进程隔离（`exec` 设 `Dir`）**——对一次归档起子进程开销过大，且归档字节需回传进程内给 S3 等后端。否决。

## 3. 核心改动：新增 `internal/archive` 共享 helper

三处重复的「Chdir → FilesFromDisk → 打包」块收敛为一个单职责 helper，修复只落在一个地方，测试也对着它写：

```go
// internal/archive/archive.go
package archive

// Create 将绝对路径 sourceDir 打成 gzip tarball，targetName 作为归档内的根目录。
// 它永不修改进程 cwd，因此可在并发 job goroutine 中安全调用。
func Create(ctx context.Context, sourceDir, targetName string) (*bytes.Buffer, error)
```

实现：

```go
files, err := archives.FilesFromDisk(ctx, &archives.FromDiskOptions{}, map[string]string{sourceDir: targetName})
if err != nil {
    return nil, err
}
buf := &bytes.Buffer{}
format := archives.CompressedArchive{
    Compression: archives.Gz{},
    Archival:    archives.Tar{},
}
if err := format.Archive(ctx, buf, files); err != nil {
    return nil, err
}
return buf, nil
```

**顺带改进**：context 用调用方传入的 sync ctx（现状是 `context.TODO()` / `context.Background()`）。job 取消时磁盘遍历更早中止，最终结果一致（同样不上传），符合近期「优雅取消」主题，零风险。

## 4. 调用点改动（3 个文件）

三处相同模式：`os.Chdir(path.Dir(gitDir))` → `FilesFromDisk(相对)` → `format.Archive` → `os.Chdir(currentDir)`，全部替换为 `archive.Create(ctx, gitDir, target)`。

| 文件 | gitDir 末段 | 归档目标名 | 删除的 Chdir |
|---|---|---|---|
| `internal/repository/repository.go`（code 模式） | `code` | `r.Name` | 334、376 |
| `internal/repository/repository.go`（wiki 模式） | `wiki` | `r.Name + "_wiki"` | 同一处 |
| `internal/issue/issue.go` | `issues` | `"issues"` | 228、255 |
| `internal/discussion/discussion.go` | `discussion` | `"discussion"` | 451、477 |

注意点：
- `currentDir` **保留**（`workingDir = currentDir/.gitrieve` 仍需要它）。
- 清理不再使用的 `bytes` / `archives` import。
- 存储循环 `backend.PutObject` 不改动。

## 5. 回归测试（`internal/archive/archive_test.go`）

### 5.1 并发安全

N=12 个 goroutine，各自创建含独特 sentinel 内容的目录，用起跑屏障（`sync.WaitGroup`）同时开始 `Create`：

1. **进程 cwd 不变**：`os.Getwd()` 在测试前后必须相等（确定性断言，直接锁定「不再 Chdir」不变量）。
2. **归档内容互不串扰**：解包每个归档（标准库 `compress/gzip` + `archive/tar`），断言**每个归档包含且仅包含自己的 sentinel 文件内容**，不包含任何其他 goroutine 目录的 sentinel（高概率捕获「相对 key + Chdir」回归）。

配合 `go test -race ./internal/archive/` 运行。注意：`-race` 检测不到 cwd 竞态（非共享内存），所以核心保障是内容断言 + cwd 不变断言。

### 5.2 条目名兼容

绝对源 + target 归档后解包，断言条目恰为 `target/<rel>`（例如 `code/file.txt`）——把「归档布局与相对 key 时代一致」锁死，防止 archives 库升级破坏兼容性。测试目录位于 `t.TempDir()`（非进程 cwd），天然验证 helper 的 cwd 无关性。

## 6. 验证方式

- `go build ./...` 通过。
- `go test ./...` 全绿；`go test -race ./internal/archive/` 全绿。
- 手动：`gitrieve run` 对含 code+issues 的仓库跑一次，确认归档文件（`code`→`<name>.tar.gz`、`issues`→`issues.tar.gz`）内容与旧版一致。

## 7. 范围外（follow-up）

- **同 repo + 同组件并发**写同一 `.git`：go-git 自身无锁（零 `os.Chdir`、路径在 `PlainOpen`/`PlainClone` 时一次 `filepath.Abs` 归一化、无跨 goroutine 可变全局状态，跨仓库并发是安全的）；但两个 goroutine 同时写同一仓库对象库（packfile/refs/index.lock）会损坏缓存。可达路径：executor 同一 job 快速重触发、daemon 同 job 超过周期未结束。独立 follow-up：按 gitDir 做 per-repo 互斥或 singleflight。
