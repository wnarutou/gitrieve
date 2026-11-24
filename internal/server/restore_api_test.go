package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/archive"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func writeRestoreAPIBackup(t *testing.T, root string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "demo")
	_, err := git.PlainInit(source, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(source, "README.md"), []byte("backup\n"), 0o644))
	buf, err := archive.Create(context.Background(), source, "demo")
	require.NoError(t, err)
	defer buf.Close()
	filename := filepath.Join(root, "github.com", "acme", "demo", "demo.tar.gz")
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	destination, err := os.Create(filename)
	require.NoError(t, err)
	_, err = io.Copy(destination, buf)
	require.NoError(t, err)
	require.NoError(t, destination.Close())
}

func TestRestoreAPIScansStorageWithoutConfiguredRepositories(t *testing.T) {
	base := t.TempDir()
	storageRoot := filepath.Join(base, "repo")
	currentDir := filepath.Join(base, "app")
	writeRestoreAPIBackup(t, storageRoot)

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testDB.Close()) })
	cfg := &config.Config{
		Storage: []typedef.MultiStorage{{
			Storage: typedef.Storage{Name: "backup", Type: "file", Path: storageRoot},
		}},
		// Deliberately no Repository entries: restore discovers the backup tree.
	}
	s := server.NewRestoreTestServer(cfg, testDB, currentDir)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	req := httptest.NewRequest(http.MethodPost, "/api/restore/scan", bytes.NewBufferString(`{"storage_name":"backup"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	var scanResponse struct {
		Data struct {
			TotalRepositories int `json:"total_repositories"`
			Repositories      []struct {
				Key        string `json:"key"`
				Configured bool   `json:"configured"`
			} `json:"repositories"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &scanResponse))
	require.Equal(t, 1, scanResponse.Data.TotalRepositories)
	require.Equal(t, "github.com/acme/demo", scanResponse.Data.Repositories[0].Key)
	require.False(t, scanResponse.Data.Repositories[0].Configured)

	req = httptest.NewRequest(http.MethodPost, "/api/restore", bytes.NewBufferString(`{"storage_name":"backup"}`))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	s.ServeHTTP(resp, req)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	var startResponse struct {
		Data struct {
			JobIDs []string `json:"job_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &startResponse))
	require.Len(t, startResponse.Data.JobIDs, 1)
	jobID := startResponse.Data.JobIDs[0]

	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		require.NoError(t, testDB.QueryRow(`SELECT status FROM executions WHERE id = ?`, jobID).Scan(&status))
		if status == "completed" || status == "failed" || status == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, "completed", status)
	_, err = os.Stat(filepath.Join(
		currentDir, ".gitrieve", "github.com", "acme", "demo", "code", ".git", "HEAD",
	))
	require.NoError(t, err)
}
