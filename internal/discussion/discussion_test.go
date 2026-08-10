package discussion

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSyncBlocksWhileLockHeld(t *testing.T) {
	repo := typedef.Repository{URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "discussion")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, nil)
	require.Equal(t, context.DeadlineExceeded, err, "discussion Sync must block on the held discussion lock")
}
