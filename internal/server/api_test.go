package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestGetJobsAPI(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	s := server.NewTestServer(testDB)
	req, _ := http.NewRequest("GET", "/api/jobs", nil)
	resp := httptest.NewRecorder()

	s.ServeHTTP(resp, req)

	if resp.Code != 200 {
		t.Errorf("Expected status 200, got %d", resp.Code)
	}
}

func TestGetJobs(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := server.NewTestServerWithExecutor(testDB, exec)

	// Insert test data
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"job-1", "test-repo", "github.com/test/repo", now, now.Add(5*time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, status) VALUES (?, ?, ?, ?, ?)`,
		"job-2", "test-repo", "github.com/test/repo", now, "running")
	testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"job-3", "test-repo", "github.com/test/repo", now, now.Add(2*time.Minute), "failed", "some error")

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "list all jobs",
			queryParams:    "",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var response struct {
					Code    int `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Jobs  []struct {
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
				assert.Equal(t, int64(3), response.Data.Total)
				assert.Equal(t, 1, response.Data.Page)
				assert.Equal(t, 20, response.Data.Limit)
				assert.Len(t, response.Data.Jobs, 3)
				// URL 解析：repo_key 命中配置条目 → url 被填上（三行同键，全部命中）
				for _, j := range response.Data.Jobs {
					assert.Equal(t, "github.com/test/repo", j.URL)
				}
			},
		},
		{
			name:           "filter by status",
			queryParams:    "?status=running",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var response struct {
					Code    int `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Jobs  []struct {
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
		{
			name:           "pagination",
			queryParams:    "?page=1&limit=2",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var response struct {
					Code    int `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Jobs  []struct {
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
				assert.Equal(t, int64(3), response.Data.Total)
				assert.Equal(t, 1, response.Data.Page)
				assert.Equal(t, 2, response.Data.Limit)
				assert.Len(t, response.Data.Jobs, 2)
			},
		},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/api/jobs"+tt.queryParams, nil)
			resp := httptest.NewRecorder()

			s.ServeHTTP(resp, req)

			assert.Equal(t, tt.expectedStatus, resp.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestCreateJob(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := server.NewTestServerWithExecutor(testDB, exec)

	// Test invalid repository - this doesn't execute actual jobs
	t.Run("invalid_repository", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{
			"repository_key": "github.com/nope/repo",
		})
		req, _ := http.NewRequest("POST", "/api/jobs", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()

		s.ServeHTTP(resp, req)

		assert.Equal(t, 404, resp.Code)
		var response struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		err := json.Unmarshal(resp.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, 404, response.Code)
		assert.Contains(t, response.Message, "not found")
	})

	// Test missing repository field
	t.Run("missing_repository_field", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{})
		req, _ := http.NewRequest("POST", "/api/jobs", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()

		s.ServeHTTP(resp, req)

		assert.Equal(t, 400, resp.Code)
		var response struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		err := json.Unmarshal(resp.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, 400, response.Code)
		assert.Contains(t, response.Message, "Invalid request")
	})

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
}

func TestCancelJob(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := server.NewTestServerWithExecutor(testDB, exec)

	// Test non-existent job
	t.Run("non-existent_job", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", "/api/jobs/non-existent-id", nil)
		resp := httptest.NewRecorder()

		s.ServeHTTP(resp, req)

		assert.Equal(t, 200, resp.Code)
		var response struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		err := json.Unmarshal(resp.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, 200, response.Code)
		assert.Equal(t, "cancelled", response.Data.Status)
	})

	// Test missing job ID
	t.Run("missing_job_ID", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", "/api/jobs/", nil)
		resp := httptest.NewRecorder()

		s.ServeHTTP(resp, req)

		// 404 route not found
		assert.Equal(t, 404, resp.Code)
	})
}

func TestGetJobsRepositoryEscaping(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)

	s := server.NewTestServerWithExecutor(testDB, exec)

	// Insert jobs whose names contain LIKE metacharacters. The repository filter
	// escapes them, so "_" must not act as a single-char wildcard (which would
	// otherwise match "foozbar" too) and "%" must not act as a wildcard run
	// (which would otherwise match every row).
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"esc-1", "foo_bar", now, "completed")
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"esc-2", "foozbar", now, "completed")
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"esc-3", "100%", now, "completed")

	type listData struct {
		Jobs []struct {
			Name string `json:"name"`
		} `json:"jobs"`
		Total int64 `json:"total"`
	}
	getList := func(query string) (int, listData) {
		req, _ := http.NewRequest("GET", "/api/jobs"+query, nil)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)
		var response struct {
			Code int      `json:"code"`
			Data listData `json:"data"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &response))
		return resp.Code, response.Data
	}

	t.Run("underscore is escaped", func(t *testing.T) {
		code, d := getList("?repository=foo_bar")
		assert.Equal(t, 200, code)
		assert.Equal(t, int64(1), d.Total, "an unescaped _ wildcard would also match foozbar")
		require.Len(t, d.Jobs, 1)
		assert.Equal(t, "foo_bar", d.Jobs[0].Name)
	})

	t.Run("percent is escaped", func(t *testing.T) {
		// The literal % in the query string must be URL-encoded (%25) to reach
		// the server as "100%".
		code, d := getList("?repository=100%25")
		assert.Equal(t, 200, code)
		assert.Equal(t, int64(1), d.Total, "an unescaped % wildcard would match every job")
		require.Len(t, d.Jobs, 1)
		assert.Equal(t, "100%", d.Jobs[0].Name)
	})
}
