# Web UI Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the `end_time` NULL scan bug, add fuzzy repo filters + repo stats/pagination/execute button to the web UI, unify all job logging through `ui` (so the log modal shows detailed progress), and mount the `.gitrieve` cache in docker-compose.

**Architecture:** Extend the existing gin server endpoints (`GET /api/jobs`, `GET /api/repositories`) rather than adding new ones. Add an execution-ID sink to `internal/ui` that forwards `ui.Printf`/`ui.Errorf` to the DB logger, bound per goroutine by the executor. Rewrite the two SPA views in `web/static/js/main.js`. Add one volume to `docker-compose.yml`.

**Tech Stack:** Go 1.23, gin, `modernc.org/sqlite`, `github.com/robfig/cron/v3` (already a transitive dep, promoted to direct), vanilla JS SPA + CSS.

## Global Constraints

- **TDD for backend:** each task starts with a failing test, then the minimal implementation, then green.
- **Never regress the deletion-safe sync invariant** in `internal/repository/repository.go` — this plan does not touch that file.
- **CLI/daemon behavior must be unchanged:** they do not call `ui.SetSink` or `ui.Bind`, so `ui.Printf/Errorf` must be a no-op for the DB when no sink is set and no binding exists.
- **Repo JSON field names are PascalCase.** `typedef.Repository` uses `yaml:` tags only, so `encoding/json` emits `Name`, `URL`, `Cron`, `Storage`, `UseCache`, `AllBranches`, `DownloadReleases`, etc. New overview fields are snake_case (`last_run_time`, `next_run_time`, `total_runs`, `success_runs`, `failed_runs`). The frontend reads repo fields as `r.Name`, `r.URL`, … and the new fields as `r.last_run_time`, …
- **Commit after each task** with the message shown in that task.
- Frontend has no JS test harness: verify with `node --check web/static/js/main.js` plus the manual steps listed, and `go build ./...` to confirm the embedded assets still compile.

---

### Task 1: Fix `end_time` NULL scan bug in `GetJobs`

**Files:**
- Modify: `internal/server/api.go` (scan block, ~lines 160-180)
- Test: `internal/server/api_test.go` (`TestGetJobs`)

**Interfaces:**
- Consumes: existing `server.Job` struct (`StartTime *time.Time`, `EndTime *time.Time`).
- Produces: none new. `GetJobs` no longer errors when a row's `end_time` is SQL NULL.

- [ ] **Step 1: Make the regression test fail — insert a real NULL `end_time`**

In `internal/server/api_test.go` `TestGetJobs`, the setup inserts `job-2` as `"running"` with `time.Time{}` (which is NOT SQL NULL). Change that insert to omit `end_time` entirely so the column is truly NULL:

```go
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"job-2", "test-repo", now, "running")
```

Then extend the existing `"filter by status"` table entry so it asserts the NULL comes through as JSON `null`:

```go
		{
			name:           "filter by status",
			queryParams:    "?status=running",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var response struct {
					Code    int `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Jobs []struct {
							ID          string     `json:"id"`
							Name        string     `json:"name"`
							URL         string     `json:"url"`
							Status      string     `json:"status"`
							StartTime   *time.Time `json:"start_time"`
							EndTime     *time.Time `json:"end_time"`
							ErrorMessage string    `json:"error_message"`
						} `json:"jobs"`
						Total int64 `json:"total"`
						Page  int   `json:"page"`
						Limit int   `json:"limit"`
					} `json:"data"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &response)
				require.NoError(t, err)
				assert.Equal(t, 200, response.Code)
				assert.Equal(t, int64(1), response.Data.Total)
				assert.Len(t, response.Data.Jobs, 1)
				assert.Equal(t, "running", response.Data.Jobs[0].Status)
				assert.Nil(t, response.Data.Jobs[0].EndTime, "end_time should be null for a running job")
			},
		},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestGetJobs`
Expected: FAIL — `sql: Scan error on column index 3, name "end_time": unsupported Scan, storing driver.Value type <nil> into type *time.Time`

- [ ] **Step 3: Fix the scan to use `*time.Time`**

In `internal/server/api.go`, replace the scan block inside `GetJobs`:

```go
	for rows.Next() {
		var job Job
		var startTime, endTime time.Time
		var endTimePtr *time.Time

		err := rows.Scan(&job.ID, &job.Name, &startTime, &endTime, &job.Status, &job.ErrorMessage)
		if err != nil {
			c.JSON(http.StatusInternalServerError, Response{
				Code:    500,
				Message: "Failed to scan job: " + err.Error(),
			})
			return
		}

		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		job.StartTime = &startTime
		job.EndTime = endTimePtr
```

with:

```go
	for rows.Next() {
		var job Job
		var startTime time.Time
		var endTime *time.Time

		err := rows.Scan(&job.ID, &job.Name, &startTime, &endTime, &job.Status, &job.ErrorMessage)
		if err != nil {
			c.JSON(http.StatusInternalServerError, Response{
				Code:    500,
				Message: "Failed to scan job: " + err.Error(),
			})
			return
		}

		job.StartTime = &startTime
		job.EndTime = endTime
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestGetJobs`
Expected: PASS (all subtests, including "filter by status")

- [ ] **Step 5: Commit**

```bash
git add internal/server/api.go internal/server/api_test.go
git commit -m "fix: scan NULL end_time into *time.Time in GetJobs
```

---

### Task 2: Fuzzy repository-name filter on `GET /api/jobs`

**Files:**
- Create: `internal/server/helpers.go` (`escapeLike`)
- Modify: `internal/server/api.go` (query builder in `GetJobs`, ~lines 127-132)
- Test: `internal/server/api_test.go` (`TestGetJobs`), `internal/server/helpers_test.go`

**Interfaces:**
- Consumes: existing `GetJobs` query builder.
- Produces: `escapeLike(s string) string` in package `server` (used by later tasks? No — the repos `search` filter uses in-memory `strings.Contains`, which needs no escaping. `escapeLike` is only for the jobs SQL LIKE).

- [ ] **Step 1: Write the failing fuzzy-match test**

In `internal/server/api_test.go` `TestGetJobs`, add a new table entry (all existing jobs have `job_name = "test-repo"`):

```go
		{
			name:           "filter by repository fuzzy (partial name)",
			queryParams:    "?repository=test",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var response struct {
					Code    int `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Jobs  []struct{ Name string `json:"name"` } `json:"jobs"`
						Total int64                               `json:"total"`
					} `json:"data"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &response)
				require.NoError(t, err)
				assert.Equal(t, 200, response.Code)
				assert.Equal(t, int64(3), response.Data.Total, "partial name 'test' should match job_name 'test-repo'")
				assert.Len(t, response.Data.Jobs, 3)
			},
		},
		{
			name:           "filter by repository no match",
			queryParams:    "?repository=nope",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var response struct {
					Code    int `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Jobs  []struct{ Name string `json:"name"` } `json:"jobs"`
						Total int64                               `json:"total"`
					} `json:"data"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &response)
				require.NoError(t, err)
				assert.Equal(t, 200, response.Code)
				assert.Equal(t, int64(0), response.Data.Total)
				assert.Len(t, response.Data.Jobs, 0)
			},
		},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestGetJobs`
Expected: FAIL — `filter by repository fuzzy` gets `Total 0` (exact match `job_name = 'test'` matches nothing).

- [ ] **Step 3: Implement `escapeLike` helper**

Create `internal/server/helpers.go`:

```go
package server

import "strings"

// escapeLike escapes LIKE metacharacters so user input is matched literally
// when used with ESCAPE '\'.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
```

- [ ] **Step 4: Switch the filter to a fuzzy LIKE**

In `internal/server/api.go` `GetJobs`, replace:

```go
	if repository != "" {
		query += " AND job_name = ?"
		args = append(args, repository)
		argPos++
	}
```

with:

```go
	if repository != "" {
		query += " AND job_name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(repository)+"%")
		argPos++
	}
```

- [ ] **Step 5: Add `escapeLike` unit test**

Create `internal/server/helpers_test.go`:

```go
package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, "repo", escapeLike("repo"))
	assert.Equal(t, "100\\%", escapeLike("100%"))
	assert.Equal(t, "a\\_b", escapeLike("a_b"))
	assert.Equal(t, "a\\\\b", escapeLike(`a\b`))
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/server/ -run 'TestGetJobs|TestEscapeLike'`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/server/api.go internal/server/helpers.go internal/server/helpers_test.go internal/server/api_test.go
git commit -m "feat: fuzzy repository-name filter on /api/jobs
```

---

### Task 3: Enriched, paginated, searchable `GET /api/repositories`

**Files:**
- Modify: `internal/server/types.go` (add `RepositoryOverview`, `ListRepositoriesResponse`)
- Modify: `internal/server/helpers.go` (add `nextRunTime`)
- Modify: `internal/server/api.go` (rewrite `GetRepositories`)
- Test: `internal/server/repository_test.go`, `internal/server/helpers_test.go`
- Run: `go mod tidy` (promote `github.com/robfig/cron/v3` to a direct dependency)

**Interfaces:**
- Consumes: `time`, `github.com/robfig/cron/v3`, existing `typedef.Repository`, existing `a.db`, `a.config.Repository`.
- Produces:
  - `nextRunTime(cronExpr string, now time.Time) *time.Time` in package `server`.
  - `server.RepositoryOverview` — embeds `typedef.Repository` (fields marshal as PascalCase: `Name`, `URL`, `Cron`, `Storage`, …) plus `LastRunTime *time.Time \`json:"last_run_time"\``, `NextRunTime *time.Time \`json:"next_run_time"\``, `TotalRuns/SuccessRuns/FailedRuns int64` with json tags `total_runs`, `success_runs`, `failed_runs`.
  - `server.ListRepositoriesResponse` — `{Repositories []RepositoryOverview \`json:"repositories"\`, Total int, Page int, Limit int}`.
  - New `GET /api/repositories` response shape used by Task 6's frontend.

- [ ] **Step 1: Write the failing API test**

Rewrite `TestGetRepositories` in `internal/server/repository_test.go`. First add `"time"` to that file's imports (it currently imports only `bytes, encoding/json, net/http, net/http/httptest, testing, assert, require, config, db, server, typedef`):

```go
import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)
```

Then replace the test body:

```go
func TestGetRepositories(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "repo-a", URL: "github.com/a/a", Cron: "0 2 * * *"},
			{Name: "repo-b", URL: "github.com/b/b"},
			{Name: "alpha", URL: "github.com/alpha/alpha"},
		},
	}

	// Pre-insert executions for repo-a only: 2 runs, 1 completed, 1 failed.
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?)`,
		"e1", "repo-a", now, now.Add(time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?)`,
		"e2", "repo-a", now, now.Add(time.Minute), "failed", "boom")

	s := server.NewRepoTestServer(cfg, testDB)

	// Note: typedef.Repository fields marshal as PascalCase (no json tags),
	// so the response key is "Name" — the tag here must match exactly.
	type repoView struct {
		Name        string     `json:"Name"`
		LastRunTime *time.Time `json:"last_run_time"`
		NextRunTime *time.Time `json:"next_run_time"`
		TotalRuns   int64      `json:"total_runs"`
		SuccessRuns int64      `json:"success_runs"`
		FailedRuns  int64      `json:"failed_runs"`
	}
	type listData struct {
		Repositories []repoView `json:"repositories"`
		Total        int        `json:"total"`
		Page         int        `json:"page"`
		Limit        int        `json:"limit"`
	}
	var getList func(query string) listData
	getList = func(query string) listData {
		req, _ := http.NewRequest("GET", "/api/repositories"+query, nil)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)
		assert.Equal(t, 200, resp.Code)
		var response struct {
			Code int      `json:"code"`
			Data listData `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
		assert.Equal(t, 200, response.Code)
		return response.Data
	}

	t.Run("list all with stats", func(t *testing.T) {
		d := getList("")
		assert.Equal(t, 3, d.Total)
		assert.Equal(t, 1, d.Page)
		assert.Equal(t, 20, d.Limit)
		assert.Len(t, d.Repositories, 3)

		byName := map[string]repoView{}
		for _, r := range d.Repositories {
			byName[r.Name] = r
		}
		ra := byName["repo-a"]
		assert.Equal(t, int64(2), ra.TotalRuns)
		assert.Equal(t, int64(1), ra.SuccessRuns)
		assert.Equal(t, int64(1), ra.FailedRuns)
		require.NotNil(t, ra.LastRunTime, "repo-a has a last run")
		require.NotNil(t, ra.NextRunTime, "repo-a has a cron expression")
		rb := byName["repo-b"]
		assert.Equal(t, int64(0), rb.TotalRuns)
		assert.Nil(t, rb.LastRunTime)
		assert.Nil(t, rb.NextRunTime, "repo-b has no cron")
	})

	t.Run("search filters by name", func(t *testing.T) {
		d := getList("?search=repo")
		assert.Equal(t, 2, d.Total)
		names := map[string]bool{}
		for _, r := range d.Repositories {
			names[r.Name] = true
		}
		assert.True(t, names["repo-a"])
		assert.True(t, names["repo-b"])
		assert.False(t, names["alpha"])
	})

	t.Run("pagination", func(t *testing.T) {
		d1 := getList("?page=1&limit=2")
		assert.Equal(t, 3, d1.Total)
		assert.Len(t, d1.Repositories, 2)
		d2 := getList("?page=2&limit=2")
		assert.Len(t, d2.Repositories, 1)
	})
}
```

Also update the GET-verification assertions in `TestCreateRepository` and `TestDeleteRepository` (they currently unmarshal `Data []typedef.Repository`). Change both to:

```go
		var getResp struct {
			Code int `json:"code"`
			Data struct {
				Repositories []typedef.Repository `json:"repositories"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &getResp))
		assert.Len(t, getResp.Data.Repositories, 2) // or 1 for delete
```

(Adjust the `assert.Len` count to match each test's expectation.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run 'TestGetRepositories|TestCreateRepository|TestDeleteRepository'`
Expected: FAIL — JSON unmarshal cannot find `repositories` (old shape returns a bare array).

- [ ] **Step 3: Add the response types**

In `internal/server/types.go`, add the `typedef` import and the two types:

```go
import (
	"time"

	"github.com/wnarutou/gitrieve/internal/typedef"
)

type RepositoryOverview struct {
	typedef.Repository
	LastRunTime *time.Time `json:"last_run_time"`
	NextRunTime *time.Time `json:"next_run_time"`
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

- [ ] **Step 4: Add `nextRunTime` to `helpers.go`**

Append to `internal/server/helpers.go`:

```go
import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// nextRunTime returns the next time a repository's cron expression will fire,
// or nil when the expression is empty or invalid.
func nextRunTime(cronExpr string, now time.Time) *time.Time {
	if cronExpr == "" {
		return nil
	}
	sched, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return nil
	}
	t := sched.Next(now)
	return &t
}
```

(Replace the existing import block with the combined one above.)

- [ ] **Step 5: Rewrite `GetRepositories`**

In `internal/server/api.go`, replace the whole `GetRepositories` handler (currently just `c.JSON(... a.config.Repository ...)`):

```go
// GetRepositories returns repositories with per-repo execution stats, last/next
// run times, search (fuzzy name match) and pagination.
func (a *API) GetRepositories(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	// Aggregate per-repository execution stats from the DB.
	type agg struct {
		LastRun *time.Time
		Total   int64
		Success int64
		Failed  int64
	}
	stats := map[string]agg{}

	rows, err := a.db.Query(`
		SELECT job_name,
		       MAX(start_time) AS last_run,
		       COUNT(*)        AS total,
		       COALESCE(SUM(status = 'completed'), 0) AS success,
		       COALESCE(SUM(status = 'failed'), 0)    AS failed
		FROM executions
		GROUP BY job_name`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "Failed to query repository stats: " + err.Error()})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var lastRun sql.NullTime
		var a agg
		if err := rows.Scan(&name, &lastRun, &a.Total, &a.Success, &a.Failed); err != nil {
			c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "Failed to scan repository stats: " + err.Error()})
			return
		}
		if lastRun.Valid {
			a.LastRun = &lastRun.Time
		}
		stats[name] = a
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "Failed to iterate repository stats: " + err.Error()})
		return
	}

	// Fuzzy name filter (in-memory equivalent of LIKE '%search%').
	filtered := make([]typedef.Repository, 0, len(a.config.Repository))
	for _, repo := range a.config.Repository {
		if search != "" && !strings.Contains(repo.Name, search) {
			continue
		}
		filtered = append(filtered, repo)
	}

	total := len(filtered)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	now := time.Now()
	overviews := make([]RepositoryOverview, 0, end-start)
	for _, repo := range filtered[start:end] {
		s := stats[repo.Name]
		overviews = append(overviews, RepositoryOverview{
			Repository:  repo,
			LastRunTime: s.LastRun,
			NextRunTime: nextRunTime(repo.Cron, now),
			TotalRuns:   s.Total,
			SuccessRuns: s.Success,
			FailedRuns:  s.Failed,
		})
	}

	c.JSON(http.StatusOK, Response{Code: 200, Data: ListRepositoriesResponse{
		Repositories: overviews,
		Total:        total,
		Page:         page,
		Limit:        limit,
	}})
}
```

Add imports `"database/sql"` and `"strings"` to `api.go` (already has `strconv`, `time`).

- [ ] **Step 6: Add `nextRunTime` unit test**

Append to `internal/server/helpers_test.go`:

```go
func TestNextRunTime(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	assert.Nil(t, nextRunTime("", now), "empty cron has no next run")
	assert.Nil(t, nextRunTime("not a cron", now), "invalid cron has no next run")

	nt := nextRunTime("0 2 * * *", now)
	require.NotNil(t, nt)
	assert.Equal(t, time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC), *nt)

	nt2 := nextRunTime("@daily", now)
	require.NotNil(t, nt2)
	assert.Equal(t, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), *nt2)
}
```

Add `"time"` and `"github.com/stretchr/testify/require"` to `helpers_test.go` imports.

- [ ] **Step 7: Tidy modules and run all server tests**

Run: `go mod tidy`
Run: `go test ./internal/server/`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/server/api.go internal/server/types.go internal/server/helpers.go internal/server/helpers_test.go internal/server/repository_test.go go.mod go.sum
git commit -m "feat: enrich /api/repositories with stats, next/last run, search, pagination
```

---

### Task 4: `ui` sink + per-goroutine execution binding

**Files:**
- Modify: `internal/ui/print.go`
- Test: `internal/ui/print_test.go` (new)

**Interfaces:**
- Produces (consumed by Task 5):
  - `type Sink interface { Log(executionID, jobName, level, message string) error }`
  - `func SetSink(s Sink)`
  - `func Bind(executionID, jobName string) func()` — binds the *calling goroutine*; the returned func unbinds.
- Behavior: after a goroutine calls `ui.Bind(execID, jobName)`, every `ui.Printf`/`ui.Errorf` from that goroutine is forwarded to the sink as `Log(execID, jobName, "info"|"error", msg)`. No binding → terminal output only.

- [ ] **Step 1: Write the failing ui test**

Create `internal/ui/print_test.go`:

```go
package ui

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeSink struct {
	mu    sync.Mutex
	logs  []string
}

func (f *fakeSink) Log(executionID, jobName, level, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, executionID+"|"+jobName+"|"+level+"|"+message)
	return nil
}

func (f *fakeSink) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

func TestBindRoutesUiOutputToSink(t *testing.T) {
	s := &fakeSink{}
	SetSink(s)
	defer SetSink(nil)

	// Without a binding, output is terminal-only (not forwarded).
	Printf("hello %s", "world")
	assert.Len(t, s.messages(), 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		unbind := Bind("exec-1", "repo-a")
		Errorf("boom %d", 42)
		unbind()
		Printf("after unbind")
	}()
	<-done

	msgs := s.messages()
	assert.Len(t, msgs, 1, "only the bound call should be forwarded")
	assert.Equal(t, "exec-1|repo-a|error|boom 42", msgs[0])
}

func TestBindIsPerGoroutine(t *testing.T) {
	s := &fakeSink{}
	SetSink(s)
	defer SetSink(nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		unbind := Bind("exec-A", "repo-a")
		defer unbind()
		Printf("A")
	}()
	go func() {
		defer wg.Done()
		unbind := Bind("exec-B", "repo-b")
		defer unbind()
		Printf("B")
	}()
	wg.Wait()

	msgs := s.messages()
	assert.Len(t, msgs, 2)
	assert.Contains(t, msgs, "exec-A|repo-a|info|A")
	assert.Contains(t, msgs, "exec-B|repo-b|info|B")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/`
Expected: FAIL — `undefined: SetSink`, `undefined: Bind` (and `Printf`/`Errorf` never forward).

- [ ] **Step 3: Implement sink + binding in `internal/ui/print.go`**

Replace the contents of `internal/ui/print.go`:

```go
package ui

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/gookit/color"
)

// Sink receives log messages produced by ui.Printf/ui.Errorf from a goroutine
// that has been bound to an execution via Bind.
type Sink interface {
	Log(executionID, jobName, level, message string) error
}

var (
	sinkMu sync.RWMutex
	sink   Sink

	bindMu   sync.RWMutex
	bindings map[uint64]boundJob
)

type boundJob struct {
	executionID string
	jobName     string
}

// SetSink registers the sink that receives bound log output. Passing nil
// disables forwarding (the default, so CLI/daemon output is unchanged).
func SetSink(s Sink) {
	sinkMu.Lock()
	sink = s
	sinkMu.Unlock()
}

// Bind associates the calling goroutine with an execution record so that
// Printf/Errorf calls from this goroutine are also persisted through the sink.
// The returned function unbinds the goroutine.
func Bind(executionID, jobName string) func() {
	id := goroutineID()
	bindMu.Lock()
	if bindings == nil {
		bindings = make(map[uint64]boundJob)
	}
	bindings[id] = boundJob{executionID: executionID, jobName: jobName}
	bindMu.Unlock()
	return func() {
		bindMu.Lock()
		delete(bindings, id)
		bindMu.Unlock()
	}
}

// goroutineID returns the current goroutine's ID by parsing the runtime stack.
func goroutineID() uint64 {
	buf := make([]byte, 256)
	buf = buf[:runtime.Stack(buf, false)]
	line := string(buf)
	i := strings.Index(line, "goroutine ")
	idStr := line[i+len("goroutine "):]
	j := strings.IndexByte(idStr, ' ')
	idStr = idStr[:j]
	id, _ := strconv.ParseUint(idStr, 10, 64)
	return id
}

// logThroughSink forwards a message to the sink when the calling goroutine is
// bound. It short-circuits (no runtime.Stack call) when there is no sink or no
// bindings.
func logThroughSink(level, message string) {
	sinkMu.RLock()
	s := sink
	sinkMu.RUnlock()
	if s == nil {
		return
	}
	bindMu.RLock()
	has := len(bindings) > 0
	bindMu.RUnlock()
	if !has {
		return
	}

	id := goroutineID()
	bindMu.RLock()
	b, ok := bindings[id]
	bindMu.RUnlock()
	if !ok {
		return
	}
	_ = s.Log(b.executionID, b.jobName, level, message)
}

func Errorf(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	color.Danger.Print(msg + "\n")
	logThroughSink("error", msg)
}

func ErrorfExit(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	color.Danger.Print(msg + "\n")
	os.Exit(1)
}

func Printf(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	color.Success.Print(msg + "\n")
	logThroughSink("info", msg)
}

func Exit() {
	os.Exit(0)
}
```

Note: `color.Danger.Print` / `color.Success.Print` are used (not `Printf`) because `msg` is already fully formatted and may contain `%`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/print.go internal/ui/print_test.go
git commit -m "feat: add per-goroutine execution sink to ui package
```

---

### Task 5: Wire the executor to the ui sink

**Files:**
- Modify: `internal/executor/executor.go`
- Test: `internal/executor/executor_test.go`

**Interfaces:**
- Consumes: `ui.SetSink(Sink)`, `ui.Bind(executionID, jobName) func()` (from Task 4). `*logger.Logger` already satisfies `ui.Sink`.
- Produces: none new. Web-triggered jobs now persist detailed `ui` output to the `logs` table, bound to the correct execution.

- [ ] **Step 1: Write the failing executor test**

Add to `internal/executor/executor_test.go`:

```go
func TestExecuteJobWritesBoundLogs(t *testing.T) {
	exec, testDB := newTestExecutor(t)

	jobID, err := exec.ExecuteJob("test-repo")
	require.NoError(t, err)

	// executeAsync binds the goroutine and ui.Printf("Starting job execution")
	// is forwarded to the DB logs for this execution.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		err := testDB.QueryRow("SELECT COUNT(*) FROM logs WHERE execution_id = ?", jobID).Scan(&count)
		require.NoError(t, err)
		if count >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected at least one log row for execution %s, found none", jobID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The log content must be the unified ui output, scoped to this execution.
	var message string
	err = testDB.QueryRow("SELECT message FROM logs WHERE execution_id = ? ORDER BY id LIMIT 1", jobID).Scan(&message)
	require.NoError(t, err)
	assert.Equal(t, "Starting job execution", message)
}
```

Update the test file imports to add `"time"` and `"github.com/stretchr/testify/require"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/executor/ -run TestExecuteJobWritesBoundLogs`
Expected: FAIL — timeout, because the executor currently writes no `logs` row for this execution (the `logger.Log` calls only write the start/completed lines… note: actually the current code writes "Starting job execution" via `e.logger.Log` in `ExecuteJob`, which *should* create a row. If it already passes, see note below.)

> **Note:** The current `ExecuteJob` already calls `e.logger.Log(jobID, jobName, "info", "Starting job execution")` synchronously, which may make this test pass before the change. If Step 2 passes, proceed directly to Step 3 — the assertion still guards the refactor (that "Starting job execution" is written through the bound ui path and lands in the DB).

- [ ] **Step 3: Rewire executor to use the ui sink**

In `internal/executor/executor.go`:

(a) In `NewExecutor`, register the DB logger as the ui sink (guard nil), and drop the now-unused `logger` field:

```go
func NewExecutor(logger *logger.Logger, db *db.DB, cfg *config.Config) *Executor {
	if logger != nil {
		ui.SetSink(logger)
	}
	return &Executor{
		db:          db,
		cfg:         cfg,
		runningJobs: make(map[string]*JobContext),
	}
}
```

Remove the `logger *logger.Logger` field from the `Executor` struct (keep the constructor parameter).

(b) In `ExecuteJob`, delete the "Log start" block (previously lines ~91-94):

```go
	// Log start
	if e.logger != nil {
		e.logger.Log(jobID, jobName, "info", "Starting job execution")
	}
```

(c) In `executeAsync`, bind at entry and route the terminal-state logs through `ui`:

```go
func (e *Executor) executeAsync(ctx context.Context, jobID string, job typedef.Repository) {
	unbind := ui.Bind(jobID, job.Name)
	defer unbind()

	defer func() {
		e.mu.Lock()
		delete(e.runningJobs, jobID)
		e.mu.Unlock()
	}()

	ui.Printf("Starting job execution")

	// Check if context is already cancelled
	if ctx.Err() != nil {
		e.updateJobStatus(jobID, string(StatusCancelled), "")
		ui.Printf("Job was cancelled")
		return
	}

	// Get storages
	var storages []typedef.MultiStorage
	for _, storageName := range job.Storage {
		for _, s := range e.cfg.Storage {
			if s.Name == storageName {
				storages = append(storages, typedef.MultiStorage{
					Storage: typedef.Storage{
						Name: s.Name,
						Type: s.Type,
						Path: s.Path,
					},
				})
				break
			}
		}
	}

	// Execute repository sync
	err := repository.Sync(job, false, storages)

	if err != nil {
		// Check if cancelled
		if ctx.Err() != nil {
			e.updateJobStatus(jobID, string(StatusCancelled), "")
			ui.Printf("Job was cancelled")
		} else {
			e.updateJobStatus(jobID, string(StatusFailed), err.Error())
			ui.Errorf("Job failed: %v", err)
		}
		return
	}

	e.updateJobStatus(jobID, string(StatusCompleted), "")
	ui.Printf("Job completed successfully")
}
```

(d) Remove the now-unused import `"github.com/wnarutou/gitrieve/internal/logger"`? **No** — keep it: `NewExecutor` still takes a `*logger.Logger` parameter and calls `ui.SetSink(logger)`. Add the `ui` import: `"github.com/wnarutou/gitrieve/internal/ui"`.

- [ ] **Step 4: Run executor tests to verify they pass**

Run: `go test ./internal/executor/ ./internal/server/ ./internal/ui/`
Expected: PASS. (Note: `TestExecuteJobCreatesRecord` and `TestExecuteJobWritesBoundLogs` spawn a background goroutine that attempts to clone `github.com/test/repo`; the job fails on network error and the test only asserts on the DB records/logs, so this passes without network success.)

- [ ] **Step 5: Commit**

```bash
git add internal/executor/executor.go internal/executor/executor_test.go
git commit -m "feat: route executor job logs through the ui sink into the DB
```

---

### Task 6: Frontend — Repositories page

**Files:**
- Modify: `web/static/js/main.js`
- Modify: `web/static/css/main.css`

**Interfaces:**
- Consumes: new `GET /api/repositories?page=&limit=&search=` shape (Task 3): response `data = { repositories: [{Name, URL, Type, Cron, Storage, ..., last_run_time, next_run_time, total_runs, success_runs, failed_runs}], total, page, limit }`. Also `POST /api/jobs {repository}` (existing).
- Produces (consumed by Task 7): `debounce(fn, ms)`, `paginationHTML(page, pages, total, idPrefix)`, `runRepo(name, btn)`.

- [ ] **Step 1: Add state + shared helpers**

In `web/static/js/main.js`, extend the `state` object:

```js
const state = {
    jobsPage: 1,
    jobsStatus: '',
    jobsRepo: '',
    reposPage: 1,
    reposSearch: '',
    es: null,
    logJob: null,
    logIds: {}
};
```

Add a `debounce` helper and a `paginationHTML` helper near the other top-level helpers:

```js
function debounce(fn, ms) {
    let t;
    return function (...args) {
        clearTimeout(t);
        t = setTimeout(() => fn.apply(this, args), ms);
    };
}

function paginationHTML(page, pages, total, idPrefix) {
    return `
        <div class="pagination">
            <button class="btn btn-sm" id="pg-prev-${idPrefix}" ${page > 1 ? '' : 'disabled'}>Prev</button>
            <span class="pg-info">Page ${page} of ${pages} (${total} total)</span>
            <button class="btn btn-sm" id="pg-next-${idPrefix}" ${page < pages ? '' : 'disabled'}>Next</button>
        </div>`;
}
```

Add an `optionsCell` helper (extracts the existing inline options rendering):

```js
function optionsCell(r) {
    const parts = [];
    if (r.UseCache) parts.push('cache');
    if (r.AllBranches) parts.push('allBranches');
    if (r.DownloadReleases) parts.push('releases');
    if (r.DownloadIssues) parts.push('issues');
    if (r.DownloadWiki) parts.push('wiki');
    if (r.DownloadDiscussion) parts.push('discussion');
    return parts.length ? esc(parts.join(' ')) : '-';
}
```

Add a `runRepo` function (place it near `cancelJob`):

```js
async function runRepo(name, btn) {
    if (!name) return;
    btn.disabled = true;
    try {
        const data = await api('/api/jobs', { method: 'POST', body: JSON.stringify({ repository: name }) });
        toast('Job started (' + (data.job_id || '').slice(0, 8) + '…)');
        openLogModal(data.job_id, name);
        renderRepositories();
    } catch (e) {
        toast('Failed to start job: ' + e.message, true);
        btn.disabled = false;
    }
}
```

- [ ] **Step 2: Rewrite `renderRepositories`**

Replace the current `renderRepositories` body (it currently fetches a bare array and renders a fixed table with no pagination/refresh/search):

```js
async function renderRepositories() {
    $('#app').innerHTML = '<div class="loading">Loading repositories…</div>';

    const params = new URLSearchParams({ page: state.reposPage, limit: 20 });
    if (state.reposSearch) params.set('search', state.reposSearch);

    let data = null;
    try {
        data = await api('/api/repositories?' + params.toString());
    } catch (e) {
        $('#app').innerHTML = '<div class="empty error-text">Failed to load repositories: ' + esc(e.message) + '</div>';
        return;
    }
    const repos = (data && data.repositories) || [];
    const total = (data && data.total) || 0;
    const pages = Math.max(1, Math.ceil(total / 20));

    const rows = repos.map(r => `
        <tr>
            <td><strong>${esc(r.Name)}</strong></td>
            <td class="muted">${esc(r.URL || '-')}</td>
            <td>${esc(r.Type || 'repo')}</td>
            <td class="muted">${esc(r.Cron || '-')}</td>
            <td class="muted">${fmtTime(r.next_run_time)}</td>
            <td class="muted">${fmtTime(r.last_run_time)}</td>
            <td class="muted">${r.total_runs} total · ${r.success_runs} ok · ${r.failed_runs} fail</td>
            <td class="muted">${esc((r.Storage || []).join(', ') || '-')}</td>
            <td class="muted">${optionsCell(r)}</td>
            <td class="actions">
                <button class="btn btn-sm btn-primary btn-run-repo" data-name="${esc(r.Name)}">Execute</button>
                <button class="btn btn-sm btn-edit-repo" data-name="${esc(r.Name)}">Edit</button>
                <button class="btn btn-sm btn-danger btn-del-repo" data-name="${esc(r.Name)}">Delete</button>
            </td>
        </tr>`).join('');

    $('#app').innerHTML = `
        <div class="page-header">
            <h2>Repositories</h2>
            <div class="toolbar-group">
                <input type="text" id="repos-search" placeholder="Filter by repository name…" value="${esc(state.reposSearch)}">
                <button id="btn-add-repo" class="btn btn-primary">Add Repository</button>
                <button id="btn-refresh-repos" class="btn">Refresh</button>
            </div>
        </div>
        <div class="panel">
            ${repos.length
                ? '<div class="table-wrap"><table class="table"><thead><tr><th>Name</th><th>URL</th><th>Type</th><th>Cron</th><th>Next Run</th><th>Last Run</th><th>Stats</th><th>Storage</th><th>Options</th><th></th></tr></thead><tbody>' + rows + '</tbody></table></div>'
                : '<div class="empty">No repositories configured. Click <strong>Add Repository</strong>.</div>'}
            ${repos.length ? paginationHTML(state.reposPage, pages, total, 'repos') : ''}
        </div>`;

    $('#btn-add-repo').addEventListener('click', () => openRepoForm(null));
    $('#btn-refresh-repos').addEventListener('click', () => renderRepositories());
    $('#repos-search').addEventListener('input', debounce(() => {
        state.reposSearch = $('#repos-search').value.trim();
        state.reposPage = 1;
        renderRepositories();
    }, 300));
    $('#repos-search').addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter') {
            ev.preventDefault();
            state.reposSearch = $('#repos-search').value.trim();
            state.reposPage = 1;
            renderRepositories();
        }
    });

    const prev = $('#pg-prev-repos');
    const next = $('#pg-next-repos');
    if (prev) prev.addEventListener('click', () => { if (state.reposPage > 1) { state.reposPage--; renderRepositories(); } });
    if (next) next.addEventListener('click', () => { state.reposPage++; renderRepositories(); });

    $$('.btn-run-repo').forEach(b => b.addEventListener('click', () => runRepo(b.dataset.name, b)));
    $$('.btn-edit-repo').forEach(b => b.addEventListener('click', () => {
        const r = repos.find(x => x.Name === b.dataset.name);
        if (r) openRepoForm(r);
    }));
    $$('.btn-del-repo').forEach(b => b.addEventListener('click', () => deleteRepo(b.dataset.name)));
}
```

- [ ] **Step 3: Change the log-modal `done` handler to refresh the current route**

In `openLogModal`, replace:

```js
        if (state.logJob === jobId) renderJobs();
```

with:

```js
        if (state.logJob === jobId) renderApp();
```

So opening the log from the Repositories page re-renders Repositories (not Jobs) when the job finishes.

- [ ] **Step 4: Add a small CSS rule**

In `web/static/css/main.css`, add near the `.toolbar-group` rule:

```css
.toolbar input[type="text"] { min-width: 220px; }
```

- [ ] **Step 5: Syntax-check and build**

Run: `node --check web/static/js/main.js`
Expected: no output (valid JS).
Run: `go build ./...`
Expected: success (web assets still embed).

- [ ] **Step 6: Commit**

```bash
git add web/static/js/main.js web/static/css/main.css
git commit -m "feat: repositories page — execute button, stats, times, pagination, search, refresh
```

---

### Task 7: Frontend — Jobs page

**Files:**
- Modify: `web/static/js/main.js`

**Interfaces:**
- Consumes: fuzzy `repository` param on `GET /api/jobs` (Task 2); `debounce` + `paginationHTML` (Task 6).
- Produces: none.

- [ ] **Step 1: Rewrite `jobsToolbar` to drop the run dropdown**

Replace the whole `jobsToolbar` function:

```js
function jobsToolbar(jobCount) {
    return `
        <div class="panel">
            <div class="toolbar">
                <div class="toolbar-group">
                    <input type="text" id="jobs-repo-filter" placeholder="Filter by repository name…" value="${esc(state.jobsRepo)}">
                </div>
                <div class="toolbar-group">
                    <select id="jobs-status">
                        <option value="">All statuses</option>
                        <option value="pending">Pending</option>
                        <option value="running">Running</option>
                        <option value="completed">Completed</option>
                        <option value="failed">Failed</option>
                        <option value="cancelled">Cancelled</option>
                    </select>
                    <button id="btn-refresh" class="btn">Refresh</button>
                </div>
                <div class="toolbar-group toolbar-count">${jobCount} job(s)</div>
            </div>
        </div>`;
}
```

- [ ] **Step 2: Update `jobsTable` — new empty text + shared pagination**

In `jobsTable`, change the empty-state line:

```js
    if (!jobs.length) {
        return '<div class="empty">No jobs yet. Run a repository from the <strong>Repositories</strong> page, or click <strong>Refresh</strong>.</div>';
    }
```

And replace the inline `.pagination` block at the end of the template string:

```js
        <div class="pagination">
            <button class="btn btn-sm" id="pg-prev" ${page > 1 ? '' : 'disabled'}>Prev</button>
            <span class="pg-info">Page ${page} of ${pages} (${total} total)</span>
            <button class="btn btn-sm" id="pg-next" ${page < pages ? '' : 'disabled'}>Next</button>
        </div>`;
```

with:

```js
        ${paginationHTML(page, pages, total, 'jobs')}`;
```

- [ ] **Step 3: Rewrite `renderJobs`**

Replace the body of `renderJobs`:

```js
async function renderJobs() {
    $('#app').innerHTML = '<div class="loading">Loading jobs…</div>';

    const params = new URLSearchParams({ page: state.jobsPage, limit: 20 });
    if (state.jobsStatus) params.set('status', state.jobsStatus);
    if (state.jobsRepo) params.set('repository', state.jobsRepo);

    let jobs = [], total = 0;
    try {
        const data = await api('/api/jobs?' + params.toString());
        jobs = (data && data.jobs) || [];
        total = (data && data.total) || 0;
    } catch (e) {
        $('#app').innerHTML = '<div class="empty error-text">Failed to load jobs: ' + esc(e.message) + '</div>';
        return;
    }

    $('#app').innerHTML = jobsToolbar(total) + jobsTable(jobs, state.jobsPage, 20, total);

    $('#btn-refresh').addEventListener('click', () => renderJobs());
    $('#jobs-status').value = state.jobsStatus;
    $('#jobs-status').addEventListener('change', (ev) => {
        state.jobsStatus = ev.target.value;
        state.jobsPage = 1;
        renderJobs();
    });

    const filter = $('#jobs-repo-filter');
    filter.addEventListener('input', debounce(() => {
        state.jobsRepo = filter.value.trim();
        state.jobsPage = 1;
        renderJobs();
    }, 300));
    filter.addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter') {
            ev.preventDefault();
            state.jobsRepo = filter.value.trim();
            state.jobsPage = 1;
            renderJobs();
        }
    });

    const prev = $('#pg-prev-jobs');
    const next = $('#pg-next-jobs');
    if (prev) prev.addEventListener('click', () => { if (state.jobsPage > 1) { state.jobsPage--; renderJobs(); } });
    if (next) next.addEventListener('click', () => { state.jobsPage++; renderJobs(); });

    $$('.btn-log').forEach(b => b.addEventListener('click', () => openLogModal(b.dataset.jobid, b.dataset.jobname)));
    $$('.btn-cancel').forEach(b => b.addEventListener('click', () => cancelJob(b.dataset.jobid)));
}
```

- [ ] **Step 4: Remove `runJob` and the 5s auto-refresh**

Delete the entire `runJob` function.

In the `DOMContentLoaded` handler, delete the block:

```js
    setInterval(() => {
        const route = (location.hash || '#/jobs').replace(/^#\/?/, '').split('/')[0];
        if (route === 'jobs' && $('#log-modal').classList.contains('hidden')) renderJobs();
    }, 5000);
```

Keep the `refreshMetrics()` + its 15s interval (server status dot, unrelated to the job list).

- [ ] **Step 5: Syntax-check and build**

Run: `node --check web/static/js/main.js`
Expected: no output (valid JS).
Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Manual verification**

Start the server: `go run . -c config/example.config.yaml server`, open `http://localhost:8080/#/jobs`.
- Confirm no automatic reload (list only changes on Refresh click, filter input, or pagination).
- Confirm there is no Run dropdown/button.
- Type `test` in the filter box → jobs for repos whose name contains "test" appear, ordered by `start_time` descending; empty the box → all jobs.
- Confirm `?status=` filter, pagination, Logs and Cancel still work.
- Open the log of a running/completed job and confirm detailed `ui` lines (e.g. `local branch main has been checked out.`, `File ... stored`) appear, not just the 3 generic lines.

- [ ] **Step 7: Commit**

```bash
git add web/static/js/main.js
git commit -m "feat: jobs page — manual refresh, fuzzy repo filter, remove run dropdown
```

---

### Task 8: Mount the `.gitrieve` cache in docker-compose

**Files:**
- Modify: `docker-compose.yml`

**Interfaces:**
- Consumes: none.
- Produces: persistent `./.gitrieve` on the host, mounted at `/app/.gitrieve` in the container (the container working dir is `/app`, so `useCache: true` writes the git cache under `/app/.gitrieve`).

- [ ] **Step 1: Add the volume entry**

In `docker-compose.yml`, after the existing `./data:/app/data` volume entry (line ~28), add:

```yaml
      # git clone cache directory — persists the local .git object store and
      # working copy across container rebuilds (required when useCache: true).
      # The in-container path resolves under the working directory /app.
      - ./.gitrieve:/app/.gitrieve
```

- [ ] **Step 2: Validate the YAML**

Run: `docker compose config` (or `docker-compose config` if older CLI)
Expected: config renders; the `gitrieve` service shows the new volume `./.gitrieve:/app/.gitrieve`.

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml
git commit -m "fix: mount .gitrieve cache directory in docker-compose
```

---

## Self-Review Notes

- Spec coverage: bug fix (Task 1), jobs fuzzy filter (Task 2), repos enriched/paginated/search (Task 3), logging unification via ui (Tasks 4-5), repos page UI (Task 6), jobs page UI (Task 7), docker-compose mount (Task 8). Every spec section maps to a task.
- Repo JSON field naming gotcha (PascalCase) is captured in Global Constraints and used correctly in Task 6's frontend (`r.Name`, `r.URL`, `r.Cron`, `r.Storage`) alongside the snake_case new fields (`r.last_run_time`, etc.).
- The `escapeLike` helper introduced in Task 2 is only needed for the SQL LIKE filter; the in-memory repos `search` uses `strings.Contains` (equivalent fuzzy semantics, no escaping needed) — no dangling dependency.
- CLI/daemon unchanged: no `SetSink`/`Bind` call in daemon or CLI paths; `logThroughSink` short-circuits when sink is nil or no bindings exist.
