package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	internalserver "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// newRepoTestServer builds a TestServer that only registers the repository
// CRUD routes. It intentionally avoids the shared helper.go constructors so
// concurrent edits to that file do not conflict.
func newRepoTestServer(cfg *config.Config, testDB *db.DB) *TestServer {
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	api := internalserver.NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default()}
	s.router.GET("/api/repositories", api.GetRepositories)
	s.router.POST("/api/repositories", api.CreateRepository)
	s.router.PUT("/api/repositories/:id", api.UpdateRepository)
	s.router.DELETE("/api/repositories/:id", api.DeleteRepository)
	return s
}

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
			{Name: "repo-a", URL: "github.com/a/a"},
			{Name: "repo-b", URL: "github.com/b/b"},
		},
	}

	s := newRepoTestServer(cfg, testDB)

	req, _ := http.NewRequest("GET", "/api/repositories", nil)
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code)

	var response struct {
		Code int                    `json:"code"`
		Data []typedef.Repository   `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
	assert.Equal(t, 200, response.Code)
	assert.Len(t, response.Data, 2)
	assert.Equal(t, "repo-a", response.Data[0].Name)
	assert.Equal(t, "repo-b", response.Data[1].Name)
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

	s := newRepoTestServer(cfg, testDB)

	t.Run("success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"name":               "new-repo",
			"url":                "github.com/new/repo",
			"downloadReleases":   true,
		})
		req, _ := http.NewRequest("POST", "/api/repositories", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 200, resp.Code)
		var response struct {
			Code int                 `json:"code"`
			Data typedef.Repository  `json:"data"`
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
			Code int                  `json:"code"`
			Data []typedef.Repository `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &getResp))
		assert.Len(t, getResp.Data, 2)
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

	s := newRepoTestServer(cfg, testDB)

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

	s := newRepoTestServer(cfg, testDB)

	t.Run("success", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", "/api/repositories/delete-me", nil)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 200, resp.Code)
		var response struct {
			Code int  `json:"code"`
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
			Code int                  `json:"code"`
			Data []typedef.Repository `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &getResp))
		assert.Len(t, getResp.Data, 1)
		assert.Equal(t, "keep-me", getResp.Data[0].Name)
	})

	t.Run("not_found", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", "/api/repositories/does-not-exist", nil)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)

		assert.Equal(t, 404, resp.Code)
		assert.Equal(t, 404, repoResponseCode(t, resp))
	})
}
