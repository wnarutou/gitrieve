package server

import (
	"fmt"
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/auth"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/monitoring"
	internalserver "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type Server struct {
	router *gin.Engine
}

func NewServer(cfg *config.Config) *Server {
	s := &Server{
		router: gin.Default(),
	}
	s.setupRoutes(cfg)
	return s
}

func NewTestServer(db *db.DB) *Server {
	s := &Server{
		router: gin.Default(),
	}
	s.setupTestRoutes(db)
	return s
}

func (s *Server) setupRoutes(cfg *config.Config) {
	// Initialize database
	database, err := db.Initialize("gitrieve.db")
	if err != nil {
		ui.ErrorfExit("Failed to initialize database: %s", err)
	}

	// Initialize logger
	log := logger.NewLogger(database)

	// Initialize executor
	exec := executor.NewExecutor(log, database, cfg)

	// Initialize API
	api := internalserver.NewAPI(cfg, database, exec)

	// Static files (public)
	s.router.Static("/static", "./web/static")
	s.router.LoadHTMLGlob("web/templates/*")

	// Main page (public)
	s.router.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", gin.H{
			"title": "Gitrieve",
		})
	})

	// Monitoring: health check is always public
	monitor := monitoring.NewMonitor()
	s.router.GET("/health", monitor.HealthCheck)

	// API routes — protected by auth when enabled
	var apiGroup *gin.RouterGroup
	serverCfg := internalserver.GetServerConfig()
	if serverCfg.AuthEnabled && serverCfg.AuthToken != "" {
		authMW := auth.NewAuthMiddleware(serverCfg.AuthToken)
		apiGroup = s.router.Group("/", authMW.Middleware())
	} else {
		apiGroup = s.router.Group("/")
	}

	apiGroup.POST("/api/jobs", api.CreateJob)
	apiGroup.DELETE("/api/jobs/:id", api.CancelJob)
	apiGroup.GET("/api/jobs", api.GetJobs)
	apiGroup.GET("/api/jobs/:id/logs", api.GetJobLogs)
	apiGroup.GET("/api/repositories", api.GetRepositories)
	apiGroup.POST("/api/repositories", api.CreateRepository)
	apiGroup.PUT("/api/repositories/:id", api.UpdateRepository)
	apiGroup.DELETE("/api/repositories/:id", api.DeleteRepository)
	apiGroup.GET("/api/storage", api.GetStorages)
	apiGroup.POST("/api/storage", api.CreateStorage)
	apiGroup.PUT("/api/storage/:id", api.UpdateStorage)
	apiGroup.DELETE("/api/storage/:id", api.DeleteStorage)
	apiGroup.GET("/api/metrics", monitor.GetMetrics)
}

func (s *Server) setupTestRoutes(db *db.DB) {
	s.router.GET("/api/jobs", func(c *gin.Context) {
		api := internalserver.NewAPI(&config.Config{}, db, nil)
		api.GetJobs(c)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

var Cmd = &cobra.Command{
	Use:   "server",
	Short: "start web server",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.GetIns()
		s := NewServer(cfg)
		serverCfg := internalserver.GetServerConfig()
		addr := fmt.Sprintf("%s:%s", serverCfg.Host, serverCfg.Port)
		ui.Printf("Starting server on %s", addr)
		if err := http.ListenAndServe(addr, s); err != nil {
			ui.ErrorfExit("Server failed: %s", err)
		}
	},
}