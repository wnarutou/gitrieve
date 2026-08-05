package server

import (
	"fmt"
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
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

	// Static files
	s.router.Static("/static", "./web/static")
	s.router.LoadHTMLGlob("web/templates/*")

	// Main page
	s.router.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", gin.H{
			"title": "Gitrieve",
		})
	})

	// API routes
	s.router.POST("/api/jobs", api.CreateJob)
	s.router.DELETE("/api/jobs/:id", api.CancelJob)
	s.router.GET("/api/jobs", api.GetJobs)
	s.router.GET("/api/jobs/:id/logs", api.GetJobLogs)
	s.router.GET("/api/repositories", api.GetRepositories)
	s.router.POST("/api/repositories", api.CreateRepository)
	s.router.PUT("/api/repositories/:id", api.UpdateRepository)
	s.router.DELETE("/api/repositories/:id", api.DeleteRepository)
	s.router.GET("/api/storage", api.GetStorages)
	s.router.POST("/api/storage", api.CreateStorage)
	s.router.PUT("/api/storage/:id", api.UpdateStorage)
	s.router.DELETE("/api/storage/:id", api.DeleteStorage)
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
		// TODO: Implement server configuration
		serverCfg := struct{ Host, Port string }{Host: "localhost", Port: "8080"}
		addr := fmt.Sprintf("%s:%s", serverCfg.Host, serverCfg.Port)
		ui.Printf("Starting server on %s", addr)
		if err := http.ListenAndServe(addr, s); err != nil {
			ui.ErrorfExit("Server failed: %s", err)
		}
	},
}