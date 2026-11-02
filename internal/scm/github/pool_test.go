package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/githubapi/githubapitest"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type accountLogSink struct{ messages []string }

func (s *accountLogSink) Log(_, _, _, message string) error {
	s.messages = append(s.messages, message)
	return nil
}

// Exercise real REST 404 responses and the execution log sink together: the
// final error alone cannot distinguish one attempt from an exhausted pool.
func TestReleaseAccountFallbackLogsActualAttempts(t *testing.T) {
	for _, allDenied := range []bool{false, true} {
		t.Run(fmt.Sprint(allDenied), func(t *testing.T) {
			ctx, tokens := githubapitest.Context(t)
			sink := &accountLogSink{}
			ui.SetSink(sink)
			defer ui.SetSink(nil)
			defer ui.Bind("release-fallback", t.Name())()
			var seen []string
			githubapitest.Local(t, func(w http.ResponseWriter, r *http.Request) {
				credential := r.Header.Get("Authorization")
				seen = append(seen, credential)
				w.Header().Set("Content-Type", "application/json")
				if allDenied || credential == "Bearer "+tokens[0] {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"message":"Not Found"}`)
					return
				}
				fmt.Fprint(w, `[{"id":42,"tag_name":"v1"}]`)
			})
			client, err := NewWithContext(ctx)
			require.NoError(t, err)
			releases, err := client.GetReleases(ctx, "wnarutou", "lottery")
			if allDenied {
				require.ErrorContains(t, err, "404 Not Found")
			} else {
				require.NoError(t, err)
				require.Len(t, releases, 1)
				require.Equal(t, "v1", releases[0].GetTagName())
			}
			require.Equal(t, []string{"Bearer " + tokens[0], "Bearer " + tokens[1]}, seen)
			logs := strings.Join(sink.messages, "\n")
			require.Contains(t, logs, "GitHub API (core): credential 1/2 unavailable for this request")
			require.Contains(t, logs, "GitHub API (core): trying credential 2/2")
			if allDenied {
				require.Contains(t, logs, "GitHub API (core): request failed after trying 2/2 distinct credentials")
				require.NotContains(t, logs, "credential 2/2 succeeded")
			} else {
				require.Contains(t, logs, "GitHub API (core): credential 2/2 succeeded")
			}
			for _, token := range tokens {
				require.NotContains(t, logs, token)
			}
		})
	}
}

func TestReleaseAnonymousFailureDoesNotClaimCredentialWasTried(t *testing.T) {
	ctx := config.WithExecutionSnapshot(context.Background(), config.NewExecutionSnapshot(&config.Config{}))
	githubapi.Configure(githubapi.Config{Concurrency: 1})
	sink := &accountLogSink{}
	ui.SetSink(sink)
	defer ui.SetSink(nil)
	defer ui.Bind("release-anonymous", t.Name())()
	requests := 0
	githubapitest.Local(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Empty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})
	client, err := NewWithContext(ctx)
	require.NoError(t, err)
	_, err = client.GetReleases(ctx, "wnarutou", "lottery")
	require.ErrorContains(t, err, "404 Not Found")
	require.Equal(t, 1, requests)
	logs := strings.Join(sink.messages, "\n")
	require.Contains(t, logs, "GitHub API (core): anonymous request failed; no credentials configured")
	require.NotContains(t, logs, "credential 1/1")
	require.NotContains(t, logs, "1/1 distinct credentials")
}

func TestPoolSCMAllRequestsUseSelectedCredential(t *testing.T) {
	for _, operation := range []string{"repos", "releases", "assets", "download"} {
		for _, failure := range []string{"none", "primary", "permission", "invalid"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				ctx, tokens := githubapitest.Context(t)
				var auth []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					auth = append(auth, r.Header.Get("Authorization"))
					require.Equal(t, "pool-test-agent", r.Header.Get("User-Agent"))
					w.Header().Set("Content-Type", "application/json")
					if len(auth) == 1 && failure != "none" {
						status := 403
						if failure == "invalid" {
							status = 401
						}
						if failure == "primary" {
							w.Header().Set("X-RateLimit-Remaining", "0")
							w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
							w.Header().Set("X-RateLimit-Resource", "core")
						}
						w.WriteHeader(status)
						fmt.Fprint(w, `{"message":"failure"}`)
						return
					}
					if operation == "download" && strings.Contains(r.URL.Path, "/releases/assets/") {
						fmt.Fprint(w, "asset body")
					} else {
						fmt.Fprint(w, "[]")
					}
				}))
				defer server.Close()
				client, err := NewWithContext(ctx)
				require.NoError(t, err)
				client.c.BaseURL, _ = url.Parse(server.URL + "/")
				client.c.UserAgent = "pool-test-agent"
				run := func() error {
					switch operation {
					case "repos":
						_, err := client.GetReposContext(ctx, "org", typedef.TypeOrg)
						return err
					case "releases":
						_, err := client.GetReleases(ctx, "o", "r")
						return err
					case "assets":
						_, err := client.GetReleaseAssets(ctx, "o", "r", 1)
						return err
					default:
						rc, err := client.DownloadAsset(ctx, "o", "r", 1)
						if err == nil {
							defer rc.Close()
							data, readErr := io.ReadAll(rc)
							require.Equal(t, "asset body", string(data))
							return readErr
						}
						return err
					}
				}
				require.NoError(t, run())
				if failure == "none" {
					require.NoError(t, run())
					require.Equal(t, []string{"Bearer " + tokens[0], "Bearer " + tokens[1]}, auth)
				} else {
					require.Equal(t, []string{"Bearer " + tokens[0], "Bearer " + tokens[1]}, auth)
				}
				// The shared base must remain unauthenticated after every selected request.
				_, _, err = client.c.Repositories.ListReleases(ctx, "o", "r", nil)
				require.NoError(t, err)
				require.Empty(t, auth[len(auth)-1])
			})
		}
	}
}

func TestPoolSCMDownloadReleasesPermitBeforeBodyRead(t *testing.T) {
	ctx, _ := githubapitest.Context(t)
	githubapi.Configure(githubapi.Config{Concurrency: 1})
	githubapitest.Local(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "body") })
	client, err := NewWithContext(ctx)
	require.NoError(t, err)
	rc, err := client.DownloadAsset(ctx, "o", "r", 1)
	require.NoError(t, err)
	defer rc.Close()
	permit, err := githubapi.AcquireAccount(ctx, "core", nil, nil)
	require.NoError(t, err)
	permit.Done(githubapi.Observation{})
}

func TestPoolSCMRedirectDoesNotHoldPermitOrForwardToken(t *testing.T) {
	ctx, tokens := githubapitest.Context(t)
	githubapi.Configure(githubapi.Config{Concurrency: 1})
	var apiAuth, bodyAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			bodyAuth = r.Header.Get("Authorization")
			// Body download starts only after authorization has released its slot.
			permit, err := githubapi.AcquireAccount(ctx, "core", nil, nil)
			require.NoError(t, err)
			if permit != nil {
				permit.Done(githubapi.Observation{})
			}
			fmt.Fprint(w, "redirected body")
			return
		}
		apiAuth = r.Header.Get("Authorization")
		w.Header().Set("Location", "/body")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	client, err := NewWithContext(ctx)
	require.NoError(t, err)
	client.c.BaseURL, _ = url.Parse(server.URL + "/")
	rc, err := client.DownloadAsset(ctx, "o", "r", 1)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "redirected body", string(data))
	require.Equal(t, "Bearer "+tokens[0], apiAuth)
	require.Empty(t, bodyAuth)
}

func TestPoolSCMConstructorRetainsSnapshotAndCancellationSendsNoRequest(t *testing.T) {
	ctx, tokens := githubapitest.Context(t)
	requests := 0
	githubapitest.Local(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Equal(t, "Bearer "+tokens[0], r.Header.Get("Authorization"))
		fmt.Fprint(w, "[]")
	})
	client, err := NewWithContext(ctx)
	require.NoError(t, err)
	// The constructor's frozen credential pool remains in effect for background
	// convenience methods after the global configuration changes.
	previous := config.GetIns()
	config.SetIns(&config.Config{GitHubToken: "changed-global"})
	t.Cleanup(func() { config.SetIns(previous) })
	_, err = client.GetRepos("org", typedef.TypeOrg)
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = client.GetReleases(cancelled, "o", "r")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, requests)
}
