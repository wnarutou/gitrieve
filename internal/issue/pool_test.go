package issue

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/githubapi/githubapitest"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

func TestPoolIssueAndCommentPagination(t *testing.T) {
	for _, failure := range []string{"none", "primary", "permission", "not-found", "comments-not-found"} {
		t.Run(failure, func(t *testing.T) {
			ctx, tokens := githubapitest.Context(t)
			sink := &commentLogSink{}
			ui.SetSink(sink)
			t.Cleanup(func() { ui.SetSink(nil) })
			t.Cleanup(ui.Bind("issue-account-logs", t.Name()))
			var auth, pages []string
			githubapitest.Local(t, func(w http.ResponseWriter, r *http.Request) {
				auth = append(auth, r.Header.Get("Authorization"))
				pages = append(pages, r.URL.Path+"?"+r.URL.Query().Get("page"))
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-GitHub-Request-Id", fmt.Sprintf("request-%d", len(auth)))
				if failure == "comments-not-found" && len(auth) == 2 {
					w.WriteHeader(404)
					fmt.Fprint(w, `{"message":"Not Found"}`)
					return
				}
				if len(auth) == 1 && failure != "none" && failure != "comments-not-found" {
					if failure == "primary" {
						w.Header().Set("X-RateLimit-Remaining", "0")
						w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
						w.Header().Set("X-RateLimit-Resource", "core")
					}
					status := 403
					if failure == "not-found" {
						status = 404
					}
					w.WriteHeader(status)
					fmt.Fprint(w, `{"message":"forbidden"}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/comments") {
					if r.URL.Query().Get("page") == "2" {
						fmt.Fprint(w, `[{"id":2,"body":"second comment"}]`)
						return
					}
					w.Header().Set("Link", `<https://api.github.com/repos/pool-issue/repo/issues/1/comments?page=2>; rel="next"`)
					fmt.Fprint(w, `[{"id":1,"body":"first comment"}]`)
					return
				}
				if r.URL.Query().Get("page") == "2" {
					require.Equal(t, "next-cursor", r.URL.Query().Get("before"), "account selection must preserve the full pagination link")
					fmt.Fprint(w, `[]`)
					return
				}
				w.Header().Set("Link", `<https://api.github.com/repos/pool-issue/repo/issues?page=2&before=next-cursor>; rel="next"`)
				fmt.Fprint(w, `[{"number":1,"title":"hello","updated_at":"2026-10-12T00:00:00Z"}]`)
			})
			cache := filepath.Join(".gitrieve", "github.com", "pool-issue", "repo")
			t.Cleanup(func() { _ = os.RemoveAll(cache) })
			require.NoError(t, Sync(ctx, typedef.Repository{URL: "github.com/pool-issue/repo", UseCache: true}, nil))
			data, err := os.ReadFile(filepath.Join(cache, "issues", "#1.md"))
			require.NoError(t, err)
			require.Contains(t, string(data), "- Comment Count: 2")
			require.Equal(t, 1, strings.Count(string(data), "first comment"))
			require.Equal(t, 1, strings.Count(string(data), "second comment"))
			a, b := "Bearer "+tokens[0], "Bearer "+tokens[1]
			switch failure {
			case "none":
				require.Equal(t, []string{a, b, a, b}, auth)
			case "primary":
				require.Equal(t, []string{a, b, b, b, b}, auth)
			case "permission", "not-found":
				require.Equal(t, []string{a, b, a, b, a}, auth)
			case "comments-not-found":
				require.Equal(t, []string{a, b, a, b, a}, auth)
			}
			require.Equal(t, "/repos/pool-issue/repo/issues?2", pages[len(pages)-1])
			require.Contains(t, pages, "/repos/pool-issue/repo/issues/1/comments?2")
			logs := strings.Join(sink.messages, "\n")
			require.Contains(t, logs, "Issues request for pool-issue/repo batch 1 using credential 1/2")
			require.Contains(t, logs, "Issues request for pool-issue/repo batch 2 using credential")
			require.Contains(t, logs, "Comments request for pool-issue/repo issue #1 page 2 using credential")
			if failure == "none" {
				require.Contains(t, logs, "Comments request for pool-issue/repo issue #1 page 1 using credential 2/2: succeeded (HTTP 200; GitHub Request ID: request-2)")
			} else if failure == "comments-not-found" {
				require.Contains(t, logs, "Comments request for pool-issue/repo issue #1 page 1 using credential 2/2: failed (HTTP 404; GitHub Request ID: request-2)")
				require.Contains(t, logs, "Comments request for pool-issue/repo issue #1 page 1 using credential 1/2: succeeded (HTTP 200; GitHub Request ID: request-3)")
			} else {
				require.Contains(t, logs, "Issues request for pool-issue/repo batch 1 using credential 1/2: failed (HTTP")
				require.Contains(t, logs, "Issues request for pool-issue/repo batch 1 using credential 2/2: succeeded (HTTP 200; GitHub Request ID: request-2)")
			}
			for _, token := range tokens {
				require.NotContains(t, logs, token)
			}
		})
	}
}
