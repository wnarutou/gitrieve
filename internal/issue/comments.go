package issue

import (
	"context"
	"errors"
	"net/http"
	"time"

	gh "github.com/google/go-github/v56/github"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/retry"
	"github.com/wnarutou/gitrieve/internal/ui"
)

// Only comments of an issue already returned by ListByRepo get this extra
// allowance. Keep the 404 budget across transient retries without changing the
// shared retry policy or resetting its rate-limit/server-error retry budget.
func listIssueComments(ctx context.Context, baseClient *gh.Client, accounts []githubapi.Account, owner, repo string, number int, opt *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	delays := [...]time.Duration{time.Second, 3 * time.Second}
	notFoundRetries := 0
	var comments []*gh.IssueComment
	var resp *gh.Response
	err := retry.Do(ctx, config.GetRetryConfigContext(ctx), func() error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			apiErr := githubapi.Do(ctx, "core", accounts, func(token string) (githubapi.Observation, error) {
				client := githubapi.NewREST(baseClient, token)
				var requestErr error
				comments, resp, requestErr = client.Issues.ListComments(ctx, owner, repo, number, opt)
				if requestErr != nil {
					requestID := "unknown"
					if resp != nil && resp.Response != nil && resp.Header.Get("X-GitHub-Request-Id") != "" {
						requestID = resp.Header.Get("X-GitHub-Request-Id")
					}
					ui.Printf("Comments request for %s/%s issue #%d failed (GitHub Request ID: %s): %v", owner, repo, number, requestID, requestErr)
				}
				return githubapi.ObserveREST(resp, requestErr), requestErr
			})
			if apiErr == nil {
				return nil
			}
			// Pool exhaustion can preserve a 404 diagnostic while carrying a
			// quota reset from another account. Let shared retry honor that reset.
			var deadline interface{ RetryAt() time.Time }
			if errors.As(apiErr, &deadline) {
				return apiErr
			}

			var responseErr *gh.ErrorResponse
			if !errors.As(apiErr, &responseErr) || responseErr.Response == nil ||
				responseErr.Response.StatusCode != http.StatusNotFound || notFoundRetries == len(delays) {
				return apiErr
			}
			delay := delays[notFoundRetries]
			notFoundRetries++
			ui.Printf("Retrying comments for %s/%s issue #%d in %s after 404 (%d/%d)", owner, repo, number, delay, notFoundRetries, len(delays))

			// Release the HTTP permit before waiting, and let cancellation stop
			// the wait without issuing another request.
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	})
	return comments, resp, err
}
