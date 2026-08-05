package server

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
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestGetJobsAPI(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()

	s := NewTestServer(testDB)
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

	s := NewTestServerWithExecutor(testDB, exec)

	// Insert test data
	now := time.Now()
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?)`,
		"job-1", "test-repo", now, now.Add(5*time.Minute), "completed", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?)`,
		"job-2", "test-repo", now, time.Time{}, "running", "")
	testDB.Exec(`INSERT INTO executions (id, job_name, start_time, end_time, status, error_message) VALUES (?, ?, ?, ?, ?, ?)`,
		"job-3", "test-repo", now, now.Add(2*time.Minute), "failed", "some error")

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

	s := NewTestServerWithExecutor(testDB, exec)

	// Test invalid repository - this doesn't execute actual jobs
	t.Run("invalid_repository", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{
			"repository": "non-existent-repo",
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

	s := NewTestServerWithExecutor(testDB, exec)

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