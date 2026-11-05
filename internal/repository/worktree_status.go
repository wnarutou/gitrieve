package repository

import (
	"bytes"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
	"github.com/go-git/go-git/v5/utils/merkletrie/filesystem"
	mindex "github.com/go-git/go-git/v5/utils/merkletrie/index"
	"github.com/go-git/go-git/v5/utils/merkletrie/noder"
)

// managedWorktreeStatus compares parent-owned files using the index's gitlinks.
// go-git instead uses every .gitmodules mapping, including stale mappings whose
// paths are now ordinary tracked directories. Those hide their children and
// falsely report them deleted. Keep .gitmodules itself byte-for-byte intact.
func managedWorktreeStatus(repo *git.Repository, worktree *git.Worktree) (git.Status, error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	// Directory hashes are deliberately unknown in these public go-git nodes.
	// Never prune a subtree just because both directory hashes are all zero.
	empty := make([]byte, 24)
	equals := func(a, b noder.Hasher) bool {
		x, y := a.Hash(), b.Hash()
		return !bytes.Equal(x, empty) && !bytes.Equal(y, empty) && bytes.Equal(x, y)
	}
	staged, err := merkletrie.DiffTree(object.NewTreeRootNode(tree), mindex.NewRootNode(idx), equals)
	if err != nil {
		return nil, err
	}
	gitlinks := make(map[string]plumbing.Hash)
	for _, entry := range idx.Entries {
		if entry.Mode == filemode.Submodule {
			gitlinks[entry.Name] = entry.Hash
		}
	}
	unstaged, err := merkletrie.DiffTree(mindex.NewRootNode(idx), filesystem.NewRootNode(worktree.Filesystem, gitlinks), equals)
	if err != nil {
		return nil, err
	}
	patterns, patternErr := gitignore.ReadPatterns(worktree.Filesystem, nil)
	// Match go-git's behavior: a failed ignore read must not hide changes.
	if patternErr != nil {
		patterns = nil
	} else {
		patterns = append(patterns, worktree.Excludes...)
	}
	matcher := gitignore.NewMatcher(patterns)
	status := make(git.Status)
	for _, group := range []struct {
		changes merkletrie.Changes
		staged  bool
	}{{staged, true}, {unstaged, false}} {
		for _, change := range group.changes {
			action, err := change.Action()
			if err != nil {
				return nil, err
			}
			name, code := change.To.String(), git.Modified
			switch action {
			case merkletrie.Delete:
				name, code = change.From.String(), git.Deleted
			case merkletrie.Insert:
				code = git.Added
				if !group.staged {
					if matcher.Match(strings.Split(name, "/"), change.To.IsDir()) {
						continue
					}
					code = git.Untracked
				}
			}
			if status[name] == nil {
				status[name] = &git.FileStatus{Staging: git.Unmodified, Worktree: git.Unmodified}
			}
			if group.staged {
				status[name].Staging = code
			} else {
				status[name].Worktree = code
				if code == git.Untracked && status[name].Staging == git.Unmodified {
					status[name].Staging = git.Untracked
				}
			}
		}
	}
	return status, nil
}
