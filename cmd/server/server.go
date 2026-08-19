package server

import (
	"fmt"
	"html/template"
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
	"github.com/wnarutou/gitrieve/web"
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
	// Initialize database — path is configurable via `server.dbPath` so the
	// SQLite file (which also holds job history and logs) can be placed on a
	// mounted volume for persistence, backup and migration.
	serverCfg := internalserver.GetServerConfig()
	database, err := db.Initialize(serverCfg.DbPath)
	if err != nil {
		ui.ErrorfExit("Failed to initialize database: %s", err)
	}

	// 迁移旧库（新增 repo_key 列）。失败宁可起不来，也不在坏 schema 上跑。
	if err := db.Migrate(database); err != nil {
		ui.ErrorfExit("Failed to migrate database: %s", err)
	}

	// Initialize logger
	log := logger.NewLogger(database)

	// Initialize executor
	exec := executor.NewExecutor(log, database, cfg)

	// Initialize API
	api := internalserver.NewAPI(cfg, database, exec)

	// Static files and templates are embedded in the binary (see package web),
	// so the server works from any working directory.
	tmpl, err := template.ParseFS(web.TemplatesFS, "templates/*")
	if err != nil {
		ui.ErrorfExit("Failed to load templates: %s", err)
	}
	s.router.SetHTMLTemplate(tmpl)
	s.router.StaticFS("/static", http.FS(web.StaticFS))

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
	// *id catch-all: the identity key is a URL like github.com/owner/repo and
	// contains "/", so a single-segment :id param cannot match it. gin prefixes
	// the captured value with "/" (trimmed inside the handlers).
	apiGroup.PUT("/api/repositories/*id", api.UpdateRepository)
	apiGroup.DELETE("/api/repositories/*id", api.DeleteRepository)
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