package repository

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncCancelledContextFailsPromptlyAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the sync starts

	repo := typedef.Repository{Name: "test-repo", URL: "github.com/test/repo", UseCache: true}

	// A cancelled context must fail the sync at the first network operation
	// instead of hanging forever.
	err := Sync(ctx, repo, false, nil)
	assert.Error(t, err, "a cancelled context must fail the sync")

	// The partial clone directory must be removed so the next sync retries a
	// clean clone instead of trying to open a broken .git.
	cwd, _ := os.Getwd()
	gitDir := path.Join(cwd, ".gitrieve", "github.com", "test", "repo", "code")
	_, statErr := os.Stat(path.Join(gitDir, ".git"))
	assert.True(t, os.IsNotExist(statErr), "partial clone .git must be cleaned up after a cancelled clone")

	// Clean up the .gitrieve cache dir created by the test.
	os.RemoveAll(path.Join(cwd, ".gitrieve"))
}

func TestSyncBlocksWhileCodeLockHeld(t *testing.T) {
	repo := typedef.Repository{Name: "test-repo", URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "code")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	// If Sync respects the lock it blocks here and hits the ctx timeout instead
	// of reaching the network (github.com/test/repo does not exist).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, false, nil)
	require.Equal(t, context.DeadlineExceeded, err, "Sync must block on the held code lock")
}

func TestSyncBlocksWhileWikiLockHeld(t *testing.T) {
	repo := typedef.Repository{Name: "test-repo", URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "wiki")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, true, nil)
	require.Equal(t, context.DeadlineExceeded, err, "wiki Sync must block on the held wiki lock")
}
