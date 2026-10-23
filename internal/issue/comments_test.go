package issue

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type commentRoundTripper func(*http.Request) (*http.Response, error)

func (fn commentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type commentLogSink struct{ messages []string }

func (s *commentLogSink) Log(_, _, _, message string) error {
	s.messages = append(s.messages, message)
	return nil
}

// Exercise Sync through go-github's real response decoding and error handling.
// These tests replace process-wide HTTP/logging state and must stay serial.
func setupCommentSync(t *testing.T, respond func(*http.Request) *http.Response) (context.Context, string, *commentLogSink) {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	previousTransport := http.DefaultTransport
	http.DefaultTransport = commentRoundTripper(func(req *http.Request) (*http.Response, error) {
		return respond(req), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	githubapi.Configure(githubapi.Config{Concurrency: 1})

	sink := &commentLogSink{}
	ui.SetSink(sink)
	t.Cleanup(func() { ui.SetSink(nil) })
	t.Cleanup(ui.Bind("execution-1", "issues"))

	ctx := config.WithExecutionSnapshot(context.Background(), config.NewExecutionSnapshot(&config.Config{
		RetryMaxCount: 3, RetryBaseDelay: time.Millisecond,
	}))
	return ctx, filepath.Join(".gitrieve", "github.com", "test", "repo", "issues"), sink
}

func commentResponse(req *http.Request, status int, body, requestID string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}, "X-Github-Request-Id": {requestID}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestSyncCommentNotFoundRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statuses   []int
		wantStatus int
		maxRetries int
	}{
		{"recovers_on_first_retry", []int{404, 200}, 0, 0},
		{"recovers_on_second_retry", []int{404, 404, 200}, 0, 0},
		{"stops_after_two_retries", []int{404, 404, 404}, 404, 5},
		{"forbidden_is_not_retried", []int{403}, 403, 3},
		{"existing_server_error_retry", []int{500, 200}, 0, 1},
		{"server_error_does_not_reset_404_budget", []int{404, 500, 404, 404}, 404, 3},
		{"not_found_does_not_reset_server_error_budget", []int{500, 404, 500}, 500, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts []time.Time
			laterIssueFetched := false
			ctx, cacheDir, sink := setupCommentSync(t, func(req *http.Request) *http.Response {
				switch req.URL.Path {
				case "/repos/test/repo/issues":
					return commentResponse(req, 200, `[{"number":16,"title":"first"},{"number":17,"title":"later"}]`, "list")
				case "/repos/test/repo/issues/16/comments":
					attempts = append(attempts, time.Now())
					require.LessOrEqual(t, len(attempts), len(tc.statuses), "unexpected extra comment request")
					status := tc.statuses[len(attempts)-1]
					body := `{"message":"failed"}`
					if status == 200 {
						body = `[{"id":42,"body":"saved comment"}]`
					}
					return commentResponse(req, status, body, fmt.Sprintf("comments-%d", len(attempts)))
				case "/repos/test/repo/issues/17/comments":
					laterIssueFetched = true
					return commentResponse(req, 200, `[]`, "later")
				default:
					t.Fatalf("unexpected request: %s", req.URL)
					return nil
				}
			})
			ctx = config.WithExecutionSnapshot(ctx, config.NewExecutionSnapshot(&config.Config{
				RetryMaxCount: tc.maxRetries, RetryBaseDelay: time.Millisecond,
			}))
			storageDir := t.TempDir()
			archivePath := filepath.Join(storageDir, "github.com", "test", "repo", "issues.tar.gz")
			err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo", UseCache: true}, []typedef.MultiStorage{{
				Storage: typedef.Storage{Type: "file", Path: storageDir},
			}})
			require.Len(t, attempts, len(tc.statuses))
			if tc.wantStatus != 0 {
				var apiErr *gh.ErrorResponse
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, tc.wantStatus, apiErr.Response.StatusCode)
				require.False(t, laterIssueFetched)
				require.NoFileExists(t, filepath.Join(cacheDir, "#16.md"))
				require.NoFileExists(t, archivePath, "an incomplete backup must not be published")
			} else {
				require.NoError(t, err)
				require.True(t, laterIssueFetched)
				content, readErr := os.ReadFile(filepath.Join(cacheDir, "#16.md"))
				require.NoError(t, readErr)
				require.Contains(t, string(content), "saved comment")
				require.Contains(t, string(content), "- Comment Count: 1")
				require.FileExists(t, archivePath)
			}
			logs := strings.Join(sink.messages, "\n")
			for i, status := range tc.statuses {
				if status != 200 {
					require.Contains(t, logs, fmt.Sprintf("comments-%d", i+1), "failed requests must log their GitHub Request ID")
				}
			}
			if tc.name == "recovers_on_second_retry" {
				require.GreaterOrEqual(t, attempts[1].Sub(attempts[0]), time.Second)
				require.GreaterOrEqual(t, attempts[2].Sub(attempts[1]), 3*time.Second)
			}
		})
	}
}

func TestSyncCommentNotFoundWaitCanBeCancelled(t *testing.T) {
	calls := 0
	ctx, cacheDir, _ := setupCommentSync(t, func(req *http.Request) *http.Response {
		if req.URL.Path == "/repos/test/repo/issues" {
			return commentResponse(req, 200, `[{"number":16}]`, "list")
		}
		calls++
		return commentResponse(req, 404, `{"message":"Not Found"}`, "cancelled-request")
	})
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo", UseCache: true}, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 750*time.Millisecond)
	require.Equal(t, 1, calls)
	require.NoFileExists(t, filepath.Join(cacheDir, "#16.md"))

	// The HTTP permit must be released before the retry wait.
	permitCtx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	permit, err := githubapi.Acquire(permitCtx, "core")
	require.NoError(t, err)
	permit.Done(githubapi.Observation{})
}

func TestSyncCommentNotFoundRetryPreservesPagination(t *testing.T) {
	var pages []string
	ctx, cacheDir, _ := setupCommentSync(t, func(req *http.Request) *http.Response {
		if req.URL.Path == "/repos/test/repo/issues" {
			return commentResponse(req, 200, `[{"number":16}]`, "list")
		}
		page := req.URL.Query().Get("page")
		pages = append(pages, page)
		if page == "" {
			resp := commentResponse(req, 200, `[{"id":1,"body":"first page"}]`, "page-1")
			resp.Header.Set("Link", `<https://api.github.com/repos/test/repo/issues/16/comments?page=2&per_page=100>; rel="next"`)
			return resp
		}
		if len(pages) == 2 {
			return commentResponse(req, 404, `{"message":"Not Found"}`, "page-2-missing")
		}
		return commentResponse(req, 200, `[{"id":2,"body":"second page"}]`, "page-2")
	})
	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo", UseCache: true}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"", "2", "2"}, pages)
	content, err := os.ReadFile(filepath.Join(cacheDir, "#16.md"))
	require.NoError(t, err)
	require.Contains(t, string(content), "- Comment Count: 2")
	require.Contains(t, string(content), "first page")
	require.Contains(t, string(content), "second page")
}

func TestSyncIssueListNotFoundIsNotRetried(t *testing.T) {
	calls := 0
	ctx, _, _ := setupCommentSync(t, func(req *http.Request) *http.Response {
		calls++
		return commentResponse(req, 404, `{"message":"Not Found"}`, "missing-repo")
	})
	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo", UseCache: true}, nil)
	var apiErr *gh.ErrorResponse
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 404, apiErr.Response.StatusCode)
	require.Equal(t, 1, calls)
}

func TestSyncCommentNotFoundRetryUsesAccountPool(t *testing.T) {
	var auth []string
	ctx, _, _ := setupCommentSync(t, func(req *http.Request) *http.Response {
		auth = append(auth, req.Header.Get("Authorization"))
		if req.URL.Path == "/repos/test/repo/issues" {
			return commentResponse(req, 200, `[{"number":16}]`, "list")
		}
		if len(auth) == 2 {
			return commentResponse(req, 404, `{"message":"Not Found"}`, "pool-missing")
		}
		return commentResponse(req, 200, `[]`, "pool-success")
	})
	ctx = config.WithExecutionSnapshot(ctx, config.NewExecutionSnapshot(&config.Config{
		GitHubAccounts: []githubapi.Account{{Name: "a", Token: "token-a"}, {Name: "b", Token: "token-b"}},
	}))
	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo", UseCache: true}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"Bearer token-a", "Bearer token-b", "Bearer token-a"}, auth)
}

func TestSyncCommentPoolQuotaDeadlineTakesPriorityOverNotFound(t *testing.T) {
	var auth []string
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	ctx, _, sink := setupCommentSync(t, func(req *http.Request) *http.Response {
		auth = append(auth, req.Header.Get("Authorization"))
		if req.URL.Path == "/repos/test/repo/issues" {
			return commentResponse(req, 200, `[{"number":16}]`, "list")
		}
		if req.Header.Get("Authorization") == "Bearer missing-b" {
			return commentResponse(req, 404, `{"message":"Not Found"}`, "missing")
		}
		resp := commentResponse(req, 403, `{"message":"API rate limit exceeded"}`, "limited")
		resp.Header.Set("X-RateLimit-Remaining", "0")
		resp.Header.Set("X-RateLimit-Resource", "core")
		resp.Header.Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		return resp
	})
	ctx = config.WithExecutionSnapshot(ctx, config.NewExecutionSnapshot(&config.Config{
		GitHubAccounts: []githubapi.Account{{Name: "a", Token: "quota-a"}, {Name: "b", Token: "missing-b"}},
		RetryMaxCount:  0,
	}))
	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo", UseCache: true}, nil)
	var deadline interface{ RetryAt() time.Time }
	require.ErrorAs(t, err, &deadline)
	require.Equal(t, reset, deadline.RetryAt())
	require.Equal(t, []string{"Bearer quota-a", "Bearer missing-b", "Bearer quota-a"}, auth,
		"quota exhaustion must return to the existing retry policy without spending the 404 allowance")
	require.NotContains(t, strings.Join(sink.messages, "\n"), "Retrying comments")
}
