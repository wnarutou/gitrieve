package server

// This file uses the Go export_test.go idiom: it lives in the internal test
// package (package server) so it can reference unexported identifiers, and it
// exposes test-only constructors to the external test package (package
// server_test) in the same directory. These symbols are only compiled into
// tests, never into production binaries.

import (
	"net/http"

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
	return s.api.config
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

	s := &TestServer{router: gin.Default()}
	s.router.POST("/api/jobs", api.CreateJob)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)
	return s
}

// NewTestServerWithExecutor creates a new server instance with a custom
// executor for testing, with the jobs routes registered.
func NewTestServerWithExecutor(db *db.DB, exec *executor.Executor) *TestServer {
	cfg := newTestConfig()
	api := NewAPI(cfg, db, exec)

	s := &TestServer{router: gin.Default()}
	s.router.POST("/api/jobs", api.CreateJob)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)
	return s
}

// NewRepoTestServer creates a test server with only the repository CRUD
// routes registered, using a fresh executor backed by testDB.
func NewRepoTestServer(cfg *config.Config, testDB *db.DB) *TestServer {
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default()}
	s.router.GET("/api/repositories", api.GetRepositories)
	s.router.POST("/api/repositories", api.CreateRepository)
	// *id catch-all: identity keys are URLs containing "/" that :id cannot match.
	s.router.PUT("/api/repositories/*id", api.UpdateRepository)
	s.router.DELETE("/api/repositories/*id", api.DeleteRepository)
	return s
}

// NewStorageTestServer creates a test server with only the storage CRUD
// routes registered, using a fresh executor backed by testDB.
func NewStorageTestServer(cfg *config.Config, testDB *db.DB) *TestServer {
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default()}
	s.router.GET("/api/storage", api.GetStorages)
	s.router.POST("/api/storage", api.CreateStorage)
	s.router.PUT("/api/storage/:id", api.UpdateStorage)
	s.router.DELETE("/api/storage/:id", api.DeleteStorage)
	return s
}

// NewConfigTestServer creates a test server with the config export + preview
// routes registered, using a fresh executor backed by testDB. Task 4 adds the
// apply + reload routes to the same constructor.
func NewConfigTestServer(cfg *config.Config, testDB *db.DB) *TestServer {
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default(), api: api}
	s.router.GET("/api/config/export", api.ExportConfig)
	s.router.POST("/api/config/import/preview", api.PreviewImport)
	return s
}
