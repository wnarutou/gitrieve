package release

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestDownloadAllAssetsCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := DownloadAllAssets(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDownloadAllAssetsBlocksWhileLockHeld(t *testing.T) {
	// DownloadAllAssets reads the release limits from the package-global config
	// before acquiring the lock, so point config at a minimal file (same
	// pattern as the executor tests) to avoid a nil-pointer dereference.
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString("githubtoken: test-token\n")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	config.Path = tmp.Name()
	config.Init()
	t.Cleanup(func() { config.Path = "" })

	repo := typedef.Repository{URL: "github.com/test/repo"}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "release")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = DownloadAllAssets(ctx, repo, nil)
	require.Equal(t, context.DeadlineExceeded, err, "DownloadAllAssets must block on the held release lock")
}
