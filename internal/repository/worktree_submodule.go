package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
)

var errManagedIndexRecovery = errors.New("managed index update failed")

func checkoutManagedWorktree(repo *git.Repository, worktree *git.Worktree, opts *git.CheckoutOptions) error {
	if _, err := unmappedManagedGitlinks(repo, worktree); err != nil {
		return err
	}
	return worktree.Checkout(opts)
}

// Missing .gitmodules mappings are valid gitlinks to archive, but go-git
// treats their empty directories as deleted. Check before resetting: a reset
// could otherwise traverse an unmapped submodule and delete its contents.
func unmappedManagedGitlinks(repo *git.Repository, worktree *git.Worktree) (map[string]bool, error) {
	gitlinks, err := managedGitlinks(repo)
	if err != nil {
		return nil, err
	}
	modules, err := worktree.Submodules()
	if err != nil {
		return nil, fmt.Errorf("read managed submodule mappings: %w", err)
	}
	mapped := make(map[string]bool, len(modules))
	for _, module := range modules {
		mapped[module.Config().Path] = true
	}
	unmapped := make(map[string]bool)
	for name := range gitlinks {
		info, err := worktree.Filesystem.Lstat(name)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect submodule %s: %w", name, err)
		}
		if err == nil && !info.IsDir() {
			return nil, fmt.Errorf("submodule path %s is not a directory", name)
		}
		if mapped[name] {
			continue
		}
		if err == nil {
			children, err := worktree.Filesystem.ReadDir(name)
			if err != nil {
				return nil, fmt.Errorf("inspect unmapped submodule %s: %w", name, err)
			}
			if len(children) != 0 {
				return nil, fmt.Errorf("unmapped submodule %s contains local files", name)
			}
		}
		unmapped[name] = true
	}
	return unmapped, nil
}

// Keep go-git's fetch and fast-forward checks, but exclude empty, unmapped
// gitlinks from its unstaged-file check. They stay in commit trees and are
// rebuilt in the index by Pull's reset. Restore the index even if Pull exits
// early; use the current LOCAL HEAD, since Pull may already have advanced it.
func pullManagedWorktree(ctx context.Context, repo *git.Repository, worktree *git.Worktree, opts *git.PullOptions) (result error) {
	unmapped, err := unmappedManagedGitlinks(repo, worktree)
	if err != nil {
		return err
	}
	if len(unmapped) == 0 {
		return worktree.PullContext(ctx, opts)
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		return err
	}
	filtered := *idx
	filtered.Entries = make([]*index.Entry, 0, len(idx.Entries))
	for _, entry := range idx.Entries {
		if !unmapped[entry.Name] {
			filtered.Entries = append(filtered.Entries, entry)
		}
	}
	if err := repo.Storer.SetIndex(&filtered); err != nil {
		// SetIndex can truncate the on-disk index before failing. Restore
		// from the saved copy and abort this sync even if restoration succeeds.
		restoreErr := repo.Storer.SetIndex(idx)
		return errors.Join(fmt.Errorf("%w: prepare pull: %w", errManagedIndexRecovery, err), restoreErr)
	}
	defer func() {
		if err := worktree.Reset(&git.ResetOptions{Mode: git.MixedReset}); err != nil {
			result = errors.Join(result, fmt.Errorf("%w: %w", errManagedIndexRecovery, err))
		}
	}()
	return worktree.PullContext(ctx, opts)
}

func managedGitlinks(repo *git.Repository) (map[string]bool, error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("read managed gitlinks: %w", err)
	}
	gitlinks := make(map[string]bool)
	for _, entry := range idx.Entries {
		if entry.Mode == filemode.Submodule {
			gitlinks[entry.Name] = true
		}
	}
	return gitlinks, nil
}

// go-git's Clean(Dir: true) removes even tracked, empty gitlink directories
// and traverses populated submodules. Keep those subtrees opaque: this sync
// archives the parent repository without recursively fetching submodules.
func cleanManagedWorktree(worktree *git.Worktree, gitlinks map[string]bool) error {
	status, err := worktree.Status()
	if err != nil {
		return err
	}
	var clean func(string) error
	clean = func(dir string) error {
		children, err := worktree.Filesystem.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, child := range children {
			name := path.Join(dir, child.Name())
			if child.Name() == ".git" || gitlinks[name] {
				continue
			}
			if child.IsDir() {
				if err := clean(name); err != nil {
					return err
				}
			} else if status.IsUntracked(name) {
				if err := worktree.Filesystem.Remove(name); err != nil {
					return err
				}
			}
		}
		if dir != "" {
			remaining, err := worktree.Filesystem.ReadDir(dir)
			if err != nil {
				return err
			}
			if len(remaining) == 0 {
				return worktree.Filesystem.Remove(dir)
			}
		}
		return nil
	}
	if err := clean(""); err != nil {
		return err
	}
	for name := range gitlinks {
		if err := worktree.Filesystem.MkdirAll(name, 0o755); err != nil {
			return fmt.Errorf("restore submodule directory %s: %w", name, err)
		}
	}
	return nil
}
