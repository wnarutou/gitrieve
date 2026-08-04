package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/wnarutou/gitrieve/internal/db"
)

func TestGetJobsAPI(t *testing.T) {
	// Setup test database with mock data
	db, _ := db.Initialize(":memory:")

	server := NewTestServer(db)
	req, _ := http.NewRequest("GET", "/api/jobs", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != 200 {
		t.Errorf("Expected status 200, got %d", resp.Code)
	}
}