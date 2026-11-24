package recovery

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
)

func writeBackupArchive(t *testing.T, filename string, entries map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	file, err := os.Create(filename)
	require.NoError(t, err)
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, file.Close())
}

func TestScanDiscoversRepositoriesFromStorageTree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	currentDir := filepath.Join(base, "app")

	alphaDir := filepath.Join(root, "git.example.com", "acme", "alpha")
	writeBackupArchive(t, filepath.Join(alphaDir, "alpha.tar.gz"), map[string]string{
		"alpha/.git/HEAD": "ref: refs/heads/main\n",
	})
	writeBackupArchive(t, filepath.Join(alphaDir, "issues.tar.gz"), map[string]string{
		"issues/1.md": "issue\n",
	})
	require.NoError(t, os.MkdirAll(filepath.Join(alphaDir, "release", "v1.0.0"), 0o755))

	betaDir := filepath.Join(root, "git.example.com", "acme", "beta")
	writeBackupArchive(t, filepath.Join(betaDir, "discussions.tar.gz"), map[string]string{
		"discussion/2.md": "discussion\n",
	})

	require.NoError(t, os.MkdirAll(filepath.Join(root, "git.example.com", "acme", "empty"), 0o755))

	existing := filepath.Join(currentDir, ".gitrieve", "git.example.com", "acme", "alpha", "code")
	_, err := git.PlainInit(existing, false)
	require.NoError(t, err)

	result, err := Scan(root, currentDir)
	require.NoError(t, err)

	require.Equal(t, 2, result.TotalRepositories)
	require.Equal(t, 3, result.TotalComponents)
	require.Equal(t, 1, result.PresentComponents)
	require.Len(t, result.Repositories, 2)

	alpha := result.Repositories[0]
	require.Equal(t, "git.example.com/acme/alpha", alpha.Key)
	require.True(t, alpha.Release)
	require.Equal(t, []ComponentBackup{
		{Component: "code", Archive: "alpha.tar.gz", Present: true},
		{Component: "issues", Archive: "issues.tar.gz", Present: false},
	}, alpha.Components)

	beta := result.Repositories[1]
	require.Equal(t, "git.example.com/acme/beta", beta.Key)
	require.Equal(t, []ComponentBackup{
		{Component: "discussion", Archive: "discussions.tar.gz", Present: false},
	}, beta.Components)
	require.NotEmpty(t, result.Warnings, "repo directories without cache archives should be reported")
}

func TestScanRejectsMissingStorageRoot(t *testing.T) {
	_, err := Scan(filepath.Join(t.TempDir(), "missing"), t.TempDir())
	require.Error(t, err)
}
