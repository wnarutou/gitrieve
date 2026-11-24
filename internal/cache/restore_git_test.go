package cache

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/archive"
	"github.com/wnarutou/gitrieve/internal/scm"
)

func TestRestoredCodeArchiveIsAUsableGitRepository(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source", "demo")
	require.NoError(t, os.MkdirAll(source, 0o755))
	repo, err := git.PlainInit(source, false)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o644))
	worktree, err := repo.Worktree()
	require.NoError(t, err)
	_, err = worktree.Add("README.md")
	require.NoError(t, err)
	commit, err := worktree.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0)},
	})
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature"),
		commit,
	)))
	sourceHead, err := repo.Head()
	require.NoError(t, err)

	buf, err := archive.Create(context.Background(), source, "demo")
	require.NoError(t, err)
	defer buf.Close()

	storageRoot := filepath.Join(base, "repo")
	archivePath := filepath.Join(storageRoot, "github.com", "acme", "demo", "demo.tar.gz")
	require.NoError(t, os.MkdirAll(filepath.Dir(archivePath), 0o755))
	destination, err := os.Create(archivePath)
	require.NoError(t, err)
	_, err = io.Copy(destination, buf)
	require.NoError(t, err)
	require.NoError(t, destination.Close())

	currentDir := filepath.Join(base, "app")
	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)
	_, err = RestoreComponent(context.Background(), currentDir, r, ComponentCode, fileStorages(storageRoot))
	require.NoError(t, err)

	restored, err := git.PlainOpen(filepath.Join(currentDir, ".gitrieve", "github.com", "acme", "demo", "code"))
	require.NoError(t, err)
	head, err := restored.Head()
	require.NoError(t, err)
	require.Equal(t, sourceHead.Name(), head.Name())
	require.Equal(t, sourceHead.Hash(), head.Hash())
	feature, err := restored.Reference(plumbing.NewBranchReferenceName("feature"), true)
	require.NoError(t, err)
	require.Equal(t, commit, feature.Hash())
}
