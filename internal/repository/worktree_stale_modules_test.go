package repository

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestManagedRecoveryWithStaleSubmoduleMapping(t *testing.T) {
	repo, worktree, fs := newMemoryRepository(t)
	require.NoError(t, fs.MkdirAll("android/app", 0o755))
	writeBillyFile(t, fs, "android/app/build.gradle", []byte("tracked source\n"), 0o644)
	_, err := worktree.Add("android/app/build.gradle")
	require.NoError(t, err)
	// This mapping outlived the gitlink: android is now an ordinary tree.
	modules := []byte("[submodule \"android\"]\n\tpath = android\n\turl = https://example.com/android.git\n")
	writeBillyFile(t, fs, ".gitmodules", modules, 0o644)
	_, err = worktree.Add(".gitmodules")
	require.NoError(t, err)
	commit, err := worktree.Commit("ordinary android sources with stale mapping", &git.CommitOptions{Author: testSignature()})
	require.NoError(t, err)
	before, err := repo.Head()
	require.NoError(t, err)

	writeBillyFile(t, fs, "android/app/build.gradle", []byte("damaged\n"), 0o644)
	writeBillyFile(t, fs, "android/app/leftover.txt", []byte("remove me\n"), 0o644)
	require.NoError(t, restoreManagedWorktreeWithChmod(repo, worktree, func(string, os.FileMode) error { return nil }))
	require.Equal(t, "tracked source\n", string(readBillyFile(t, fs, "android/app/build.gradle")))
	require.Equal(t, modules, readBillyFile(t, fs, ".gitmodules"))
	after, err := repo.Head()
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = repo.CommitObject(commit)
	require.NoError(t, err)
	_, err = fs.Stat("android/app/leftover.txt")
	require.True(t, os.IsNotExist(err))
	require.NoError(t, fs.Remove("android/app/build.gradle"))
	require.NoError(t, restoreManagedWorktreeWithChmod(repo, worktree, func(string, os.FileMode) error { return nil }))
	require.Equal(t, "tracked source\n", string(readBillyFile(t, fs, "android/app/build.gradle")))
}

func TestSyncWithStaleSubmoduleMapping(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })
	remote, _ := serveAccountGit(t, func(string, *http.Request) int { return 0 })
	w, err := remote.Worktree()
	require.NoError(t, err)
	require.NoError(t, w.Filesystem.MkdirAll("android/app", 0o755))
	old := commitTestFile(t, w, "android/app/build.gradle", "original")
	modules := "[submodule \"android\"]\n\tpath = android\n\turl = https://example.com/android.git\n"
	commitTestFile(t, w, ".gitmodules", modules)
	repo := typedef.Repository{URL: "github.com/test/stale-modules", UseCache: true}
	cache := filepath.Join(root, ".gitrieve", "github.com", "test", "stale-modules", "code")
	for _, component := range []string{"code", "wiki"} {
		t.Run(component, func(t *testing.T) {
			cache = filepath.Join(filepath.Dir(cache), component)
			for _, content := range []string{"updated", "updated again"} {
				expected := commitTestFile(t, w, "android/app/build.gradle", content)
				require.NoError(t, Sync(context.Background(), repo, component == "wiki", nil))
				local, err := git.PlainOpen(cache)
				require.NoError(t, err)
				head, err := local.Head()
				require.NoError(t, err)
				require.Equal(t, expected, head.Hash())
				data, err := os.ReadFile(filepath.Join(cache, "android", "app", "build.gradle"))
				require.NoError(t, err)
				require.Equal(t, content, string(data))
				data, err = os.ReadFile(filepath.Join(cache, ".gitmodules"))
				require.NoError(t, err)
				require.Equal(t, modules, string(data))
				_, err = local.CommitObject(old)
				require.NoError(t, err)
			}
		})
	}
}

func TestManagedStatusDoesNotHideOrdinaryChanges(t *testing.T) {
	repo, w, fs := newMemoryRepository(t)
	commitTestFile(t, w, "tracked.txt", "original")
	commitTestFile(t, w, ".gitignore", "*.log\n")
	writeBillyFile(t, fs, "tracked.txt", []byte("changed"), 0o644)
	writeBillyFile(t, fs, "new.txt", []byte("new"), 0o644)
	writeBillyFile(t, fs, "ignored.log", []byte("ignored"), 0o644)
	status, err := managedWorktreeStatus(repo, w)
	require.NoError(t, err)
	require.Equal(t, git.Modified, status.File("tracked.txt").Worktree)
	require.Equal(t, git.Untracked, status.File("new.txt").Worktree)
	require.NotContains(t, status, "ignored.log")
	_, err = w.Add("tracked.txt")
	require.NoError(t, err)
	require.NoError(t, fs.Remove("tracked.txt"))
	status, err = managedWorktreeStatus(repo, w)
	require.NoError(t, err)
	require.Equal(t, git.Modified, status.File("tracked.txt").Staging)
	require.Equal(t, git.Deleted, status.File("tracked.txt").Worktree)
}
