package repository

import (
	"context"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
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
