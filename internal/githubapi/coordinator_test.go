package githubapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorLimitsConcurrentRequests(t *testing.T) {
	c := newCoordinator(Config{Concurrency: 1})
	p, err := c.acquire(context.Background(), "core")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = c.acquire(ctx, "core")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	p.Done(Observation{})

	p2, err := c.acquire(context.Background(), "core")
	require.NoError(t, err)
	p2.Done(Observation{})
}

func TestCoordinatorPausesOnlyObservedResource(t *testing.T) {
	c := newCoordinator(Config{Concurrency: 2, LowRemainingThreshold: 10})
	p, err := c.acquire(context.Background(), "core")
	require.NoError(t, err)
	p.Done(Observation{Resource: "core", Remaining: 10, Reset: time.Now().Add(time.Second)})

	other, err := c.acquire(context.Background(), "graphql")
	require.NoError(t, err)
	other.Done(Observation{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = c.acquire(ctx, "core")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestObserveErrorSecondaryDefaultsToOneMinute(t *testing.T) {
	err := &github.AbuseRateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden}}
	obs := ObserveError(err)
	require.True(t, obs.Secondary)
	require.Equal(t, time.Minute, obs.RetryAfter)
}
