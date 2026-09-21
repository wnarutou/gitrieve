package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

type chmodWorktreeFile func(name string, mode os.FileMode) error

// restoreManagedWorktree rebuilds only the derived index/worktree state from
// the current local HEAD. It never resets to an origin ref or removes .git, so
// previously fetched branches, tags, and objects remain deletion-safe.
func restoreManagedWorktree(repo *git.Repository, worktree *git.Worktree) error {
	root := filepath.Clean(worktree.Filesystem.Root())
	return restoreManagedWorktreeWithChmod(repo, worktree, func(name string, mode os.FileMode) error {
		return os.Chmod(filepath.Join(root, filepath.FromSlash(name)), mode)
	})
}

// pullWithManagedWorktreeRecovery retries only the one pull failure that a
// disposable dirty worktree can cause. Network and history errors retain their
// existing behavior, and a persistent dirty state gets at most one retry.
func pullWithManagedWorktreeRecovery(pull func() error, recoverWorktree func() error) error {
	err := pull()
	if !errors.Is(err, git.ErrUnstagedChanges) {
		return err
	}
	if recoveryErr := recoverWorktree(); recoveryErr != nil {
		return fmt.Errorf("recover managed worktree after pull reported unstaged changes: %w", recoveryErr)
	}
	return pull()
}

func restoreManagedWorktreeWithChmod(repo *git.Repository, worktree *git.Worktree, chmod chmodWorktreeFile) error {
	headBefore, err := repo.Head()
	if err != nil {
		return fmt.Errorf("resolve local HEAD before worktree recovery: %w", err)
	}
	if err := worktree.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: headBefore.Hash()}); err != nil {
		return fmt.Errorf("reset managed worktree to local %s: %w", headBefore.Name().Short(), err)
	}
	if err := worktree.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("clean untracked files from managed worktree: %w", err)
	}
	if err := repairWorktreeModes(repo, worktree, chmod); err != nil {
		return err
	}

	headAfter, err := repo.Head()
	if err != nil {
		return fmt.Errorf("resolve local HEAD after worktree recovery: %w", err)
	}
	if headAfter.Name() != headBefore.Name() || headAfter.Hash() != headBefore.Hash() {
		return fmt.Errorf("managed worktree recovery changed local HEAD from %s@%s to %s@%s",
			headBefore.Name(), headBefore.Hash(), headAfter.Name(), headAfter.Hash())
	}

	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("verify managed worktree recovery: %w", err)
	}
	if status.IsClean() {
		return nil
	}
	paths := make([]string, 0, len(status))
	for name, fileStatus := range status {
		if fileStatus.Staging == git.Unmodified && fileStatus.Worktree == git.Unmodified {
			continue
		}
		paths = append(paths, fmt.Sprintf("%s[%c%c]", name, fileStatus.Staging, fileStatus.Worktree))
	}
	sort.Strings(paths)
	return fmt.Errorf("managed worktree remains dirty after recovery: %s", strings.Join(paths, ", "))
}

func repairWorktreeModes(repo *git.Repository, worktree *git.Worktree, chmod chmodWorktreeFile) error {
	index, err := repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("read worktree index: %w", err)
	}
	entries := make(map[string]filemode.FileMode, len(index.Entries))
	for _, entry := range index.Entries {
		entries[entry.Name] = entry.Mode
	}

	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("inspect worktree before mode recovery: %w", err)
	}
	paths := make([]string, 0, len(status))
	for name := range status {
		paths = append(paths, name)
	}
	sort.Strings(paths)

	for _, name := range paths {
		fileStatus := status.File(name)
		if fileStatus.Worktree != git.Modified {
			continue
		}
		expected, tracked := entries[name]
		if !tracked || (expected != filemode.Regular && expected != filemode.Executable) {
			continue
		}
		info, statErr := worktree.Filesystem.Stat(name)
		if statErr != nil {
			return fmt.Errorf("inspect worktree mode for %s: %w", name, statErr)
		}
		actual, modeErr := filemode.NewFromOSFileMode(info.Mode())
		if modeErr != nil {
			return fmt.Errorf("inspect worktree mode for %s: %w", name, modeErr)
		}
		if actual == expected {
			continue
		}
		if actual != filemode.Regular && actual != filemode.Executable {
			continue
		}

		desired := info.Mode().Perm()
		if expected == filemode.Executable {
			desired |= 0o111
		} else {
			desired &^= 0o111
		}
		if err := chmod(name, desired); err != nil {
			return fmt.Errorf("repair worktree mode for %s to %06o: %w", name, expected, err)
		}

		info, statErr = worktree.Filesystem.Stat(name)
		if statErr != nil {
			return fmt.Errorf("verify worktree mode for %s: %w", name, statErr)
		}
		actual, modeErr = filemode.NewFromOSFileMode(info.Mode())
		if modeErr != nil {
			return fmt.Errorf("verify worktree mode for %s: %w", name, modeErr)
		}
		if actual != expected {
			return fmt.Errorf("worktree mode recovery failed: %s expected %06o, actual %06o", name, expected, actual)
		}
	}
	return nil
}
