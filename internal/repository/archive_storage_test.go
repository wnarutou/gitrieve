package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncPublishesCompleteArchiveToEveryStorage(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	commitRefFile(t, f.remote, f.remoteDir, "latest main")
	first, second := t.TempDir(), t.TempDir()
	storages := []typedef.MultiStorage{
		{Storage: typedef.Storage{Type: "file", Path: first}},
		{Storage: typedef.Storage{Type: "file", Path: second}},
	}
	require.NoError(t, Sync(context.Background(), f.options, false, storages))
	name := filepath.Join("example.test", "owner", "repo", "repo.tar.gz")
	left, err := os.ReadFile(filepath.Join(first, name))
	require.NoError(t, err)
	right, err := os.ReadFile(filepath.Join(second, name))
	require.NoError(t, err)
	require.NotEmpty(t, left)
	require.Equal(t, left, right, "each destination must receive the entire archive from offset zero")
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(f.localDir), ".gitrieve-archive-*"))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}
