package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/web"
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

// TestRepositoryHealthAssets protects the operator-facing repository view from
// regressing into page-sized bulk retry or completion-driven refresh behavior.
func TestRepositoryHealthAssets(t *testing.T) {
	js, err := fs.ReadFile(web.StaticFS, "js/main.js")
	require.NoError(t, err)
	css, err := fs.ReadFile(web.StaticFS, "css/main.css")
	require.NoError(t, err)
	template, err := fs.ReadFile(web.TemplatesFS, "templates/index.html")
	require.NoError(t, err)

	script := string(js)
	require.Contains(t, script, "function repositoryParams()")
	require.Contains(t, script, "function setRepositoryRoute(next)")
	require.Contains(t, script, "function repositorySummaryCards(summary)")
	require.Contains(t, script, "function retryFilteredRepositories(")
	require.Contains(t, script, "function openExecutionDetails(executionID, name)")
	require.Contains(t, script, "data.total")
	require.NotContains(t, script, "expected_count: repos.length")
	require.NotRegexp(t, `setInterval\([^)]*(renderRepositories|renderApp)`, script)
	require.NotContains(t, script, "if (state.logJob === jobId) renderApp()")
	require.Contains(t, script, "btn-refresh-repos")
	require.Contains(t, string(template), "id=\"component-details\"")
	require.Contains(t, string(css), ".summary-grid")
	require.Contains(t, string(css), ".component-row")
}
