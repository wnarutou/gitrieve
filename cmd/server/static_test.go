package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticAssetServed(t *testing.T) {
	server := NewServer(nil)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
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
