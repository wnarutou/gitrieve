package server

import (
	"fmt"
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type Server struct {
	router *gin.Engine
}

func NewServer(cfg *config.Config) *Server {
	s := &Server{
		router: gin.Default(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.router.GET("/", func(c *gin.Context) {
		c.String(200, "Gitrieve Web UI")
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
		serverCfg := server.GetServerConfig()
		addr := fmt.Sprintf("%s:%s", serverCfg.Host, serverCfg.Port)
		ui.Printf("Starting server on %s", addr)
		if err := http.ListenAndServe(addr, s); err != nil {
			ui.ErrorfExit("Server failed: %s", err)
		}
	},
}