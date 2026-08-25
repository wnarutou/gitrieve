package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
)

func TestNewObservesReloadedToken(t *testing.T) {
	var auth []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer s.Close()
	base, err := url.Parse(s.URL + "/")
	require.NoError(t, err)

	for _, token := range []string{"first", "second"} {
		config.SetIns(&config.Config{GitHubToken: token, GitHubAPIConcurrency: 1})
		client, err := New()
		require.NoError(t, err)
		client.c.BaseURL = base
		_, err = client.GetReleases(context.Background(), "o", "r")
		require.NoError(t, err)
	}
	require.Equal(t, []string{"Bearer first", "Bearer second"}, auth)
}
