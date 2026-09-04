package server

// This file uses the Go export_test.go idiom: it lives in the internal test
// package (package server) so it can reference unexported identifiers, and it
// exposes test-only constructors to the external test package (package
// server_test) in the same directory. These symbols are only compiled into
// tests, never into production binaries.

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// TestServer wraps a gin.Engine for testing.
type TestServer struct {
	router *gin.Engine
	api    *API
}

func (s *TestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Cfg returns the config instance the API currently holds. Used by the
// config-reload tests to observe the in-memory config after a reload.
func (s *TestServer) Cfg() *config.Config {
	if s.api == nil {
		return nil
	}
	return s.api.configSnapshot()
}

// SetBulkRepositoryStats replaces only the bulk enqueue-time repository-stats
// read so tests can establish deterministic persistence/config barriers.
func (s *TestServer) SetBulkRepositoryStats(load func(context.Context, typedef.Repository) (map[string]db.RepositoryRunStats, error)) {
	s.api.bulkRepositoryStats = load
}

// SetReadReloadSnapshot replaces the unpublished disk-read operation so tests
// can make a deterministic edit after the read but before API publication.
func (s *TestServer) SetReadReloadSnapshot(read func() (*config.ReloadSnapshot, error)) {
	s.api.readReloadSnapshot = read
}

// SetBulkExecute replaces bulk's final Executor call for deterministic error
// and cancellation branch coverage.
func (s *TestServer) SetBulkExecute(execute func(context.Context, string, uint64) ([]string, error)) {
	s.api.bulkExecute = func(ctx context.Context, key string, generation uint64, _ executor.EligibilityCheck) ([]string, error) {
		return execute(ctx, key, generation)
	}
}

// BulkRepositoryStatsQueryPlan returns SQLite's plan for the exact production
// candidate-history query.
func (s *TestServer) BulkRepositoryStatsQueryPlan(ctx context.Context, repository typedef.Repository) (string, error) {
	query, args := repositoryRunStatsQuery(repository)
	rows, err := s.api.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			return "", err
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(details, "\n"), nil
}

// newTestConfig returns the default config used by the test servers.
func newTestConfig() *config.Config {
	return &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}
}

// NewTestServer creates a new server instance for testing with the jobs routes
// registered and a default executor.
func NewTestServer(db *db.DB) *TestServer {
	cfg := newTestConfig()
	log := logger.NewLogger(db)
	exec := executor.NewExecutor(log, db, cfg)
	api := NewAPI(cfg, db, exec)

	s := &TestServer{router: gin.Default(), api: api}
	s.router.POST("/api/jobs", api.CreateJob)
	s.router.POST("/api/jobs/bulk", api.BulkCreateJobs)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)
	s.router.GET("/api/jobs/:id/components", api.GetJobComponents)
	return s
}

// NewTestServerWithExecutor creates a new server instance with a custom
// executor for testing, with the jobs routes registered.
func NewTestServerWithExecutor(db *db.DB, exec *executor.Executor, configs ...*config.Config) *TestServer {
	cfg := newTestConfig()
	if len(configs) > 0 {
		cfg = configs[0]
	}
	api := NewAPI(cfg, db, exec)

	s := &TestServer{router: gin.Default(), api: api}
	s.router.POST("/api/jobs", api.CreateJob)
	s.router.POST("/api/jobs/bulk", api.BulkCreateJobs)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)
	s.router.GET("/api/jobs/:id/components", api.GetJobComponents)
	return s
}

// NewRepoTestServer creates a test server with only the repository CRUD
// routes registered, using a fresh executor backed by testDB.
func NewRepoTestServer(cfg *config.Config, testDB *db.DB, executors ...*executor.Executor) *TestServer {
	var exec *executor.Executor
	if len(executors) > 0 {
		exec = executors[0]
	} else {
		log := logger.NewLogger(testDB)
		exec = executor.NewExecutor(log, testDB, cfg)
	}
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default(), api: api}
	s.router.GET("/api/repositories", api.GetRepositories)
	s.router.POST("/api/repositories", api.CreateRepository)
	// *id catch-all: identity keys are URLs containing "/" that :id cannot match.
	s.router.PUT("/api/repositories/*id", api.UpdateRepository)
	s.router.DELETE("/api/repositories/*id", api.DeleteRepository)
	return s
}

// NewStorageTestServer creates a test server with only the storage CRUD
// routes registered, using a fresh executor backed by testDB.
func NewStorageTestServer(cfg *config.Config, testDB *db.DB, executors ...*executor.Executor) *TestServer {
	var exec *executor.Executor
	if len(executors) > 0 {
		exec = executors[0]
	} else {
		log := logger.NewLogger(testDB)
		exec = executor.NewExecutor(log, testDB, cfg)
	}
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default(), api: api}
	s.router.GET("/api/storage", api.GetStorages)
	s.router.POST("/api/storage", api.CreateStorage)
	s.router.PUT("/api/storage/:id", api.UpdateStorage)
	s.router.DELETE("/api/storage/:id", api.DeleteStorage)
	return s
}

// NewConfigTestServer creates a test server with the config export + preview
// routes registered, using a fresh executor backed by testDB. Task 4 adds the
// apply + reload routes to the same constructor.
func NewConfigTestServer(cfg *config.Config, testDB *db.DB, refreshers ...ScheduleRefresher) *TestServer {
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	api := NewAPI(cfg, testDB, exec)
	if len(refreshers) > 0 {
		api.SetScheduleRefresher(refreshers[0])
	}
	s := &TestServer{router: gin.Default(), api: api}
	s.router.GET("/api/config/export", api.ExportConfig)
	s.router.POST("/api/config/import/preview", api.PreviewImport)
	s.router.POST("/api/config/import", api.ApplyImport)
	s.router.POST("/api/config/reload", api.ReloadConfig)
	return s
}

// NewConfigConcurrencyTestServer exposes every route involved in concurrent
// config publication through one API and one Executor instance.
func NewConfigConcurrencyTestServer(cfg *config.Config, testDB *db.DB, exec *executor.Executor) *TestServer {
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default(), api: api}
	s.router.POST("/api/jobs/bulk", api.BulkCreateJobs)
	s.router.POST("/api/repositories", api.CreateRepository)
	s.router.PUT("/api/repositories/*id", api.UpdateRepository)
	s.router.DELETE("/api/repositories/*id", api.DeleteRepository)
	s.router.POST("/api/storage", api.CreateStorage)
	s.router.PUT("/api/storage/:id", api.UpdateStorage)
	s.router.DELETE("/api/storage/:id", api.DeleteStorage)
	s.router.POST("/api/config/import", api.ApplyImport)
	s.router.POST("/api/config/reload", api.ReloadConfig)
	return s
}
