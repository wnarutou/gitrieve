# gitrieve 跨进程同步互斥（per-repo 文件锁）设计文档

日期：2026-08-10

## 1. 背景与目标

**同 repo + 同组件并发写同一 `.git`**：go-git 自身无锁，两个 goroutine/进程同时写同一仓库对象库（packfile / refs / index.lock）会损坏 `.gitrieve` 缓存。可达路径：

- **进程内**：executor 同一 job 快速重触发（`ExecuteJob` 每次 `go e.executeAsync(...)`）；daemon 同 job 超过周期未结束（gocron 无 `WithSingletonMode`，重叠触发）。
- **跨进程**：两个 `gitrieve run` / `gitrieve repository <name>` 进程同时跑同一仓库；daemon 与 CLI 同时跑。

除 code 的 `.git` 外，**wiki / issue / discussion / release** 组件同样有并发问题：

| 组件 | 入口函数 | 并发共享的资源 |
|---|---|---|
| code | `repository.Sync(ctx, repo, false, storages)` | `.gitrieve/<host>/<owner>/<repo>/code/.git`（useCache=true）+ storage `<name>.tar.gz` |
| wiki | `repository.Sync(ctx, repo, true, storages)` | `.gitrieve/<host>/<owner>/<repo>/wiki/.git` + storage `<name>_wiki.tar.gz` |
| issue | `issue.Sync` | `.gitrieve/<host>/<owner>/<repo>/issues/` + storage `issues.tar.gz` |
| discussion | `discussion.Sync` | `.gitrieve/<host>/<owner>/<repo>/discussion/` + storage `discussions.tar.gz` |
| release | `release.DownloadAllAssets` | storage `release/<tag>/<asset>`（含删除清理，read-modify-write 竞态） |

**目标**：同 repo + 同组件在同一主机、同一工作目录下的所有并发执行（进程内 goroutine 与跨进程）被互斥串行化；不同 repo、同 repo 不同组件保持并行。**函数签名不变**，所有调用方（daemon / executor / run / repository CLI）无需改动。

## 2. 方案选择

**选 A：双层锁——进程内 per-key `sync.Mutex` + 跨进程 OS 文件锁（`github.com/gofrs/flock`）。**

- **跨进程层**：`gofrs/flock` 是 Go 生态跨平台文件锁的标准库（Unix 用 `flock`，Windows 用 `LockFileEx`）。锁随进程自动释放——sync 进程被 kill（SIGKILL / 崩溃）时 OS 释放锁，**不会留下永久 stale lock**，后续 sync 不会被堵死。
- **进程内层**：同 key 的 `sync.Mutex`。原因：Windows 字节区间锁对「同进程不同句柄再锁同一区间」的语义在不同版本上有出入，不能依赖平台语义来挡同一进程内的两个 goroutine（executor 快速重触发）；mutex 在任何平台上都保证 goroutine 级互斥。
- 两层叠加后，进程内与跨进程两种情况都被排除，且不依赖平台锁语义。这也与原分析「per-repo 互斥」一致，只是 key 换成稳定的（repo, component）。

已确认：接受新增 `gofrs/flock` 依赖；锁文件位置 `<cwd>/.gitrieve/locks/`。

## 3. 锁的 key 与锁文件路径

- **key** = `path.Join(r.Host, r.Owner, r.Name, component)`，`r` 来自 `scm.NewRepository(repo.URL)`（`{Host, Owner, Name}`）。component ∈ `{code, wiki, issue, discussion, release}`。
- **锁文件路径** = `<cwd>/.gitrieve/locks/<key>.lock`，如 `<cwd>/.gitrieve/locks/github.com/wnarutou/gitrieve/code.lock`。与现有 `.gitrieve` 缓存布局一致。
- 锁文件**永不删除**（删除会引入 unlink 竞态：另一进程可能在新 inode 上拿锁，而持有者仍在旧 inode 上）。残留锁文件占用可忽略。
- key 与 `useCache` 无关：即使 `useCache=false`（gitDir 带 uuid、`.git` 不共享），storage 归档写入仍是共享资源，仍需互斥——用稳定 key 统一串行化，正确且简单。

## 4. 新包 `internal/lock`

```go
package lock

// Acquire 持有 (repo, component) 的排他锁，包含进程内 mutex 与跨进程文件锁两层。
// 等待锁期间 ctx 取消会立即返回 ctx.Err()，与现有 graceful-cancellation 一致。
// 返回的 release 必须恰好调用一次；任何返回值路径都要释放。
func Acquire(ctx context.Context, r *scm.Repository, component string) (release func(), err error)
```

实现要点：
- 进程内层：包级 `map[string]*sync.Mutex` + 守卫 `sync.Mutex`（模式与 executor 的 `runningJobs` 相同）。key 集合由「配置 repo × 5 组件」界定，有界、无需清理。
- 跨进程层：`flock.New(lockPath)`，先 `os.MkdirAll(filepath.Dir(lockPath))`，再 `f.LockContext(ctx)`（可被 ctx 取消）。
- 获取顺序：先进程内 mutex，后 flock；任一层失败都要释放已获取的层再返回错误。
- release：`f.Unlock()`（gofrs/flock 自动随进程释放，不删文件），再释放进程内 mutex。
- `cwd` 在 Acquire 调用时用 `os.Getwd()` 解析——各函数在 os.Chdir 之前调用 Acquire，取到的是正确 cwd。

## 5. 各组件接入点

锁放在**各组件函数内部**（而非调用方），这样 daemon / executor / run / repository CLI 所有调用路径都被覆盖。

- `repository.Sync`（code/wiki 共用）：在 `scm.NewRepository` 成功、非法名检查之后（`repository.go` ~L114）：
  ```go
  component := "code"
  if iswiki {
      component = "wiki"
  }
  release, err := lock.Acquire(ctx, r, component)
  if err != nil {
      return err
  }
  defer release()
  ```
  整个函数体（含 clone 失败时的 `os.RemoveAll(gitDir)`、fetch/pull、归档、storage 写入）都在锁内——这同时堵住了「第二个调用者在 clone 进行中读到 `exist=false`、clone 失败后 `RemoveAll` 删掉第一个调用者正在用的 `.git`」这一损坏向量。
- `issue.Sync`：`scm.NewRepository` 成功、非法名检查之后（`issue.go` ~L56）加锁，component = `issue`。
- `discussion.Sync`：同上（`discussion.go` ~L178），component = `discussion`。
- `release.DownloadAllAssets`：`scm.NewRepository` 成功后（`release.go` ~L41）加锁，component = `release`。

所有组件函数签名不变，调用方零改动。

## 6. 避免 wiki 双重锁（关键）

`wiki.Sync` 调用 `repository.Sync(ctx, repo, true, storages)`。**只在 `repository.Sync` 内部加锁**（按 iswiki 区分 code/wiki），`wiki.Sync` 自身的只读 API 检查（`client.Repositories.Get`）不上锁——否则同进程内两处拿同一把锁直接死锁。

## 7. 取消与超时语义

- 等待锁期间 ctx 取消 → `LockContext(ctx)` 返回 `ctx.Err()`，调用方收到错误并按既有逻辑处理（executor 记 cancelled、CLI 打印取消），不会无限挂起。
- 组件正常执行的取消行为不改变：已持锁的组件按自身既有取消检查点优雅停止，`defer release()` 保证释放锁。
- 进程崩溃（无 ctx 路径）：flock 随进程终止自动释放，不阻塞后续。

## 8. 明确不做 / 边界

- **不同组件互不阻塞**：code 与 wiki 写不同 `.git`，key 不同，保持并行，与现状一致。
- **多机 / 不同 cwd + 绝对存储路径**：锁文件在 `<cwd>/.gitrieve/locks`，只保护**同主机、同工作目录**下的进程。跨机器并发写同一 S3 bucket 需要分布式锁（S3 条件写 / object lock），不在本次范围。
- **CWD 竞态**（`os.Chdir` 归档导致的进程全局目录竞争）：按用户确认，不在本次范围。进程内 mutex 以稳定 key 串行化同 repo 同组件，已把该竞态限制在可接受范围。
- **daemon 加 `WithSingletonMode`**：不做（会改变重叠语义为「跳过」，可能漏掉一次归档；mutex 已消除损坏）。
- 不修改各组件函数签名、不修改 `internal/scm/github` 封装。

## 9. 测试

新增 `internal/lock/lock_test.go`：
- **同 key 串行**：A 持有 key K，B 请求 K 被阻塞；A 释放后 B 获得。
- **不同 key 并行**：K1 / K2 同时获取，互不阻塞。
- **等待锁时 ctx 取消**：A 持有 K，B 用稍后取消的 ctx 请求 K，断言 B 尽快返回 `ctx.Err()`。
- **预取消 ctx**：直接返回 `ctx.Err()`，无需任何真实文件锁竞争。
- **跨进程**：测试用 `os/exec` 自举子进程（`-test.run` + 环境变量标记），子进程持锁期间父进程被阻塞，子进程退出（进程终止）后父进程获得锁——证明文件锁跨进程生效且进程死亡自动释放。

既有测试保持通过：`internal/repository/repository_test.go`（`TestSyncCancelledContextFailsPromptlyAndCleansUp`）路径不变，锁释放路径覆盖。
