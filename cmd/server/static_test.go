package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticAssetServed(t *testing.T) {
	server := NewServer(nil)
	// NewServer initializes a real DB at server.dbPath default (gitrieve.db).
	// Cleaned up implicitly by test cwd.

	for _, path := range []string{"/static/css/main.css", "/static/js/main.js"} {
		req, _ := http.NewRequest("GET", path, nil)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != 200 {
			t.Errorf("GET %s: expected 200, got %d", path, resp.Code)
		}
	}
}
