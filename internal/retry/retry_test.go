package retry

import (
	"context"
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

func TestDoSuccessFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		if calls < 3 {
			return &github.ErrorResponse{Response: resp(http.StatusInternalServerError)}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

func TestDoExhaustsRetries(t *testing.T) {
	base := time.Millisecond
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 2, BaseDelay: base}, func() error {
		calls++
		return &github.ErrorResponse{Response: resp(http.StatusBadGateway)}
	})
	require.Error(t, err)
	require.Equal(t, 3, calls) // 1 initial + 2 retries
}

func TestDoNonRetryableImmediate(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		return &github.ErrorResponse{Response: resp(http.StatusNotFound)}
	})
	require.Error(t, err)
	require.Equal(t, 1, calls) // no retry on 404
}

func TestDoContextCancelledDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	firstCall := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- Do(ctx, Config{MaxRetries: 3, BaseDelay: 100 * time.Millisecond}, func() error {
			calls++
			if calls == 1 {
				close(firstCall)
			}
			return &github.ErrorResponse{Response: resp(http.StatusInternalServerError)}
		})
	}()
	<-firstCall // wait until the first attempt actually ran
	cancel()    // then cancel while Do is waiting on backoff
	require.ErrorIs(t, <-errCh, context.Canceled)
	require.Equal(t, 1, calls) // cancelled while waiting, no further attempts
}

func TestDoHonorsRateLimitReset(t *testing.T) {
	// RateLimitError with a far reset must wait ~that long, not the backoff.
	// Use a short reset so the test stays fast; assert the total elapsed
	// roughly equals the reset delta (within tolerance) and succeeds.
	reset := time.Now().Add(150 * time.Millisecond)
	calls := 0
	start := time.Now()
	err := Do(context.Background(), Config{MaxRetries: 1, BaseDelay: time.Second}, func() error {
		calls++
		if calls == 1 {
			return &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}}
		}
		return nil
	})
	require.NoError(t, err)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 120*time.Millisecond)
	require.Less(t, elapsed, time.Second) // waited for reset, not the 1s backoff
}

func TestDoHonorsAbuseRetryAfter(t *testing.T) {
	ra := 150 * time.Millisecond
	calls := 0
	start := time.Now()
	err := Do(context.Background(), Config{MaxRetries: 1, BaseDelay: time.Second}, func() error {
		calls++
		if calls == 1 {
			return &github.AbuseRateLimitError{RetryAfter: &ra}
		}
		return nil
	})
	require.NoError(t, err)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 120*time.Millisecond)
	require.Less(t, elapsed, time.Second)
}
