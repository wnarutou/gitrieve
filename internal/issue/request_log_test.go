package issue

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	gh "github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/ui"
)

func TestIssueRequestLogAuthenticationAndResponseDiagnostics(t *testing.T) {
	accounts := []githubapi.Account{
		{Name: "private-name", Token: "first-secret"},
		{Name: "alias-name", Token: "first-secret"},
		{Name: "second-name", Token: "second-secret"},
	}
	for _, test := range []struct {
		name     string
		token    string
		response *gh.Response
		err      error
		want     string
	}{
		{"deduplicated", "second-secret", &gh.Response{Response: &http.Response{StatusCode: 200}}, nil,
			"using credential 2/2: succeeded (HTTP 200; GitHub Request ID: unknown)"},
		{"anonymous", "", &gh.Response{Response: &http.Response{StatusCode: 404}}, errors.New("second-secret in response"),
			"using anonymous (no credentials configured): failed (HTTP 404; GitHub Request ID: unknown)"},
		{"network", "first-secret", nil, errors.New("first-secret in transport error"),
			"using credential 1/2: failed (no HTTP response; GitHub Request ID: unknown)"},
		{"decode", "second-secret", &gh.Response{Response: &http.Response{StatusCode: 200}}, errors.New("first-secret in invalid JSON"),
			"using credential 2/2: failed (HTTP 200; GitHub Request ID: unknown)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &commentLogSink{}
			ui.SetSink(sink)
			t.Cleanup(func() { ui.SetSink(nil) })
			t.Cleanup(ui.Bind("issue-request", t.Name()))
			eligible := accounts
			if test.token == "" {
				eligible = nil
			}
			logIssueRequest("Issues request for test/repo batch 1", eligible, test.token, test.response, test.err)
			require.Len(t, sink.messages, 1)
			logs := strings.Join(sink.messages, "\n")
			require.Contains(t, logs, test.want)
			for _, account := range accounts {
				require.NotContains(t, logs, account.Name)
				require.NotContains(t, logs, account.Token)
			}
		})
	}
}
