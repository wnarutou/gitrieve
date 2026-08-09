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

	t.Run("duplicate_name", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"name": "existing-repo",
			"url":  "github.com/another/repo",
		})
		req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 409, resp.Code)
		assert.Equal(t, 409, repoResponseCode(t, resp))
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
		req, _ := http.NewRequest("PUT", "/api/repositories/update-me", bytes.NewBuffer(body))
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
		req, _ := http.NewRequest("DELETE", "/api/repositories/delete-me", nil)
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
