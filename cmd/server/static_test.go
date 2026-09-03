package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	require.Contains(t, script, "btn-refresh-repos")
	require.Contains(t, string(template), "id=\"component-details\"")
	require.Contains(t, string(template), "id=\"execution-details\"")
	require.Contains(t, string(template), "id=\"execution-status\"")
	require.Contains(t, string(template), "id=\"btn-retry-execution\"")
	require.Contains(t, string(css), ".summary-grid")
	require.Contains(t, string(css), ".component-row")

	bulk := jsFunctionSection(t, script, "async function retryFilteredRepositories", "async function renderRepositories")
	require.NotContains(t, bulk, "job_ids")
	require.Contains(t, bulk, "bulkRetryAttemptIsCurrent(attempt)")
	require.Contains(t, bulk, "JSON.stringify({ selector: attempt.selector, expected_count: attempt.expectedCount })")

	render := jsFunctionSection(t, script, "async function renderRepositories", "function openStorageForm")
	require.Contains(t, render, "isActiveRepositoryRoute(routeEpoch)")
	require.Contains(t, render, "freezeBulkRetryAttempt(repositoryBulkSelector(), total, routeEpoch)")

	detail := jsFunctionSection(t, script, "async function openExecutionDetails", "function appendLogLine")
	require.Contains(t, detail, "isActiveRepositoryRoute(routeEpoch)")
	require.Contains(t, detail, "detailSerial !== state.componentDetailSerial")

	detailRetry := jsFunctionSection(t, script, "async function retryExecutionRepository", "async function openExecutionDetails")
	require.Contains(t, detailRetry, "isActiveRepositoryRoute(routeEpoch)")
	require.Contains(t, detailRetry, "if (jobIDs.length === 1)")
	require.Contains(t, detailRetry, "closeLogModal()")

	renderDetail := jsFunctionSection(t, script, "function renderExecutionDetails", "async function retryExecutionRepository")
	require.Contains(t, renderDetail, "retry.disabled = false")

	for _, name := range []string{"async function runRepo", "async function saveRepo", "async function deleteRepo"} {
		require.Contains(t, jsFunctionSection(t, script, name, "\n}"), "isActiveRepositoryRoute(routeEpoch)")
	}

	log := jsFunctionSection(t, script, "function openLogModal", "function componentBadge")
	require.NotContains(t, log, "renderApp(")
	require.NotContains(t, log, "renderRepositories(")
	require.Contains(t, log, "if (state.es !== es) return")
	for _, timer := range jsTimerCalls(script) {
		require.NotContains(t, timer, "renderRepositories")
		require.NotContains(t, timer, "renderApp")
	}
}

func jsFunctionSection(t *testing.T, script, start, end string) string {
	t.Helper()
	startAt := strings.Index(script, start)
	require.NotEqual(t, -1, startAt, start)
	endAt := strings.Index(script[startAt+len(start):], end)
	require.NotEqual(t, -1, endAt, end)
	return script[startAt : startAt+len(start)+endAt]
}

func jsTimerCalls(script string) []string {
	var calls []string
	timerStart := regexp.MustCompile(`\b(?:setInterval|setTimeout)\s*\(`)
	for _, match := range timerStart.FindAllStringIndex(script, -1) {
		start := match[0]
		open := strings.Index(script[start:match[1]], "(") + start
		depth, end := 0, open
	foundEnd:
		for ; end < len(script); end++ {
			switch script[end] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end++
					calls = append(calls, script[start:end])
					break foundEnd
				}
			}
		}
	}
	return calls
}
