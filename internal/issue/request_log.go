package issue

import (
	"fmt"

	gh "github.com/google/go-github/v56/github"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/ui"
)

// Log the credential actually used by this request, including successful
// requests that rotate between accounts without entering error fallback.
func logIssueRequest(operation string, accounts []githubapi.Account, token string, response *gh.Response, err error) {
	credential := "anonymous (no credentials configured)"
	if token != "" {
		positions := make(map[string]int, len(accounts))
		for _, account := range accounts {
			if _, exists := positions[account.Token]; !exists {
				positions[account.Token] = len(positions) + 1
			}
		}
		credential = fmt.Sprintf("credential %d/%d", positions[token], len(positions))
	}
	result := "succeeded"
	if err != nil {
		result = "failed"
	}
	status, requestID := "no HTTP response", "unknown"
	if response != nil && response.Response != nil {
		status = fmt.Sprintf("HTTP %d", response.StatusCode)
		if id := response.Header.Get("X-GitHub-Request-Id"); id != "" {
			requestID = id
		}
	}
	// Ordinals match the shared pool's deduplicated configuration order. Never
	// print tokens, account names, request URLs, or raw response/error bodies.
	ui.Printf("%s using %s: %s (%s; GitHub Request ID: %s)", operation, credential, result, status, requestID)
}
