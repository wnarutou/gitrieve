package retry

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
)

func resp(status int) *http.Response {
	return &http.Response{StatusCode: status}
}

func TestClassifyRateLimitError(t *testing.T) {
	// Primary REST rate limit: 403 with rate-limit reset in ~2 minutes.
	reset := time.Now().Add(2 * time.Minute)
	err := &github.RateLimitError{
		Rate: github.Rate{Reset: github.Timestamp{Time: reset}},
	}
	retryable, wait := classify(err)
	require.True(t, retryable)
	require.GreaterOrEqual(t, wait, 100*time.Second)
	require.LessOrEqual(t, wait, 2*time.Minute)
}

func TestClassifyAbuseRateLimitError(t *testing.T) {
	// Secondary rate limit with RetryAfter.
	ra := 45 * time.Second
	err := &github.AbuseRateLimitError{RetryAfter: &ra}
	retryable, wait := classify(err)
	require.True(t, retryable)
	require.Equal(t, ra, wait)

	// Secondary rate limit without RetryAfter -> retryable, backoff.
	err = &github.AbuseRateLimitError{}
	retryable, wait = classify(err)
	require.True(t, retryable)
	require.Equal(t, time.Duration(0), wait)
}

func TestClassifyErrorResponse429(t *testing.T) {
	// 429 with Retry-After header -> wait the header.
	h := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	h.Header.Set("Retry-After", "30")
	err := &github.ErrorResponse{Response: h}
	retryable, wait := classify(err)
	require.True(t, retryable)
	require.Equal(t, 30*time.Second, wait)

	// 429 without Retry-After -> retryable, backoff.
	err = &github.ErrorResponse{Response: resp(http.StatusTooManyRequests)}
	retryable, wait = classify(err)
	require.True(t, retryable)
	require.Equal(t, time.Duration(0), wait)
}

func TestClassifyErrorResponse5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		err := &github.ErrorResponse{Response: resp(code)}
		retryable, wait := classify(err)
		require.True(t, retryable, "status %d should be retryable", code)
		require.Equal(t, time.Duration(0), wait)
	}
	// 404 is not retryable.
	err := &github.ErrorResponse{Response: resp(http.StatusNotFound)}
	retryable, _ := classify(err)
	require.False(t, retryable)
}

func TestClassifyURLError(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://api.github.com/x", Err: errors.New("connection reset")}
	retryable, _ := classify(err)
	require.True(t, retryable)
}

func TestClassifyGraphQL(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		retry    bool
		wantWait time.Duration
	}{
		{
			name:  "secondary limit with retryAfterSeconds",
			msg:   `non-200 OK status code: 403 Forbidden body: {"errors":[{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.","extensions":{"retryAfterSeconds":60}}]}`,
			retry: true,
			wantWait: 60 * time.Second,
		},
		{
			name:  "primary limit via errors array",
			msg:   "API rate limit exceeded for user ID 12345.",
			retry: true,
		},
		{
			name:  "429 non-200",
			msg:   "non-200 OK status code: 429 Too Many Requests body: {\"message\":\"rate limit\"}",
			retry: true,
		},
		{
			name:  "503 non-200",
			msg:   "non-200 OK status code: 503 Service Unavailable body: {}",
			retry: true,
		},
		{
			name:  "non-retryable 404",
			msg:   "non-200 OK status code: 404 Not Found body: {\"message\":\"Not Found\"}",
			retry: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryable, wait := classify(errors.New(tc.msg))
			require.Equal(t, tc.retry, retryable)
			if tc.wantWait > 0 {
				require.Equal(t, tc.wantWait, wait)
			}
		})
	}
}
