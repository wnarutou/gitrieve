package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
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
	require.Contains(t, script, "function routeFromHash()")
	require.Contains(t, script, "function applyRepositoryRoute(hash)")
	require.Contains(t, script, "function repositorySummaryCards(summary)")
	require.Contains(t, script, "function retryFilteredRepositories(")
	require.Contains(t, script, "function openExecutionDetails(executionID, name, repository)")
	require.Contains(t, script, "data.summary")
	require.Contains(t, script, "data.total")
	require.Contains(t, script, "repos-health")
	require.Contains(t, script, "repos-overdue")
	require.Contains(t, script, "repos-sort")
	require.Contains(t, script, "repos-direction")
	require.Contains(t, script, "btn-retry-filtered")
	require.Contains(t, script, "btn-execution-details")
	require.NotContains(t, script, "expected_count: repos.length")
	require.Contains(t, script, "JSON.stringify({ selector: attempt.selector, expected_count: attempt.expectedCount })")
	require.Contains(t, script, "e.data.actual_count")
	require.Contains(t, script, "function selectorsEqual(left, right)")
	require.Contains(t, script, "routeEpoch")
	require.Contains(t, script, "activeRoute")
	require.Contains(t, script, "function isActiveRepositoryRoute(routeEpoch)")
	require.Contains(t, script, "routeEpoch !== state.routeEpoch")
	require.Contains(t, script, "if (state.es !== es) return")
	require.Equal(t, 1, strings.Count(script, "setInterval("))
	require.Contains(t, script, "setInterval(refreshMetrics, 15000)")
	require.Contains(t, script, "btn-refresh-repos")
	require.Contains(t, string(template), "id=\"component-details\"")
	require.Contains(t, string(template), "id=\"execution-details\"")
	require.Contains(t, string(template), "id=\"execution-status\"")
	require.Contains(t, string(template), "id=\"btn-retry-execution\"")
	require.Contains(t, string(css), ".summary-grid")
	require.Contains(t, string(css), ".component-row")

	bulkStart := strings.Index(script, "async function retryFilteredRepositories")
	bulkEnd := strings.Index(script, "async function renderRepositories")
	require.NotEqual(t, -1, bulkStart)
	require.Greater(t, bulkEnd, bulkStart)
	require.NotContains(t, script[bulkStart:bulkEnd], "job_ids")

	logStart := strings.Index(script, "function openLogModal")
	logEnd := strings.Index(script, "function componentBadge")
	require.NotEqual(t, -1, logStart)
	require.Greater(t, logEnd, logStart)
	require.NotContains(t, script[logStart:logEnd], "renderApp(")
}
