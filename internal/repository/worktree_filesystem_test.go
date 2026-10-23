package repository

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// The original blob from TonyJiangWJ/Auto.js, not the SDK executable it names.
const absoluteSymlinkPath = "js-supports/dex_support/dx"
const absoluteSymlinkTarget = "/Users/jiangwenjie/Library/Android/sdk/build-tools/30.0.2/dx"

func TestSyncAbsoluteSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires native Unix symlink semantics; exercised in Linux Docker")
	}
	for _, component := range []string{"code", "wiki"} {
		for _, state := range []string{"fresh", "legacy-link", "pointer-file", "corrupted-file", "correct-link", "missing", "external-link", "link-to-tracked-file"} {
			t.Run(component+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				cwd, err := os.Getwd()
				require.NoError(t, err)
				require.NoError(t, os.Chdir(root))
				t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

				remote, _ := serveAccountGit(t, func(string, *http.Request) int { return 0 })
				previous, err := remote.Head()
				require.NoError(t, err)
				current := commitAbsoluteSymlink(t, remote)
				_, err = remote.CreateTag("retained", previous.Hash(), nil)
				require.NoError(t, err)
				cache := filepath.Join(root, ".gitrieve", "github.com", "test", "symlinks", component)
				link := filepath.Join(cache, filepath.FromSlash(absoluteSymlinkPath))
				external := filepath.Join(root, "external.txt")
				require.NoError(t, os.WriteFile(external, []byte("external data"), 0o644))

				if state != "fresh" {
					remoteTree, err := remote.Worktree()
					require.NoError(t, err)
					// Intentionally use the old filesystem to create a real broken
					// cache, including its persisted .git index. Sync must reopen it.
					cached, err := git.PlainClone(cache, false, &git.CloneOptions{URL: remoteTree.Filesystem.Root()})
					require.NoError(t, err)
					raw, err := os.Readlink(link)
					require.NoError(t, err)
					require.Equal(t, cache+absoluteSymlinkTarget, raw)
					require.NoError(t, cached.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("local-only"), previous.Hash())))

					if state != "legacy-link" {
						require.NoError(t, os.Remove(link))
					}
					switch state {
					case "pointer-file":
						require.NoError(t, os.WriteFile(link, []byte(absoluteSymlinkTarget), 0o644))
					case "corrupted-file":
						require.NoError(t, os.WriteFile(link, []byte("incorrect cached contents"), 0o644))
					case "correct-link":
						require.NoError(t, os.Symlink(absoluteSymlinkTarget, link))
					case "external-link":
						require.NoError(t, os.Symlink(external, link))
					case "link-to-tracked-file":
						require.NoError(t, os.Symlink(filepath.Join(cache, "content.txt"), link))
					}
				}

				repo := typedef.Repository{URL: "github.com/test/symlinks", UseCache: true, AllBranches: true}
				for attempt := 0; attempt < 2; attempt++ {
					require.NoError(t, Sync(context.Background(), repo, component == "wiki", nil))
					raw, err := os.Readlink(link)
					require.NoError(t, err)
					require.Equal(t, absoluteSymlinkTarget, raw)
					info, err := os.Lstat(link)
					require.NoError(t, err)
					require.NotZero(t, info.Mode()&os.ModeSymlink)
					require.EqualValues(t, 60, info.Size())

					cached, err := git.PlainOpen(cache)
					require.NoError(t, err)
					head, err := cached.Head()
					require.NoError(t, err)
					require.Equal(t, current, head.Hash())
					_, err = cached.CommitObject(previous.Hash())
					require.NoError(t, err, "upgrading must retain earlier commits")
					tag, err := cached.Reference(plumbing.NewTagReferenceName("retained"), false)
					require.NoError(t, err)
					require.Equal(t, previous.Hash(), tag.Hash())
					if state != "fresh" {
						branch, err := cached.Reference(plumbing.NewBranchReferenceName("local-only"), false)
						require.NoError(t, err)
						require.Equal(t, previous.Hash(), branch.Hash())
					}
					idx, err := cached.Storer.Index()
					require.NoError(t, err)
					entry, err := idx.Entry(absoluteSymlinkPath)
					require.NoError(t, err)
					require.Equal(t, filemode.Symlink, entry.Mode)
					require.Equal(t, plumbing.NewHash("b57adb5a57e38b85e362a08b270f71c4698c952a"), entry.Hash)

					view, err := git.Open(cached.Storer, osfs.New(cache, osfs.WithBoundOS()))
					require.NoError(t, err)
					worktree, err := view.Worktree()
					require.NoError(t, err)
					status, err := worktree.Status()
					require.NoError(t, err)
					require.True(t, status.IsClean(), status.String())
					contents, err := os.ReadFile(filepath.Join(cache, "content.txt"))
					require.NoError(t, err)
					require.Equal(t, "first version", string(contents))
					contents, err = os.ReadFile(external)
					require.NoError(t, err)
					require.Equal(t, "external data", string(contents))
				}
			})
		}
	}
}

func commitAbsoluteSymlink(t *testing.T, repo *git.Repository) plumbing.Hash {
	t.Helper()
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	require.NoError(t, err)
	_, err = io.WriteString(w, absoluteSymlinkTarget)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	hash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)
	idx, err := repo.Storer.Index()
	require.NoError(t, err)
	idx.Entries = append(idx.Entries, &index.Entry{Name: absoluteSymlinkPath, Mode: filemode.Symlink, Hash: hash})
	require.NoError(t, repo.Storer.SetIndex(idx))
	worktree, err := repo.Worktree()
	require.NoError(t, err)
	commit, err := worktree.Commit("absolute symlink", &git.CommitOptions{Author: testSignature()})
	require.NoError(t, err)
	return commit
}

func TestManagedWorktreeFSChrootPreservesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires native Unix symlinks")
	}
	root := t.TempDir()
	fs := newManagedWorktreeFS(root)
	require.NoError(t, fs.MkdirAll("nested", 0o755))
	child, err := fs.Chroot("nested")
	require.NoError(t, err)
	require.NoError(t, child.Symlink(absoluteSymlinkTarget, "dx"))
	raw, err := os.Readlink(filepath.Join(root, "nested", "dx"))
	require.NoError(t, err)
	require.Equal(t, absoluteSymlinkTarget, raw)
	// A submodule's worktree needs the same no-follow removal as its parent.
	require.NoError(t, child.Remove("dx"))
	_, err = os.Lstat(filepath.Join(root, "nested", "dx"))
	require.True(t, os.IsNotExist(err))
}

func TestManagedWorktreeFSRemoveRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep.txt")
	require.NoError(t, os.WriteFile(victim, []byte("keep"), 0o644))
	fs := newManagedWorktreeFS(root)
	for _, name := range []string{victim, filepath.Join("..", filepath.Base(outside), "keep.txt")} {
		require.Error(t, fs.Remove(name))
	}
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
		// Lexically local, but the resolved parent is outside the worktree.
		require.Error(t, fs.Remove("escape/keep.txt"))
	}
	contents, err := os.ReadFile(victim)
	require.NoError(t, err)
	require.Equal(t, "keep", string(contents))
}
