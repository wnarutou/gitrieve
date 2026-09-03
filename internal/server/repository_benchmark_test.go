package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

const (
	benchmarkRepositoryCount = 6000
	benchmarkHistoryCount    = 100
)

func BenchmarkGetRepositories6000(b *testing.B) {
	b.StopTimer()
	testDB, err := db.Initialize(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = testDB.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	repositories := make([]typedef.Repository, benchmarkRepositoryCount)
	for repositoryIndex := range repositories {
		repositories[repositoryIndex] = typedef.Repository{
			Name: fmt.Sprintf("repository-%04d", repositoryIndex),
			URL:  fmt.Sprintf("github.com/benchmark/repository-%04d", repositoryIndex),
			Cron: "0 * * * *",
		}
	}

	tx, err := testDB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`
		INSERT INTO executions
			(id, job_name, repo_key, start_time, end_time, status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	for repositoryIndex, repository := range repositories {
		for historyIndex := 0; historyIndex < benchmarkHistoryCount; historyIndex++ {
			start := now.Add(-time.Duration(benchmarkHistoryCount-historyIndex) * time.Hour)
			end := start.Add(5 * time.Minute)
			status := "completed"
			message := ""
			if historyIndex == benchmarkHistoryCount-1 {
				switch repositoryIndex % 4 {
				case 0:
					status = "failed"
					message = "benchmark failure"
				case 3:
					status = "cancelled"
					message = "benchmark cancellation"
				}
			}
			id := fmt.Sprintf("benchmark-%04d-%03d", repositoryIndex, historyIndex)
			if _, err := statement.Exec(id, repository.Name, repository.Key(), start, end, status, message); err != nil {
				_ = statement.Close()
				_ = tx.Rollback()
				b.Fatal(err)
			}
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	handler := server.NewRepoTestServer(&config.Config{
		SyncOverdueGrace:   30 * time.Minute,
		SyncStuckThreshold: 24 * time.Hour,
		Repository:         repositories,
	}, testDB)

	benchmarks := []struct {
		name  string
		query string
	}{
		{name: "attention", query: "?sort=attention&page=1&limit=20"},
		{name: "failed", query: "?health=failed&sort=attention&page=1&limit=20"},
		{name: "overdue", query: "?overdue=true&sort=attention&page=1&limit=20"},
		{name: "last_success", query: "?sort=last_success&direction=desc&page=1&limit=20"},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				request := httptest.NewRequest(http.MethodGet, "/api/repositories"+benchmark.query, nil)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					b.Fatalf("GET %s returned HTTP %d: %s", request.URL.RequestURI(), response.Code, response.Body.String())
				}
				var body struct {
					Code int `json:"code"`
					Data struct {
						Repositories []json.RawMessage `json:"repositories"`
					} `json:"data"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					b.Fatalf("decode GET %s response: %v", request.URL.RequestURI(), err)
				}
				if body.Code != http.StatusOK || len(body.Data.Repositories) == 0 || len(body.Data.Repositories) > 20 {
					b.Fatalf("GET %s returned invalid payload: code=%d repositories=%d", request.URL.RequestURI(), body.Code, len(body.Data.Repositories))
				}
			}
		})
	}
}
