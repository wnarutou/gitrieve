package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	internalserver "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// TestServer wraps a gin.Engine for testing
type TestServer struct {
	router *gin.Engine
}

func (s *TestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// NewTestServer creates a new server instance for testing
func NewTestServer(db *db.DB) *TestServer {
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	log := logger.NewLogger(db)
	exec := executor.NewExecutor(log, db, cfg)
	api := internalserver.NewAPI(cfg, db, exec)

	s := &TestServer{
		router: gin.Default(),
	}

	s.router.POST("/api/jobs", api.CreateJob)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)

	return s
}

// NewTestServerWithExecutor creates a new server instance with a custom executor for testing
func NewTestServerWithExecutor(db *db.DB, exec *executor.Executor) *TestServer {
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{
				Name: "test-repo",
				URL:  "github.com/test/repo",
			},
		},
	}

	api := internalserver.NewAPI(cfg, db, exec)

	s := &TestServer{
		router: gin.Default(),
	}

	s.router.POST("/api/jobs", api.CreateJob)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)

	return s
}