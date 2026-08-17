package issue

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	gh "github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSyncBlocksWhileLockHeld(t *testing.T) {
	repo := typedef.Repository{URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "issue")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, nil)
	require.Equal(t, context.DeadlineExceeded, err, "issue Sync must block on the held issue lock")
}

func captureIssueListQuery(t *testing.T, opt *gh.IssueListByRepoOptions) url.Values {
	t.Helper()
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte("[]")); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := gh.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	require.NoError(t, err)
	client.BaseURL = baseURL
	_, _, err = client.Issues.ListByRepo(context.Background(), "owner", "repo", opt)
	require.NoError(t, err)
	return got
}

func TestNewIssueListOptionsInitialSyncOmitsSince(t *testing.T) {
	query := captureIssueListQuery(t, newIssueListOptions(time.Time{}))
	require.NotContains(t, query, "since")
	require.Equal(t, "all", query.Get("state"))
	require.Equal(t, "updated", query.Get("sort"))
	require.Equal(t, "asc", query.Get("direction"))
	require.Equal(t, "100", query.Get("per_page"))
}

func TestNewIssueListOptionsPreservesInstantInUTC(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	lastUpdate := time.Date(2026, 8, 17, 9, 30, 45, 0, shanghai)
	query := captureIssueListQuery(t, newIssueListOptions(lastUpdate))
	require.Equal(t, "2026-08-17T01:30:45Z", query.Get("since"))
}
