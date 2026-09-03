package wiki

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/syncresult"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type wikiRoundTripper func(*http.Request) (*http.Response, error)

func (fn wikiRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type wikiLogSink struct {
	logs []string
}

func (s *wikiLogSink) Log(_, _, level, message string) error {
	s.logs = append(s.logs, level+":"+message)
	return nil
}

func TestWikiAvailabilitySkipsRepositoryWithoutWiki(t *testing.T) {
	err := wikiAvailability("github.com/test/repo", false)
	reason, ok := syncresult.SkippedReason(err)

	require.True(t, ok)
	require.Equal(t, "repository github.com/test/repo has no wiki", reason)
	require.NoError(t, wikiAvailability("github.com/test/repo", true))
}

func TestSyncSkipsUnavailableWikiWithoutPresentationOrRepositorySync(t *testing.T) {
	previousConfig := config.GetIns()
	config.SetIns(&config.Config{})
	if previousConfig != nil {
		t.Cleanup(func() { config.SetIns(previousConfig) })
	}

	previousTransport := http.DefaultTransport
	http.DefaultTransport = wikiRoundTripper(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/repos/test/repo", req.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"has_wiki":false}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	sink := &wikiLogSink{}
	ui.SetSink(sink)
	t.Cleanup(func() { ui.SetSink(nil) })
	unbind := ui.Bind("execution-1", "wiki")
	defer unbind()

	err := Sync(context.Background(), typedef.Repository{Name: "repo", URL: "github.com/test/repo"}, nil)
	reason, ok := syncresult.SkippedReason(err)

	require.True(t, ok)
	require.Equal(t, "repository github.com/test/repo has no wiki", reason)
	require.Empty(t, sink.logs)
}

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
