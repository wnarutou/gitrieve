package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	internalserver "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestServerRootRoute(t *testing.T) {
	server := NewServer(nil)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	req, _ := http.NewRequest("GET", "/", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != 200 {
		t.Errorf("Expected status 200, got %d", resp.Code)
	}
}

func TestServerReconcilesInterruptedExecutionsBeforeServing(t *testing.T) {
	executionID := fmt.Sprintf("interrupted-startup-%d", time.Now().UnixNano())
	database, err := db.Initialize(internalserver.GetServerConfig().DbPath)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(database))
	_, err = database.Exec(`
		INSERT INTO executions (id, job_name, repo_key, start_time, status)
		VALUES (?, ?, ?, ?, ?)`, executionID, "repo", "github.com/test/interrupted", time.Now(), "running")
	require.NoError(t, err)
	require.NoError(t, database.CreateComponents(context.Background(), executionID, []db.ComponentName{db.ComponentCode}))
	require.NoError(t, database.StartComponent(context.Background(), executionID, db.ComponentCode, time.Now()))
	require.NoError(t, database.Close())

	server := NewServer(nil)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	var executionStatus, componentStatus string
	require.NoError(t, server.database.QueryRow(
		`SELECT status FROM executions WHERE id = ?`, executionID,
	).Scan(&executionStatus))
	require.NoError(t, server.database.QueryRow(
		`SELECT status FROM execution_components WHERE execution_id = ?`, executionID,
	).Scan(&componentStatus))
	require.Equal(t, "failed", executionStatus)
	require.Equal(t, "failed", componentStatus)
}

func TestServerStartsConfiguredCronJobs(t *testing.T) {
	const repoKey = "invalid-server-cron"
	server := NewServer(&config.Config{
		ConcurrencyNum: 1,
		Repository: []typedef.Repository{
			{
				Name: "server-cron-test",
				URL:  repoKey,
				Cron: "@every 1s",
			},
		},
	})
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	waitForScheduledJob(t, server, repoKey)
}

func TestServerSchedulesRepositoryCreatedAtRuntime(t *testing.T) {
	const repoKey = "invalid-runtime-cron"
	server := NewServer(&config.Config{ConcurrencyNum: 1})
	t.Cleanup(func() { require.NoError(t, server.Close()) })

	createReq := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader(`{
		"Name":"runtime-cron-test",
		"URL":"invalid-runtime-cron",
		"Cron":"@every 1s"
	}`))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)
	require.Equal(t, http.StatusOK, createResp.Code, createResp.Body.String())
	waitForScheduledJob(t, server, repoKey)
}

func TestServerRefreshesCronWhenRepositoryUpdated(t *testing.T) {
	const repoKey = "invalid-updated-cron"
	server := NewServer(&config.Config{
		ConcurrencyNum: 1,
		Repository: []typedef.Repository{
			{
				Name: "updated-cron-test",
				URL:  repoKey,
			},
		},
	})
	t.Cleanup(func() { require.NoError(t, server.Close()) })

	updateReq := httptest.NewRequest(http.MethodPut, "/api/repositories/"+repoKey, strings.NewReader(`{
		"Cron":"@every 1s"
	}`))
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	server.ServeHTTP(updateResp, updateReq)
	require.Equal(t, http.StatusOK, updateResp.Code, updateResp.Body.String())
	waitForScheduledJob(t, server, repoKey)
}

func TestServerStopsCronWhenRepositoryDeleted(t *testing.T) {
	const repoKey = "invalid-deleted-cron"
	server := NewServer(&config.Config{
		ConcurrencyNum: 1,
		Repository: []typedef.Repository{
			{
				Name: "deleted-cron-test",
				URL:  repoKey,
				Cron: "@every 1s",
			},
		},
	})
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	require.Len(t, server.scheduler.Jobs(), 1, "precondition: repository cron should be registered")

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/repositories/"+repoKey, nil)
	deleteResp := httptest.NewRecorder()
	server.ServeHTTP(deleteResp, deleteReq)
	require.Equal(t, http.StatusOK, deleteResp.Code, deleteResp.Body.String())
	require.Empty(t, server.scheduler.Jobs(), "deleted repository cron remained registered")
}

func waitForScheduledJob(t *testing.T, server *Server, repoKey string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if scheduledJobTotal(t, server, repoKey) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("repository %q was not launched at its configured cron schedule", repoKey)
}

func scheduledJobTotal(t *testing.T, server *Server, repoKey string) int64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs?repository="+repoKey, nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	require.Equal(t, http.StatusOK, resp.Code)

	var body struct {
		Data struct {
			Total int64 `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	return body.Data.Total
}
