package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerRootRoute(t *testing.T) {
	server := NewServer(nil)
	req, _ := http.NewRequest("GET", "/", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != 200 {
		t.Errorf("Expected status 200, got %d", resp.Code)
	}
}