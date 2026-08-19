package server_test

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

func repoResponseCode(t *testing.T, resp *httptest.ResponseRecorder) int {
	t.Helper()
	var r struct {
		Code    int         `json:"code"`
		Data    interface{} `json:"data"`
		Message string      `json:"message"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &r))
	return r.Code
}

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
	// e2 starts LATER than e1, so the "list all" subtest below can assert the
	// HAVING start_time = MAX(start_time) row selection returns e2's time.
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"e1", "repo-a", "github.com/a/a", now, now.Add(time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"e2", "repo-a", "github.com/a/a", now.Add(2*time.Minute), now.Add(3*time.Minute), "failed", "boom")

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
		// The HAVING start_time = MAX(start_time) clause selects the row with the
		// latest start_time — e2 was inserted at now.Add(2m), so that must win
		// over e1's now. SQLite round-trips strip the monotonic clock component,
		// so compare within a second (2m apart, well within the window).
		assert.WithinDuration(t, now.Add(2*time.Minute), *ra.LastRunTime, time.Second,
			"last_run_time must be the later of the two executions")
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

	t.Run("search is case-insensitive", func(t *testing.T) {
		// SQL LIKE is case-insensitive for ASCII; the in-memory repos search
		// must match that (search "REPO" should still find repo-a / repo-b).
		d := getList("?search=REPO")
		assert.Equal(t, 2, d.Total)
		names := map[string]bool{}
		for _, r := range d.Repositories {
			names[r.Name] = true
		}
		assert.True(t, names["repo-a"])
		assert.True(t, names["repo-b"])
		assert.False(t, names["alpha"])
	})

	t.Run("search filters by URL", func(t *testing.T) {
		d := getList("?search=github.com/b")
		assert.Equal(t, 1, d.Total)
		assert.Equal(t, "repo-b", d.Repositories[0].Name)
	})

	t.Run("pagination", func(t *testing.T) {
		d1 := getList("?page=1&limit=2")
		assert.Equal(t, 3, d1.Total)
		assert.Len(t, d1.Repositories, 2)
		d2 := getList("?page=2&limit=2")
		assert.Len(t, d2.Repositories, 1)
	})
}

func TestCreateRepository(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "existing-repo", URL: "github.com/existing/repo"},
		},
	}

	s := server.NewRepoTestServer(cfg, testDB)

	t.Run("success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"name":             "new-repo",
			"url":              "github.com/new/repo",
			"downloadReleases": true,
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
		assert.Equal(t, "new-repo", response.Data.Name)
		assert.Equal(t, "github.com/new/repo", response.Data.URL)
		assert.True(t, response.Data.DownloadReleases)

		// Verify via subsequent GET
		req, _ = http.NewRequest("GET", "/api/repositories", nil)
		resp = httptest.NewRecorder()
		s.ServeHTTP(resp, req)
		var getResp struct {
			Code int `json:"code"`
			Data struct {
				Repositories []typedef.Repository `json:"repositories"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &getResp))
		assert.Len(t, getResp.Data.Repositories, 2)
	})

	t.Run("same_name_different_url", func(t *testing.T) {
		// Name may repeat; identity is the URL. Same name + different URL → 200.
		body, _ := json.Marshal(map[string]interface{}{
			"name": "existing-repo",
			"url":  "github.com/another/repo",
		})
		req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 200, resp.Code)
		assert.Equal(t, 200, repoResponseCode(t, resp))
	})

	t.Run("empty_name", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"url": "github.com/no/name",
		})
		req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 400, resp.Code)
		assert.Equal(t, 400, repoResponseCode(t, resp))
	})
}

func TestUpdateRepository(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "update-me", URL: "github.com/old/url", AllBranches: false},
		},
	}

	s := server.NewRepoTestServer(cfg, testDB)

	t.Run("success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"url":         "github.com/new/url",
			"allBranches": true,
		})
		req, _ := http.NewRequest("PUT", "/api/repositories/github.com/old/url", bytes.NewBuffer(body))
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
		assert.Equal(t, "update-me", response.Data.Name) // name unchanged
		assert.Equal(t, "github.com/new/url", response.Data.URL)
		assert.True(t, response.Data.AllBranches)
	})

	t.Run("not_found", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"url": "github.com/whatever",
		})
		req, _ := http.NewRequest("PUT", "/api/repositories/does-not-exist", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 404, resp.Code)
		assert.Equal(t, 404, repoResponseCode(t, resp))
	})
}

func TestDeleteRepository(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "delete-me", URL: "github.com/delete/me"},
			{Name: "keep-me", URL: "github.com/keep/me"},
		},
	}

	s := server.NewRepoTestServer(cfg, testDB)

	t.Run("success", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", "/api/repositories/github.com/delete/me", nil)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 200, resp.Code)
		var response struct {
			Code int `json:"code"`
			Data struct {
				Success bool `json:"success"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
		assert.Equal(t, 200, response.Code)
		assert.True(t, response.Data.Success)

		// Verify removal via GET
		req, _ = http.NewRequest("GET", "/api/repositories", nil)
		resp = httptest.NewRecorder()
		s.ServeHTTP(resp, req)
		var getResp struct {
			Code int `json:"code"`
			Data struct {
				Repositories []typedef.Repository `json:"repositories"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &getResp))
		assert.Len(t, getResp.Data.Repositories, 1)
		assert.Equal(t, "keep-me", getResp.Data.Repositories[0].Name)
	})

	t.Run("not_found", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", "/api/repositories/does-not-exist", nil)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 404, resp.Code)
		assert.Equal(t, 404, repoResponseCode(t, resp))
	})
}

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
	// last_run is the max member START time: alpha (a1) starts at now-2m, beta
	// (b1) at now-4m — the max is now-2m (the brief's original now-1m was the
	// member's end time; the API convention is start_time-based).
	assert.WithinDuration(t, now.Add(-2*time.Minute), *acme.LastRunTime, time.Second, "last_run must be the max of members")

	solo := byName["solo"]
	assert.Equal(t, int64(1), solo.TotalRuns)
}
