package wiki

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/syncresult"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestWikiAvailabilitySkipsRepositoryWithoutWiki(t *testing.T) {
	err := wikiAvailability("github.com/test/repo", false)
	reason, ok := syncresult.SkippedReason(err)

	require.True(t, ok)
	require.Equal(t, "repository github.com/test/repo has no wiki", reason)
	require.NoError(t, wikiAvailability("github.com/test/repo", true))
}

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
