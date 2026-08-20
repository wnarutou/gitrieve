# 仓库身份重构（URL 为主键）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把仓库身份从 `name` 重构为规范化 URL（`repo_key`），贯穿 DB → executor → API → 前端，使 URL 成为唯一判等口径，为子项目 B「配置导入导出」奠基。

**Architecture:** 在 `typedef` 引入身份原语（`NormalizeURL` / `EffectiveURL` / `Key` / `Matches`）作为唯一判等口径；`executions` 新增 `repo_key` 列（保留 `job_name` 作为展示快照）；executor 按 `Matches` 定位、org/user 经 `repository.Expand` 在任务内展开为具体仓库逐条记账；server API 的创建/查询/CRUD 全部按身份键；前端把操作按钮的编码从 name 换成 URL。`cmd/daemon` 只做日志、无身份逻辑，不动。

**Tech Stack:** Go 1.x, gin-gonic, modernc.org/sqlite（纯 Go 驱动）, viper, go-git, testify。

## Global Constraints

- **主键 = 规范化 URL**；**空 URL 即非法**：创建（400）、更新（400）、启动配置校验（`ui.ErrorfExit`）一律拒绝；无 `name:` 兜底。
- **name 允许重复、允许改名**，仅用于展示与查询；删除按 name 判重的 409。
- **不做历史数据回填**：`executions` 只新增 `repo_key` 列，旧行键保持 `''`（纯历史显示）。
- **org/user 条目**：URL 为空时按 `orgName` 合成 `https://github.com/<orgName>` 并**写回条目**（config 存合成后的完整 URL）。
- **执行/记账粒度**：运行 org/user 时任务内展开成具体仓库，每个具体仓库一条 execution；org 统计 = URL **路径边界前缀**聚合（`entry.Key()+"/"` 前缀，避免 `wnarutou` 误吞 `wnarutou2/x`）。
- **URL 修改 = 身份变更**：允许改；旧 `repo_key` 历史保持原样、不再被新键关联；更新后与其他仓库 URL 冲突 → 409（不与自身比较）。
- **查询**：任务过滤与仓库搜索均支持 name + URL 模糊匹配；`GetJobs` 的 URL 搜索词先 `NormalizeURL` 再包 `%`。
- **API 破坏性变更**：`POST /api/jobs` 请求字段 `repository` → `repository_key`，响应 `job_id` → `job_ids`（数组）；仓库 `PUT/DELETE /api/repositories/:id` 的 `:id` 语义变为身份键。
- **保留** `GetRepositories` 聚合 SQL 的 `GROUP BY xxx HAVING start_time = MAX(start_time)` 写法，以及「只有声明为 DATETIME 的列才能 scan 成 `time.Time`」的驱动约束（不能对聚合表达式 `MAX(start_time)` 做 scan）。
- **明确不做**：`cmd/daemon` 行为改动；前端 JS 做 URL 规范化（以后端为准）；配置导入导出（子项目 B）。

---

### Task 1: typedef 身份原语（key.go）+ 启动配置校验

**Files:**
- Create: `internal/typedef/key.go`
- Test: `internal/typedef/key_test.go`
- Modify: `internal/typedef/repository.go`（无改动，仅确认 `GetType` 存在）
- Modify: `internal/config/config.go:59` 之后追加空 URL 启动校验
- Test: `internal/config/config_test.go` 追加校验用例

**Interfaces:**
- Produces（后续任务全部依赖）:
  - `func NormalizeURL(raw string) string` — 规范化 URL 形态 `host/owner/repo`，空输入返回 `""`
  - `func (r *Repository) EffectiveURL() string` — URL 非空直接用；user/org 且 URL 为空、orgName 非空时合成 `https://github.com/<orgName>`；否则 `r.URL`
  - `func (r *Repository) Key() string` — `NormalizeURL(r.EffectiveURL())`
  - `func (r *Repository) Matches(input string) bool` — 输入规范化后与 `Key()` 判等；`Key()` 为空或输入为空返回 false

- [ ] **Step 1: 写失败测试** `internal/typedef/key_test.go`

```go
package typedef

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"github.com/wnarutou/gitrieve", "github.com/wnarutou/gitrieve"},
		{"https://GITHUB.com/wnarutou/gitrieve/", "github.com/wnarutou/gitrieve"},
		{"http://github.com/wnarutou/gitrieve.git", "github.com/wnarutou/gitrieve"},
		{"https://www.github.com/wnarutou/gitrieve", "github.com/wnarutou/gitrieve"},
		{"www.github.com/wnarutou/gitrieve", "github.com/wnarutou/gitrieve"},
		{"https://github.com/wnarutou/gitrieve#readme", "github.com/wnarutou/gitrieve"},
		{"https://gitlab.com/Wnarutou/proj.git/", "gitlab.com/wnarutou/proj"},
		{"HTTPS://GitHub.com/Foo/Bar.Git", "github.com/foo/bar"},
		{"   https://github.com/foo/bar  ", "github.com/foo/bar"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, NormalizeURL(c.in), "NormalizeURL(%q)", c.in)
	}
}

func TestEffectiveURL(t *testing.T) {
	cases := []struct {
		repo Repository
		want string
	}{
		{Repository{Type: TypeRepo, URL: "github.com/a/b"}, "github.com/a/b"},
		{Repository{Type: TypeOrg, OrgName: "acme"}, "https://github.com/acme"},
		{Repository{Type: TypeUser, OrgName: "alice"}, "https://github.com/alice"},
		// 显式 URL 优先于合成
		{Repository{Type: TypeOrg, URL: "gitlab.com/acme/org", OrgName: "acme"}, "gitlab.com/acme/org"},
		// orgName 为空 → 无有效 URL
		{Repository{Type: TypeOrg}, ""},
		// 非 user/org 且无 URL → 无有效 URL
		{Repository{Type: TypeRepo}, ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.repo.EffectiveURL(), "EffectiveURL(%+v)", c.repo)
	}
}

func TestKeyAndMatches(t *testing.T) {
	repo := Repository{Name: "x", URL: "https://GitHub.com/Foo/Bar.git"}
	assert.Equal(t, "github.com/foo/bar", repo.Key())

	assert.True(t, repo.Matches("github.com/foo/bar"))
	assert.True(t, repo.Matches("https://github.com/foo/bar"))
	assert.True(t, repo.Matches("HTTPS://GITHUB.COM/FOO/BAR.GIT"))
	assert.True(t, repo.Matches("github.com/foo/bar/"))
	assert.False(t, repo.Matches("github.com/other/bar"))
	assert.False(t, repo.Matches(""))

	// 空身份条目谁也不匹配
	empty := Repository{Name: "orphan"}
	assert.Equal(t, "", empty.Key())
	assert.False(t, empty.Matches("github.com/foo/bar"))
	assert.False(t, empty.Matches(""))
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/typedef/ -run 'TestNormalizeURL|TestEffectiveURL|TestKeyAndMatches'`
Expected: FAIL — `undefined: NormalizeURL` / `undefined: EffectiveURL` 等编译错误。

- [ ] **Step 3: 实现** `internal/typedef/key.go`

```go
package typedef

import (
	"strings"
)

// NormalizeURL 归一化仓库 URL，产出无协议、小写、无 www、无尾斜杠、无 .git 的
// 规范形态 "host/owner/repo"。处理顺序：去空白 → 去 #fragment → 小写 →
// 去 http(s):// → 去 www. → 去尾斜杠 → 去 .git。返回 "" 表示没有可用 URL。
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// EffectiveURL 返回条目的有效 URL：URL 非空直接用；type 为 user/org 且 URL 为
// 空、orgName 非空时合成 "https://github.com/<orgName>"；否则返回 r.URL（可能
// 为空，即非法）。
func (r *Repository) EffectiveURL() string {
	if (r.GetType() == TypeUser || r.GetType() == TypeOrg) &&
		strings.TrimSpace(r.URL) == "" && r.OrgName != "" {
		return "https://github.com/" + r.OrgName
	}
	return r.URL
}

// Key 返回仓库身份键 = NormalizeURL(EffectiveURL())。空 URL 会得到 ""，
// 调用方（创建/更新/启动校验）必须拒绝。
func (r *Repository) Key() string {
	return NormalizeURL(r.EffectiveURL())
}

// Matches 判断一段用户输入是否命中该仓库：输入被规范化后与 Key() 比较。
// Key() 为空（无身份）或输入为空时返回 false。
func (r *Repository) Matches(input string) bool {
	key := r.Key()
	if key == "" {
		return false
	}
	return NormalizeURL(input) == key
}
```

- [ ] **Step 4: 运行 typedef 测试确认通过**

Run: `go test ./internal/typedef/`
Expected: PASS。

- [ ] **Step 5: 加 `validateIdentity` 校验函数并在 `Init` 末尾调用**（`internal/config/config.go`）

在 `Save` 函数前新增：

```go
// validateIdentity ensures every repository entry has a usable identity (a
// non-empty URL, or orgName for user/org types). The repository identity is the
// normalized URL; an entry without one can never be matched or executed.
// Returns an error rather than exiting so it is unit-testable; Init surfaces
// it via ui.ErrorfExit.
func validateIdentity(cfg *Config) error {
	for _, repo := range cfg.Repository {
		if repo.Key() == "" {
			return fmt.Errorf("repository %q (type %q) has an empty URL and no orgName; every repository needs a URL identity",
				repo.Name, repo.GetType())
		}
	}
	return nil
}
```

`Init` 末尾（第 59 行 `}` 之后、`Init` 结束前）：

```go
	// 启动校验：每个仓库条目都必须有可用身份。身份键为空意味着永远无法被
	// 匹配或执行，直接拒绝启动。
	if err := validateIdentity(ins); err != nil {
		ui.ErrorfExit("Invalid configuration: %s", err)
	}
```

（注意：`ui.ErrorfExit` 调用 `os.Exit(1)` 而非 panic，因此测试必须针对 `validateIdentity` 本身，不能 `require.Panics` 包 `Init`。）

- [ ] **Step 6: 追加 config 校验测试**（`internal/config/config_test.go` 末尾追加；import 增加 `github.com/wnarutou/gitrieve/internal/typedef`）

```go
func TestValidateIdentity(t *testing.T) {
	// 非 user/org 且无 URL → 校验拒绝。
	err := validateIdentity(&Config{Repository: []typedef.Repository{
		{Name: "orphan", Type: typedef.TypeRepo},
	}})
	require.Error(t, err)

	// user/org 且带 orgName → 通过；合成 URL 即为身份。
	acme := Config{Repository: []typedef.Repository{
		{Name: "acme", Type: typedef.TypeOrg, OrgName: "acme"},
	}}
	require.NoError(t, validateIdentity(&acme))
	require.Equal(t, "https://github.com/acme", acme.Repository[0].EffectiveURL())

	// 空仓库列表 → 通过。
	require.NoError(t, validateIdentity(&Config{}))
}
```

- [ ] **Step 7: 运行 config 测试确认通过**

Run: `go test ./internal/config/`
Expected: PASS。（现有用例的配置均不含 `repository:` 段，`ins.Repository` 为 nil，遍历空切片，不受影响。）

- [ ] **Step 8: 提交**

```bash
git add internal/typedef/key.go internal/typedef/key_test.go internal/config/config.go internal/config/config_test.go
git commit -m "feat(typedef): repository identity as normalized URL (Key/Matches) + startup validation"
```

---

### Task 2: DB 层 —— executions 增加 repo_key 列 + Migrate + server 接线

**Files:**
- Modify: `internal/db/db.go:44-52`（CREATE TABLE 加 `repo_key` 列）
- Create: `internal/db/migrate.go`
- Test: `internal/db/migrations_test.go`（追加 Migrate 用例）
- Modify: `cmd/server/server.go:46-49`（`db.Initialize` 之后调用 `db.Migrate`）

**Interfaces:**
- Consumes: 无
- Produces:
  - `func Migrate(d *DB) error` — 幂等：`PRAGMA table_info(executions)` 缺 `repo_key` 列时 `ALTER TABLE ... ADD COLUMN repo_key TEXT NOT NULL DEFAULT ''`；**不做数据回填**
  - `executions.repo_key TEXT NOT NULL DEFAULT ''`

- [ ] **Step 1: 写失败测试**（追加到 `internal/db/migrations_test.go`）

```go
// TestMigrateAddsRepoKeyColumn verifies Migrate upgrades a legacy executions
// table (no repo_key) in place without backfilling, and that new rows can
// write repo_key.
func TestMigrateAddsRepoKeyColumn(t *testing.T) {
	testDB, err := Initialize(":memory:")
	assert.NoError(t, err)
	defer testDB.Close()

	// Rebuild executions in the pre-migration shape (no repo_key).
	_, err = testDB.Exec(`DROP TABLE executions`)
	assert.NoError(t, err)
	_, err = testDB.Exec(`
		CREATE TABLE executions (
			id TEXT PRIMARY KEY,
			job_name TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME,
			status TEXT NOT NULL,
			error_message TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
	assert.NoError(t, err)

	// A pre-existing legacy row.
	_, err = testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"old", "repo-a", time.Now(), "completed")
	assert.NoError(t, err)

	assert.NoError(t, Migrate(testDB))

	// Column now exists; legacy row's key stays empty (no backfill).
	var key string
	err = testDB.QueryRow(`SELECT repo_key FROM executions WHERE id = 'old'`).Scan(&key)
	assert.NoError(t, err)
	assert.Equal(t, "", key)

	// New rows can write repo_key.
	_, err = testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, status) VALUES (?, ?, ?, ?, ?)`,
		"new", "repo-b", "github.com/b/b", time.Now(), "running")
	assert.NoError(t, err)
}

// TestMigrateIsIdempotent verifies Migrate on a fresh (already current) DB is a no-op.
func TestMigrateIsIdempotent(t *testing.T) {
	testDB, err := Initialize(":memory:")
	assert.NoError(t, err)
	defer testDB.Close()

	assert.NoError(t, Migrate(testDB))
	assert.NoError(t, Migrate(testDB))
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/db/ -run 'TestMigrate'`
Expected: FAIL — `undefined: Migrate`。

- [ ] **Step 3: 实现** `internal/db/migrate.go`

```go
package db

import "fmt"

// Migrate upgrades an existing database to the current schema. Additive-only:
// it adds the repo_key column to executions when missing and never backfills or
// drops data. Call once at server startup (after Initialize).
func Migrate(d *DB) error {
	has, err := columnExists(d, "executions", "repo_key")
	if err != nil {
		return fmt.Errorf("check executions.repo_key: %w", err)
	}
	if !has {
		if _, err := d.Exec(`ALTER TABLE executions ADD COLUMN repo_key TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add executions.repo_key: %w", err)
		}
	}
	return nil
}

// columnExists reports whether table has a column named column.
func columnExists(d *DB, table, column string) (bool, error) {
	rows, err := d.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             *string
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
```

- [ ] **Step 4: 在 CREATE TABLE 中带上新列**（`internal/db/db.go`，`job_name` 行后加一行）

```go
			job_name TEXT NOT NULL,
			repo_key TEXT NOT NULL DEFAULT '',
```

- [ ] **Step 5: 运行 db 测试确认通过**

Run: `go test ./internal/db/`
Expected: PASS（含新增两个用例）。

- [ ] **Step 6: 接线到 server 启动**（`cmd/server/server.go`，`db.Initialize` 成功之后）

```go
	// 迁移旧库（新增 repo_key 列）。失败宁可起不来，也不在坏 schema 上跑。
	if err := db.Migrate(database); err != nil {
		ui.ErrorfExit("Failed to migrate database: %s", err)
	}
```

- [ ] **Step 7: 构建确认**

Run: `go build ./...`
Expected: 成功。

- [ ] **Step 8: 提交**

```bash
git add internal/db/db.go internal/db/migrate.go internal/db/migrations_test.go cmd/server/server.go
git commit -m "feat(db): executions.repo_key column + additive Migrate wired at server startup"
```

---

### Task 3: repository.Expand（导出 addRepo）+ GetRepositories 支持按 name 或 URL 匹配

**Files:**
- Modify: `internal/repository/repository.go:23-76`
- Test: `internal/repository/expand_test.go`（新增，白盒 `package repository`，mock 掉 GitHub 客户端）

**Interfaces:**
- Consumes: `typedef.Repository.Key()` / `.Matches()`（Task 1）
- Produces:
  - `func Expand(repo typedef.Repository) []typedef.Repository` — type=repo 原样返回；user/org 经 GitHub API 展开为具体仓库（每个都带继承的 cron/storage/useCache 等）；非法类型返回空切片
  - `type repoLister interface { GetRepos(name, accountType string) ([]string, error) }`
  - `var newGithubClient = func() (repoLister, error)`（测试可替换的包级 seam）
  - `repository.GetRepositories(query)` 的过滤改为 `repo.Name == query || repo.Matches(query)`

- [ ] **Step 1: 写失败测试** `internal/repository/expand_test.go`

```go
package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type fakeRepoLister struct {
	repos []string
	err   error
}

func (f *fakeRepoLister) GetRepos(name, accountType string) ([]string, error) {
	return f.repos, f.err
}

func TestExpand(t *testing.T) {
	old := newGithubClient
	t.Cleanup(func() { newGithubClient = old })
	newGithubClient = func() (repoLister, error) {
		return &fakeRepoLister{repos: []string{"github.com/acme/alpha", "github.com/acme/beta"}}, nil
	}

	t.Run("repo passthrough", func(t *testing.T) {
		repo := typedef.Repository{Name: "solo", URL: "github.com/a/solo", Type: typedef.TypeRepo}
		got := Expand(repo)
		require.Len(t, got, 1)
		assert.Equal(t, "solo", got[0].Name)
		assert.Equal(t, "github.com/a/solo", got[0].URL)
	})

	t.Run("org expands to concrete repos inheriting options", func(t *testing.T) {
		org := typedef.Repository{
			Name: "acme", Type: typedef.TypeOrg, OrgName: "acme",
			Cron: "0 2 * * *", AllBranches: true,
		}
		got := Expand(org)
		require.Len(t, got, 2)
		assert.Equal(t, "alpha", got[0].Name)
		assert.Equal(t, "github.com/acme/alpha", got[0].URL)
		assert.Equal(t, typedef.TypeRepo, got[0].GetType())
		assert.Equal(t, "0 2 * * *", got[0].Cron) // 继承父条目配置
		assert.True(t, got[0].AllBranches)
		assert.Equal(t, "beta", got[1].Name)
	})

	t.Run("invalid type yields nothing", func(t *testing.T) {
		bad := typedef.Repository{Name: "x", URL: "github.com/a/x", Type: "whatever"}
		assert.Empty(t, Expand(bad))
	})
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/repository/ -run TestExpand`
Expected: FAIL — `undefined: newGithubClient` / `undefined: Expand`。

- [ ] **Step 3: 改造 `internal/repository/repository.go`**

把第 46 行的 `client, err := github.New()` 改为经 seam 调用；新增 `repoLister`、`newGithubClient`、`Expand`；`GetRepositories` 的过滤改为 name 或 URL。

```go
// repoLister 抽象 GitHub 客户端，测试时替换 newGithubClient 即可 mock。
type repoLister interface {
	GetRepos(name, accountType string) ([]string, error)
}

// newGithubClient 是可替换的包级 seam：生产用真客户端，测试注入 fake。
var newGithubClient = func() (repoLister, error) { return github.New() }
```

`GetRepositories` 中按名字找的循环改为：

```go
	if name != "" {
		for _, repository := range internalconfig.GetIns().Repository {
			if repository.Name == name || repository.Matches(name) {
				repositories = addRepo(repository, repositories)
			}
		}
		return repositories
	}
```

`addRepo` 中创建客户端改为：

```go
		client, err := newGithubClient()
```

文件末尾新增导出函数：

```go
// Expand 返回一个配置条目实际对应的具体仓库列表。type=repo 原样返回自身；
// type=user/org 通过 GitHub API 展开为成员仓库（继承 cron/storage 等选项）；
// 非法类型返回空切片。CLI 与 executor 共用。
func Expand(repo typedef.Repository) []typedef.Repository {
	return addRepo(repo, nil)
}
```

（`github` 包仍被 import——`newGithubClient` 闭包引用 `github.New()`。）

- [ ] **Step 4: 运行 repository 测试确认通过**

Run: `go test ./internal/repository/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/repository/repository.go internal/repository/expand_test.go
git commit -m "feat(repository): export Expand + name-or-URL matching in GetRepositories"
```

---

### Task 4: executor 按 repo_key 查找 + org/user 展开为多 job

**Files:**
- Modify: `internal/executor/executor.go:57-104`
- Modify: `internal/executor/executor_test.go`（既有用例改为传 URL 键、返回值改 `[]string`；新增 repo_key 写入与 org 展开用例）

**Interfaces:**
- Consumes: `typedef.Repository.Matches()` / `.Key()`（Task 1）、`repository.Expand()`（Task 3）、`executions.repo_key` 列（Task 2）
- Produces:
  - `var ErrRepositoryNotFound = errors.New("repository not found in configuration")`
  - `func (e *Executor) ExecuteJob(repoKey string) ([]string, error)` — 按 `Matches` 定位；找不到返回 `ErrRepositoryNotFound`；type=repo 返回 `[]string{jobID}`；user/org 经展开对每个具体仓库产生一条 execution，返回多个 jobID
  - `func (e *Executor) launchJob(job typedef.Repository) (string, error)` — 原 ExecuteJob 主体：生成 jobID、INSERT（`job_name` + `repo_key`）、登记 runningJobs、异步执行
  - `var expandRepos = repository.Expand`（executor 包级 seam，测试注入 fake）

- [ ] **Step 1: 改既有 executor 测试适配新签名**

`internal/executor/executor_test.go`：
- `TestExecuteJobWritesBoundLogs`（第 34 行）、`TestExecuteJobRunsConfiguredComponents`（第 86 行）、`TestExecuteJobCreatesRecord`（第 113 行）：
  `jobID, err := exec.ExecuteJob("test-repo")` → `jobIDs, err := exec.ExecuteJob("github.com/test/repo")` 且 `require.Len(t, jobIDs, 1)`，随后用 `jobIDs[0]`。
- `TestExecuteJobUnknownRepository`（第 130 行）：`exec.ExecuteJob("does-not-exist")` 保持报错（规范化后仍无命中）。

- [ ] **Step 2: 写失败测试**（`internal/executor/executor_test.go` 追加）

```go
func TestExecuteJobWritesRepoKey(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobIDs, err := exec.ExecuteJob("https://github.com/test/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)

	var repoKey string
	err = testDB.QueryRow("SELECT repo_key FROM executions WHERE id = ?", jobIDs[0]).Scan(&repoKey)
	assert.NoError(t, err)
	assert.Equal(t, "github.com/test/repo", repoKey)
}

func TestExecuteJobExpandsOrgIntoMultipleJobs(t *testing.T) {
	exec, testDB := newTestExecutor(t)
	exec.cfg.Repository = []typedef.Repository{
		{Name: "acme", URL: "https://github.com/acme", Type: typedef.TypeOrg, OrgName: "acme"},
	}

	old := expandRepos
	t.Cleanup(func() { expandRepos = old })
	expandRepos = func(repo typedef.Repository) []typedef.Repository {
		return []typedef.Repository{
			{Name: "alpha", URL: "github.com/acme/alpha"},
			{Name: "beta", URL: "github.com/acme/beta"},
		}
	}

	jobIDs, err := exec.ExecuteJob("github.com/acme")
	require.NoError(t, err)
	require.Len(t, jobIDs, 2)

	keys := map[string]bool{}
	for _, id := range jobIDs {
		var repoKey string
		require.NoError(t, testDB.QueryRow("SELECT repo_key FROM executions WHERE id = ?", id).Scan(&repoKey))
		keys[repoKey] = true
	}
	assert.True(t, keys["github.com/acme/alpha"])
	assert.True(t, keys["github.com/acme/beta"])
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/executor/ -run 'TestExecuteJobWritesRepoKey|TestExecuteJobExpandsOrgIntoMultipleJobs'`
Expected: FAIL — `undefined: expandRepos` 编译错误（第一步对旧用例的适配也一起在这一轮生效）。

- [ ] **Step 4: 重写 `ExecuteJob`**（`internal/executor/executor.go`）

```go
import "errors"

// ErrRepositoryNotFound 表示配置中找不到匹配该身份键的仓库条目。
var ErrRepositoryNotFound = errors.New("repository not found in configuration")

// expandRepos 是把 user/org 条目展开为具体仓库的 seam：生产用 repository.Expand，
// 测试注入 fake，避免真实 GitHub 调用。
var expandRepos = repository.Expand

// ExecuteJob 按仓库身份键（规范化 URL）在配置中定位条目并执行。type=repo 产生
// 一条 execution 并返回单元素 jobID；type=user/org 先在任务内展开为具体仓库，
// 每个具体仓库独立执行（各自 jobID / execution / 日志流 / 可取消）。
func (e *Executor) ExecuteJob(repoKey string) ([]string, error) {
	var repo typedef.Repository
	found := false
	for _, r := range e.cfg.Repository {
		if r.Matches(repoKey) {
			repo = r
			found = true
			break
		}
	}
	if !found {
		return nil, ErrRepositoryNotFound
	}

	jobIDs := make([]string, 0, 1)
	for _, concrete := range expandRepos(repo) {
		jobID, err := e.launchJob(concrete)
		if err != nil {
			return nil, err
		}
		jobIDs = append(jobIDs, jobID)
	}
	return jobIDs, nil
}

// launchJob 为单个具体仓库创建 execution 记录并异步执行。
func (e *Executor) launchJob(job typedef.Repository) (string, error) {
	// Generate job ID
	jobID := uuid.New().String()
	startTime := time.Now()

	// Create execution record: job_name 保存展示名快照，repo_key 是身份键。
	_, err := e.db.Exec(`
		INSERT INTO executions (id, job_name, repo_key, start_time, status)
		VALUES (?, ?, ?, ?, ?)
	`, jobID, job.Name, job.Key(), startTime, string(StatusPending))
	if err != nil {
		return "", fmt.Errorf("failed to create execution record: %w", err)
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Store job context
	e.mu.Lock()
	e.runningJobs[jobID] = &JobContext{
		Ctx:        ctx,
		CancelFunc: cancel,
	}
	e.mu.Unlock()

	// Update status to running
	e.updateJobStatus(jobID, string(StatusRunning), "")

	// Execute async
	go e.executeAsync(ctx, jobID, job)

	return jobID, nil
}
```

- [ ] **Step 5: 运行全部 executor 测试确认通过**

Run: `go test ./internal/executor/`
Expected: PASS。（`TestExecuteJobRunsConfiguredComponents` 会真实 clone 一个不存在的仓库后失败，日志行断言仍应通过；网络受限时允许重跑一次。）

- [ ] **Step 6: 提交**

```bash
git add internal/executor/executor.go internal/executor/executor_test.go
git commit -m "feat(executor): ExecuteJob by repo key, expand org/user into per-repo jobs"
```

---

### Task 5: API —— CreateJob 用 repository_key/job_ids + GetJobs 按 repo_key 关联与过滤

**Files:**
- Modify: `internal/server/types.go`
- Modify: `internal/server/api.go:30-74`（CreateJob）、`:105-213`（GetJobs）
- Modify: `internal/server/api_test.go`（既有用例字段改名 + 新增 URL 过滤、URL 解析断言）

**Interfaces:**
- Consumes: `executor.ExecuteJob(repoKey) ([]string, error)` + `executor.ErrRepositoryNotFound`（Task 4）、`typedef.NormalizeURL`/`Matches`（Task 1）、`repo_key` 列（Task 2）
- Produces:
  - `CreateJobRequest{ RepositoryKey string json:"repository_key" binding:"required" }`
  - `CreateJobResponse{ JobIDs []string json:"job_ids"; Status string }`
  - `GetJobs`：SELECT 含 `repo_key`；`repository` 过滤为 `(job_name LIKE ? OR repo_key LIKE ?)`，第二个词先 `NormalizeURL`；URL 关联按 `repo.Matches(repoKey)`

- [ ] **Step 1: 改 types.go**

```go
type CreateJobRequest struct {
	RepositoryKey string `json:"repository_key" binding:"required"`
}

type CreateJobResponse struct {
	JobIDs []string `json:"job_ids"`
	Status string   `json:"status"`
}
```

- [ ] **Step 2: 改 CreateJob handler**（`internal/server/api.go`）

```go
func (a *API) CreateJob(c *gin.Context) {
	var req CreateJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Invalid request: " + err.Error(),
		})
		return
	}

	jobIDs, err := a.executor.ExecuteJob(req.RepositoryKey)
	if err != nil {
		if errors.Is(err, executor.ErrRepositoryNotFound) {
			c.JSON(http.StatusNotFound, Response{
				Code:    404,
				Message: "Repository not found in configuration",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to execute job: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: CreateJobResponse{
			JobIDs: jobIDs,
			Status: string(executor.StatusRunning),
		},
	})
}
```

`internal/server/api.go` import 增加 `errors`。

- [ ] **Step 3: 改 GetJobs**（`internal/server/api.go`）

查询串抽成常量并加入 `repo_key`，过滤与关联改身份键：

```go
const jobSelect = "SELECT id, job_name, repo_key, start_time, end_time, status, error_message FROM executions"

// Build query
query := jobSelect + " WHERE 1=1"
args := []interface{}{}

if status != "" && status != "all" {
	query += " AND status = ?"
	args = append(args, status)
}

if repository != "" {
	// name 模糊匹配原始输入；URL 搜索词先规范化，命中规范化后的 repo_key。
	query += " AND (job_name LIKE ? ESCAPE '\\' OR repo_key LIKE ? ESCAPE '\\')"
	args = append(args, "%"+escapeLike(repository)+"%", "%"+escapeLike(typedef.NormalizeURL(repository))+"%")
}

// Get total count
countQuery := "SELECT COUNT(*) FROM executions" + query[len(jobSelect):]
```

扫描与关联段改为：

```go
	var repoKey string
	err := rows.Scan(&job.ID, &job.Name, &repoKey, &startTime, &endTime, &job.Status, &errorMessage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to scan job: " + err.Error(),
		})
		return
	}

	job.StartTime = &startTime
	job.EndTime = endTime
	if errorMessage != nil {
		job.ErrorMessage = *errorMessage
	}

	// Resolve URL from config by identity key; unmatched (renamed/deleted/old
	// empty-key rows) keeps the job_name snapshot and empty URL.
	for _, repo := range a.config.Repository {
		if repo.Matches(repoKey) {
			job.URL = repo.URL
			break
		}
	}
```

（`GetJobs` 中旧的 `argPos` 变量是死代码，一并删掉。）

- [ ] **Step 4: 更新 api_test.go**

（import 增加 `net/url`，供新的 URL 过滤用例使用 `url.QueryEscape`。）

- `TestCreateJob`：请求体 `"repository": "non-existent-repo"` → `"repository_key": "github.com/nope/repo"`（仍 404）；`"missing_repository_field"` 用空 body（仍 400）；新增成功用例：

```go
t.Run("valid_repository_key", func(t *testing.T) {
	body, _ := json.Marshal(map[string]string{
		"repository_key": "https://github.com/test/repo",
	})
	req, _ := http.NewRequest("POST", "/api/jobs", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code)
	var response struct {
		Code int `json:"code"`
		Data struct {
			JobIDs []string `json:"job_ids"`
			Status string   `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
	assert.Equal(t, 200, response.Code)
	require.Len(t, response.Data.JobIDs, 1)
	assert.NotEmpty(t, response.Data.JobIDs[0])
	assert.Equal(t, "running", response.Data.Status)
})
```

- `TestGetJobs` 的三个 INSERT 改为带 `repo_key`（`"github.com/test/repo"`），并在 `list all jobs` 的 checkResponse 里加 URL 断言：

```go
	// URL 解析：repo_key 命中配置条目 → url 被填上（三行同键，全部命中）
	for _, j := range response.Data.Jobs {
		assert.Equal(t, "github.com/test/repo", j.URL)
	}
```

- 追加 URL 模糊过滤用例（filter 列表里新增一行）：

```go
{
	name:           "filter by full URL with scheme and trailing slash",
	queryParams:    "?repository=" + url.QueryEscape("https://github.com/test/repo/"),
	expectedStatus: 200,
	checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
		var response struct {
			Code    int `json:"code"`
			Data    struct {
				Jobs  []struct{ Name string `json:"name"` } `json:"jobs"`
				Total int64                               `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
		assert.Equal(t, int64(3), response.Data.Total, "normalized URL must match repo_key despite scheme/slash")
	},
},
```

- **注意**：`TestGetJobsRepositoryEscaping` 的行插入不带 `repo_key`（默认 `''`），过滤仍只命中 `job_name`，断言不变；无需改动。

- [ ] **Step 5: 运行 server 测试确认通过**

Run: `go test ./internal/server/ -run 'TestCreateJob|TestGetJobs'`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/server/types.go internal/server/api.go internal/server/api_test.go
git commit -m "feat(server): CreateJob takes repository_key and returns job_ids; GetJobs matches name or URL"
```

---

### Task 6: API —— GetRepositories 按 repo_key 聚合 + 仓库 CRUD 按身份键

**Files:**
- Modify: `internal/server/api.go`（GetRepositories `:307-407`、CreateRepository `:409-453`、UpdateRepository `:455-536`、DeleteRepository `:538-570`）
- Modify: `internal/server/repository_test.go`（既有用例路径参数由 name 改身份键 + 新增 URL 判重/空 URL/org 合成/org 前缀聚合用例）

**Interfaces:**
- Consumes: `typedef.Repository.Key()` / `.Matches()` / `.EffectiveURL()`（Task 1）、`repo_key` 列（Task 2）
- Produces:
  - `func lookupStats(stats map[string]runStats, repo typedef.Repository) runStats` — type=repo/user 直接取 `stats[repo.Key()]`；type=org/user 对 `entry.Key()+"/"` 路径前缀命中的成员求和，`last_run` 取 max
  - `GetRepositories`：`GROUP BY repo_key`；搜索 `name` 或 `EffectiveURL()` 任一 `Contains`（大小写折叠）
  - `CreateRepository`：允许 name 重复；URL 判重 409；空身份键 400；user/org 空 URL 自动填合成 URL
  - `UpdateRepository` / `DeleteRepository`：`:id` 用 `repo.Matches(id)` 定位；更新后与其他仓库 URL 冲突 409（不与自身比较）

- [ ] **Step 1: 写失败测试**（`internal/server/repository_test.go` 改动 + 新增）

**既有用例改造：**

- `TestGetRepositories`：
  - cfg 的 `repo-a` 的 INSERT 改为带 `repo_key`：
    ```go
    testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
        "e1", "repo-a", "github.com/a/a", now, now.Add(time.Minute), "completed", "")
    testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
        "e2", "repo-a", "github.com/a/a", now.Add(2*time.Minute), now.Add(3*time.Minute), "failed", "boom")
    ```
  - 追加 URL 搜索用例：
    ```go
    t.Run("search filters by URL", func(t *testing.T) {
        d := getList("?search=github.com/b")
        assert.Equal(t, 1, d.Total)
        assert.Equal(t, "repo-b", d.Repositories[0].Name)
    })
    ```

- `TestCreateRepository`：`duplicate_name` 改为 `duplicate_url`（同名不同 URL → 200）；`empty_name` 用例**保持不变**（name 仍必填 400，CreateRepository 保留 name 校验）；空 URL/org 无 orgName 由新增的 `TestCreateRepositoryEmptyURLRejected` 覆盖；新增 `org_synthesizes_url`。

- `TestUpdateRepository`：`PUT /api/repositories/update-me` → `PUT /api/repositories/github.com/old/url`。

- `TestDeleteRepository`：`DELETE /api/repositories/delete-me` → `DELETE /api/repositories/github.com/delete/me`。

**新增用例：**

```go
func TestCreateRepositoryDuplicateURL(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "existing-repo", URL: "github.com/existing/repo"},
		},
	}
	s := server.NewRepoTestServer(cfg, testDB)

	// 同一 URL 不同 name → 409（身份以 URL 为准，name 可重复）。
	body, _ := json.Marshal(map[string]interface{}{
		"name": "another-name",
		"url":  "https://github.com/existing/repo.git",
	})
	req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)

	assert.Equal(t, 409, resp.Code)
	assert.Equal(t, 409, repoResponseCode(t, resp))
}

func TestCreateRepositoryNameMayRepeat(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "repo", URL: "github.com/one/repo"},
		},
	}
	s := server.NewRepoTestServer(cfg, testDB)

	// 相同 name 不同 URL → 200（name 不再是唯一约束）。
	body, _ := json.Marshal(map[string]interface{}{
		"name": "repo",
		"url":  "github.com/two/repo",
	})
	req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code)
}

func TestCreateRepositoryOrgSynthesizesURL(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	s := server.NewRepoTestServer(&config.Config{}, testDB)

	body, _ := json.Marshal(map[string]interface{}{
		"name":    "acme",
		"type":    "org",
		"orgName": "acme",
	})
	req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code)
	var response struct {
		Code int                `json:"code"`
		Data typedef.Repository `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
	assert.Equal(t, 200, response.Code)
	assert.Equal(t, "https://github.com/acme", response.Data.URL)
}

func TestCreateRepositoryEmptyURLRejected(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	s := server.NewRepoTestServer(&config.Config{}, testDB)

	// type=repo 且无 URL；type=org 但无 orgName → 都 400。
	for _, body := range []map[string]interface{}{
		{"name": "orphan", "type": "repo"},
		{"name": "nobody", "type": "org"},
	} {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(b))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)
		assert.Equal(t, 400, resp.Code, "body %v", body)
	}
}

func TestUpdateRepositoryURLCollision(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "a", URL: "github.com/a/a"},
			{Name: "b", URL: "github.com/b/b"},
		},
	}
	s := server.NewRepoTestServer(cfg, testDB)

	// 把 a 的 URL 改成 b 的 → 409（与其他仓库冲突，不与自身比较）。
	body, _ := json.Marshal(map[string]interface{}{
		"url": "github.com/b/b",
	})
	req, _ := http.NewRequest("PUT", "/api/repositories/github.com/a/a", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)

	assert.Equal(t, 409, resp.Code)
}

func TestGetRepositoriesOrgPrefixAggregation(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "acme", URL: "https://github.com/acme", Type: typedef.TypeOrg, OrgName: "acme"},
			{Name: "solo", URL: "github.com/solo/app"},
		},
	}

	now := time.Now()
	// 成员仓库的 executions 各记一条。
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"a1", "alpha", "github.com/acme/alpha", now.Add(-2*time.Minute), now.Add(-1*time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"b1", "beta", "github.com/acme/beta", now.Add(-4*time.Minute), now.Add(-3*time.Minute), "failed", "boom")
	// 路径边界：github.com/acme2/x 不能被 acme 前缀吞掉。
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"c1", "other", "github.com/acme2/other", now.Add(-6*time.Minute), now.Add(-5*time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"d1", "solo", "github.com/solo/app", now.Add(-8*time.Minute), now.Add(-7*time.Minute), "completed", "")

	s := server.NewRepoTestServer(cfg, testDB)

	type repoView struct {
		Name        string     `json:"Name"`
		LastRunTime *time.Time `json:"last_run_time"`
		TotalRuns   int64      `json:"total_runs"`
		SuccessRuns int64      `json:"success_runs"`
		FailedRuns  int64      `json:"failed_runs"`
	}
	req, _ := http.NewRequest("GET", "/api/repositories", nil)
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)
	var response struct {
		Code int `json:"code"`
		Data struct {
			Repositories []repoView `json:"repositories"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
	assert.Equal(t, 200, response.Code)

	byName := map[string]repoView{}
	for _, r := range response.Data.Repositories {
		byName[r.Name] = r
	}

	acme := byName["acme"]
	assert.Equal(t, int64(2), acme.TotalRuns, "org aggregates its member repos only")
	assert.Equal(t, int64(1), acme.SuccessRuns)
	assert.Equal(t, int64(1), acme.FailedRuns)
	require.NotNil(t, acme.LastRunTime)
	assert.WithinDuration(t, now.Add(-1*time.Minute), *acme.LastRunTime, time.Second, "last_run must be the max of members")

	solo := byName["solo"]
	assert.Equal(t, int64(1), solo.TotalRuns)
}
```

- [ ] **Step 2: 运行 repository 测试确认失败**

Run: `go test ./internal/server/ -run 'TestCreateRepository|TestUpdateRepository|TestDeleteRepository|TestGetRepositories'`
Expected: FAIL — 用例期望的行为（409 而非 name 判重、URL 聚合等）当前不满足。

- [ ] **Step 3: 改 GetRepositories**（`internal/server/api.go`）

统计聚合与关联改为按身份键；新增包级 `runStats` 与 `lookupStats`：

```go
// runStats 是单仓库/单组织的聚合运行统计。
type runStats struct {
	LastRun *time.Time
	Total   int64
	Success int64
	Failed  int64
}

// lookupStats 返回配置条目的运行统计。type=repo 直接取自身键；type=org/user
// 对「路径边界前缀」（entry.Key()+"/"）命中的成员求和，last_run 取成员最大值。
// 前缀以 "/" 结尾，避免 github.com/acme 误吞 github.com/acme2/x。
func lookupStats(stats map[string]runStats, repo typedef.Repository) runStats {
	key := repo.Key()
	if key == "" {
		return runStats{}
	}
	switch repo.GetType() {
	case typedef.TypeOrg, typedef.TypeUser:
		prefix := key + "/"
		var sum runStats
		for k, s := range stats {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			sum.Total += s.Total
			sum.Success += s.Success
			sum.Failed += s.Failed
			if s.LastRun != nil && (sum.LastRun == nil || s.LastRun.After(*sum.LastRun)) {
				t := *s.LastRun
				sum.LastRun = &t
			}
		}
		return sum
	default:
		return stats[key]
	}
}
```

`GetRepositories` 内：
- 聚合 SQL 分组键 `job_name` → `repo_key`（`SELECT repo_key, start_time AS last_run, ... FROM executions GROUP BY repo_key HAVING start_time = MAX(start_time)`），scan 的变量名 `name` → `key`，`stats[key] = a`。
- `filtered` 的过滤改为 name 或 URL：
  ```go
  for _, repo := range a.config.Repository {
      if search != "" {
          inName := strings.Contains(strings.ToLower(repo.Name), strings.ToLower(search))
          inURL := strings.Contains(strings.ToLower(repo.EffectiveURL()), strings.ToLower(search))
          if !inName && !inURL {
              continue
          }
      }
      filtered = append(filtered, repo)
  }
  ```
- 统计关联 `s := stats[repo.Name]` → `s := lookupStats(stats, repo)`。
- 原函数内的 `type agg struct {...}` 删除，改用包级 `runStats`。

- [ ] **Step 4: 改 CreateRepository**

```go
	if repo.Name == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Repository name is required",
		})
		return
	}
	// user/org 空 URL → 填合成 URL；随后统一判身份键非空。
	repo.URL = repo.EffectiveURL()
	if repo.Key() == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Repository needs a non-empty URL (or orgName for user/org type)",
		})
		return
	}

	// 判重按身份键（URL），name 允许重复。
	for _, existing := range a.config.Repository {
		if existing.Key() == repo.Key() {
			c.JSON(http.StatusConflict, Response{
				Code:    409,
				Message: "Repository with URL '" + repo.Key() + "' already exists",
			})
			return
		}
	}
```

（`repo.Name == ""` 的 400 保留；`empty_name` 用例改造后不再依赖它，但保留对 UI 的友好校验。）

- [ ] **Step 5: 改 UpdateRepository**

定位与冲突检查：

```go
	// Locate the existing repository by identity key (URL).
	idx := -1
	for i, existing := range a.config.Repository {
		if existing.Matches(id) {
			idx = i
			break
		}
	}
	if idx == -1 {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Message: "Repository not found",
		})
		return
	}
```

merge 之后、写回之前：

```go
	// user/org 空 URL → 填合成 URL；空身份键拒绝；与其他仓库 URL 冲突拒绝。
	updated.URL = updated.EffectiveURL()
	if updated.Key() == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Repository needs a non-empty URL (or orgName for user/org type)",
		})
		return
	}
	for i, other := range a.config.Repository {
		if i != idx && other.Key() == updated.Key() {
			c.JSON(http.StatusConflict, Response{
				Code:    409,
				Message: "Repository with URL '" + updated.Key() + "' already exists",
			})
			return
		}
	}
```

- [ ] **Step 6: 改 DeleteRepository**

```go
	idx := -1
	for i, existing := range a.config.Repository {
		if existing.Matches(id) {
			idx = i
			break
		}
	}
```

- [ ] **Step 7: 运行全部 server 测试确认通过**

Run: `go test ./internal/server/`
Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add internal/server/api.go internal/server/repository_test.go
git commit -m "feat(server): repo_key aggregation + org prefix stats; CRUD keyed by URL identity"
```

---

### Task 7: 前端 —— 按钮/请求按 URL 身份键，name 可编辑

**Files:**
- Modify: `web/static/js/main.js`
- Modify: `web/templates/index.html`

**Interfaces:**
- Consumes: `POST /api/jobs {repository_key}` → `{job_ids: []string}`；`PUT/DELETE /api/repositories/:id`（:id = URL 编码的身份键）；`GET /api/repositories` 返回 `RepositoryOverview`（内嵌 `typedef.Repository`，字段 `URL`、`Name`、`Type`、`OrgName`…）
- Produces: 无（纯前端消费）

- [ ] **Step 1: 加 `repoKey` 助手与占位文案**（`main.js`）

文件顶部附近加：

```js
// repoKey 返回仓库身份键。后端以规范化 URL 为唯一身份，前端不做规范化。
const repoKey = (r) => r.URL || '';
```

- 任务页过滤占位（`jobsToolbar`，约第 122 行）：`placeholder="Filter by repository name…"` → `"Filter by repository name or URL…"`。
- 仓库页搜索占位（`renderRepositories`，约第 428 行）：`placeholder="Filter by repository name…"` → `"Filter by name or URL…"`。

- [ ] **Step 2: 改 `runRepo`**（约第 240 行；签名带 name 供日志弹窗标题使用）

```js
async function runRepo(key, name, btn) {
    if (!key) return;
    btn.disabled = true;
    try {
        const data = await api('/api/jobs', { method: 'POST', body: JSON.stringify({ repository_key: key }) });
        const jobIDs = (data && data.job_ids) || [];
        if (jobIDs.length === 1) {
            toast('Job started (' + jobIDs[0].slice(0, 8) + '…)');
            openLogModal(jobIDs[0], name);
        } else {
            toast('Started ' + jobIDs.length + ' jobs (org expansion)');
        }
        renderRepositories();
    } catch (e) {
        toast('Failed to start job: ' + e.message, true);
        btn.disabled = false;
    }
}
```

- [ ] **Step 3: 改仓库表格按钮 data-* 与事件绑定**（`renderRepositories`）

行内按钮（约第 418-420 行）`data-name="${esc(r.Name)}"` → `data-key="${esc(repoKey(r))}"`；事件绑定（约第 465-470 行）：

```js
$$('.btn-run-repo').forEach(b => b.addEventListener('click', () => {
    const r = repos.find(x => repoKey(x) === b.dataset.key);
    runRepo(b.dataset.key, r ? r.Name : '', b);
}));
$$('.btn-edit-repo').forEach(b => b.addEventListener('click', () => {
    const r = repos.find(x => repoKey(x) === b.dataset.key);
    if (r) openRepoForm(r);
}));
$$('.btn-del-repo').forEach(b => b.addEventListener('click', () => deleteRepo(b.dataset.key)));
```

- [ ] **Step 4: 改 `deleteRepo`**（约第 378 行）

```js
async function deleteRepo(key) {
    if (!confirm('Delete repository?')) return;
    try {
        await api('/api/repositories/' + encodeURIComponent(key), { method: 'DELETE' });
        toast('Repository deleted');
        renderRepositories();
    } catch (e) {
        toast('Failed to delete repository: ' + e.message, true);
    }
}
```

- [ ] **Step 5: 改编辑表单：存身份键、name 可改**（`main.js` + `index.html`）

`index.html` 第 43 行：`<input type="hidden" id="repo-original-name">` → `<input type="hidden" id="repo-original-key">`。

`openRepoForm`（约第 308-310 行）：

```js
$('#repo-original-key').value = repo ? repoKey(repo) : '';
$('#repo-name').value = repo ? repo.Name : '';
$('#repo-name').disabled = false; // name 仅展示/查询，可重复可改名
```

`saveRepo`（约第 342、365 行）：

```js
const originalKey = $('#repo-original-key').value;
...
if (originalKey) {
    await api('/api/repositories/' + encodeURIComponent(originalKey), { method: 'PUT', body: JSON.stringify(repo) });
    toast('Repository updated');
} else {
    await api('/api/repositories', { method: 'POST', body: JSON.stringify(repo) });
    toast('Repository added');
}
```

（`repo` 对象的构造不变；`URL` 字段为 `$('#repo-url').value.trim()`，后端负责规范化与 user/org 合成。）

- [ ] **Step 6: 手工/浏览器验证**

Run: `go build -o gitrieve main.go && ./gitrieve server -c config.yaml`（或项目既有启动方式），浏览器打开：
1. 仓库列表按钮执行单个仓库 → 打开日志弹窗；任务页出现该仓库的 execution。
2. 添加一个 `type: org, orgName: <你的org>` 的条目 → 保存后 URL 自动显示 `https://github.com/<orgName>`；点 Execute → toast「Started N jobs」、任务列表出现 N 条（各为具体仓库 URL）。
3. 编辑仓库：name 输入框可改；改 URL 后保存 → 任务页旧历史保留、新任务关联新 URL；把 URL 改成另一仓库的 → 后端 409。
4. 任务页过滤输入完整带协议的 URL → 能搜到；仓库页搜索 URL 片段 → 能搜到。

- [ ] **Step 7: 提交**

```bash
git add web/static/js/main.js web/templates/index.html
git commit -m "feat(ui): key repo actions by URL identity; make name editable"
```

---

## Self-Review

**Spec coverage 核对：**

- §4 身份键/规范化 → Task 1（含表驱动测试覆盖协议/大小写/尾斜杠/.git/www/#/多主机/合成/空输入）。
- §4 空 URL 启动校验 → Task 1 Step 5-7。
- §5 schema + 迁移 → Task 2（新增列、`Migrate`、幂等、无回填、server 接线、失败即退出）。
- §6 executor → Task 4（`Matches` 查找、`Expand` 展开、INSERT 写 `repo_key`、`job_name` 写展示名、返回多 jobID）。
- §7.1 CreateJob → Task 5。
- §7.2 GetJobs → Task 5（name+URL 模糊、URL 搜索词先规范化、`repo.Matches(repoKey)` 关联、关联不到用快照）。
- §7.3 GetRepositories → Task 6（`GROUP BY repo_key` 保留 HAVING 写法；org 前缀求和 + 路径边界；搜索 name 或 EffectiveURL）。
- §7.4 CRUD → Task 6（`:id`=身份键、name 重复、URL 判重、空 URL 400、user/org 合成写回、更新冲突 409 不与自身比较）。
- §7.5 破坏性变更清单 → Task 5/6/7。
- §8 前端 → Task 7（`repoKey=r.URL`、`repository_key`/`job_ids`、name 可编辑、占位文案）。
- §10 测试 → 各任务内；executor 的 org 多 job 用 `expandRepos` seam（Task 4 Step 2），`repository.Expand` 用 `newGithubClient` seam（Task 3 Step 1）；API 层 org 多值经 executor 单测覆盖 + `job_ids` 形状断言。
- §9 明确不做 → 计划未触碰 `cmd/daemon`、不做前端规范化、不做导入导出。

**Placeholder scan：** 无 TBD/占位；每个代码步骤均含完整实现。

**Type consistency：**
- `repoKey(r)` 前端返回 `r.URL`，与后端 `Matches` 规范化判等一致（§8）。
- `expandRepos`（executor）签名 `func(typedef.Repository) []typedef.Repository` 与 `repository.Expand` 一致。
- `newGithubClient`（repository）签名 `func() (repoLister, error)` 与 `github.New` 兼容（`*github.Client` 实现 `GetRepos(name, accountType string) ([]string, error)`）。
- `runStats`/`lookupStats` 在 Task 6 定义并仅在该任务使用；`jobSelect` 常量在 Task 5 定义并同步用于 countQuery 切片。
- 既有 server 测试（`repository_test.go`/`api_test.go`）路径参数全部由 name 改为身份键，Task 5/6 内显式列出。

**执行前修正（SDD 预检发现并已改入本计划）：**
- Task 1：`ui.ErrorfExit` 走 `os.Exit(1)` 而非 panic——原 `require.Panics(Init)` 用例会杀测试进程；改为可测试的 `validateIdentity(cfg) error` + `Init` 内 `ui.ErrorfExit`，测试针对 `validateIdentity` 断言 error。
- Task 5：`api_test.go` 需新增 `net/url` import；URL 解析断言改为遍历全部 jobs（三条 INSERT 的 start_time 相同，取 [0] 依赖插入顺序不可靠）。
- Task 6：`empty_name` 用例保留不变（name 仍必填 400）；空 URL 由新增 `TestCreateRepositoryEmptyURLRejected` 覆盖。
- Task 7：`runRepo(key, name, btn)` 增加 name 参数供日志弹窗标题使用（按钮 textContent 是 "Execute"，不能当仓库名）。
