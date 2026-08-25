package discussion

import (
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/require"
)

func TestGraphQLObservationIncludesQuota(t *testing.T) {
	reset := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	obs := graphQLObservation(rateLimitData{
		Cost: 7, Remaining: 42, ResetAt: githubv4.DateTime{Time: reset},
	}, nil)
	require.Equal(t, "graphql", obs.Resource)
	require.Equal(t, 7, obs.Used)
	require.Equal(t, 42, obs.Remaining)
	require.Equal(t, reset, obs.Reset)
}
