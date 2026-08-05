package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/gin-gonic/gin"
)

type testServer struct {
	router *gin.Engine
}

func (s *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func TestStaticFileServing(t *testing.T) {
	// Create a test server with static file serving
	router := gin.New()

	// Add static file serving
	router.Static("/static", "/workspaces/gitrieve/web/static")

	// Add main route
	router.GET("/", func(c *gin.Context) {
		c.String(200, "Gitrieve Web UI")
	})

	server := &testServer{
		router: router,
	}

	req, _ := http.NewRequest("GET", "/static/css/main.css", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != 200 {
		t.Errorf("Expected status 200, got %d", resp.Code)
	}
}