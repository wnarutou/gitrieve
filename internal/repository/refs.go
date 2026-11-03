package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/wnarutou/gitrieve/internal/ui"
)

const originRefPrefix = "refs/remotes/origin/"

const pendingSyncFile = "gitrieve-sync-pending"

type refFilesystem interface{ Filesystem() billy.Filesystem }

func pendingBranchSync(repo *git.Repository) (bool, error) {
	store, ok := repo.Storer.(refFilesystem)
	if !ok {
		return false, fmt.Errorf("branch sync requires filesystem storage")
	}
	_, err := store.Filesystem().Stat(pendingSyncFile)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Keep this marker until every configured archive destination succeeds. A
// cancelled sync or failed upload must not turn into an up-to-date no-op on retry.
func recordBranchSync(repo *git.Repository) error {
	pending, err := pendingBranchSync(repo)
	if err != nil || pending {
		return err
	}
	f, err := repo.Storer.(refFilesystem).Filesystem().Create(pendingSyncFile)
	if err != nil {
		return err
	}
	return f.Close()
}

func completeBranchSync(repo *git.Repository) error {
	err := repo.Storer.(refFilesystem).Filesystem().Remove(pendingSyncFile)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// syncBranches must run under the repository's existing component lock.
// Archive refs are additive: even a tracking-only branch or a force-pushed tip
// remains reachable. Only conflicting tracking names may be removed, and only
// after their durable archive refs have been verified.
func syncBranches(ctx context.Context, repo *git.Repository, allBranches bool, progress io.Writer, accounts *gitAccounts, component string) (bool, error) {
	remote, err := repo.Remote("origin")
	if err != nil {
		return false, err
	}
	accounts.restrictRemote(remote.Config().URLs)
	ui.Printf("Sync phase: remote reference discovery started")
	var advertised []*plumbing.Reference
	err = accounts.do(ctx, func(auth transport.AuthMethod) error {
		return retryGitEOF(ctx, component+" reference discovery", func() error {
			var listErr error
			advertised, listErr = remote.ListContext(ctx, &git.ListOptions{Auth: auth})
			return listErr
		})
	})
	if err != nil {
		return false, fmt.Errorf("list remote branches: %w", err)
	}

	var defaultBranch plumbing.ReferenceName
	var branches []plumbing.ReferenceName
	for _, ref := range advertised {
		if ref.Name() == plumbing.HEAD {
			defaultBranch = ref.Target()
		}
		if ref.Name().IsBranch() && ref.Type() == plumbing.HashReference {
			if err := ref.Name().Validate(); err != nil {
				return false, err
			}
			branches = append(branches, ref.Name())
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i] < branches[j] })
	defaultFound := false
	for _, branch := range branches {
		if branch == defaultBranch {
			defaultFound = true
		}
	}
	if !defaultFound {
		return false, fmt.Errorf("remote HEAD does not identify an advertised branch")
	}

	changed, err := archiveTrackingRefs(ctx, repo)
	if err != nil {
		return changed, err
	}
	if err := relocateConflictingRefs(ctx, repo, branches); err != nil {
		return changed, err
	}

	// Fetch precisely the listed branches. If upstream changes between list and
	// fetch, fail safely instead of fetching an unexamined conflicting path.
	specs := make([]config.RefSpec, 0, len(branches))
	for _, branch := range branches {
		specs = append(specs, config.RefSpec("+"+branch.String()+":"+originRefPrefix+branch.Short()))
	}
	for _, ref := range advertised {
		if !ref.Name().IsBranch() || ref.Type() != plumbing.HashReference {
			continue
		}
		current, err := repo.Reference(plumbing.ReferenceName(originRefPrefix+ref.Name().Short()), false)
		if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return changed, err
		}
		if current == nil || current.Hash() != ref.Hash() {
			if err := recordBranchSync(repo); err != nil {
				return changed, err
			}
			break
		}
	}
	// Keep the existing unbounded fetch behavior even for a shallow initial
	// clone. Limiting this fetch can leave an ancestry gap between cached tips.
	ui.Printf("Sync phase: Git fetch started")
	err = accounts.do(ctx, func(auth transport.AuthMethod) error {
		return retryGitEOF(ctx, component+" fetch", func() error {
			// A failed previous attempt may have updated some tracking refs.
			// Protect those tips too before a retry or account fallback fetch.
			if _, err := archiveTrackingRefs(ctx, repo); err != nil {
				return err
			}
			fetchErr := repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", RefSpecs: specs, Force: true, Progress: progress, Auth: auth})
			if store, ok := repo.Storer.(interface{ operationError(error) error }); ok {
				return store.operationError(fetchErr)
			}
			return fetchErr
		})
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return changed, fmt.Errorf("fetch remote branches: %w", err)
	}
	changed = changed || err == nil
	if err == nil {
		if err := recordBranchSync(repo); err != nil {
			return changed, err
		}
	}
	archived, err := archiveTrackingRefs(ctx, repo)
	changed = changed || archived
	if err != nil {
		return changed, err
	}

	w, err := repo.Worktree()
	if err != nil {
		return changed, err
	}
	initialHead, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return changed, err
	}
	var defaultLocal plumbing.ReferenceName
	for _, branch := range branches {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		if !allBranches && branch != defaultBranch {
			continue
		}
		tracking, err := repo.Reference(plumbing.ReferenceName(originRefPrefix+branch.Short()), true)
		if err != nil {
			return changed, err
		}
		local, err := localBranchForRemote(repo, branch)
		if err != nil {
			return changed, err
		}
		if branch == defaultBranch {
			defaultLocal = local
		}
		if branch != defaultBranch {
			wasPending, err := pendingBranchSync(repo)
			if err != nil {
				return changed, err
			}
			if err := recordBranchSync(repo); err != nil {
				return changed, err
			}
			updated, err := updateArchivedBranch(repo, local.Short(), tracking.Hash())
			if errors.Is(err, git.ErrNonFastForwardUpdate) {
				ui.Printf("Keeping local branch %s unchanged: remote history is not a fast-forward; remote tip is archived.\n", local.Short())
			} else if err != nil {
				return changed, err
			}
			if !updated && !wasPending {
				if err := completeBranchSync(repo); err != nil {
					return changed, err
				}
			}
			changed = changed || updated
			continue
		}
		current, err := repo.Reference(local, false)
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			// Save the mapping before creating the branch so a retry can reuse it.
			if err := recordBranchSync(repo); err != nil {
				return changed, err
			}
			if err := checkoutManagedWorktree(repo, w, &git.CheckoutOptions{Branch: local, Create: true, Force: true, Hash: tracking.Hash()}); err != nil {
				return changed, err
			}
			changed = true
			continue
		}
		if err != nil {
			return changed, err
		}
		if current.Hash() == tracking.Hash() {
			continue
		}
		if err := checkoutManagedWorktree(repo, w, &git.CheckoutOptions{Branch: local, Force: true}); err != nil {
			return changed, err
		}
		if err := restoreManagedWorktree(repo, w); err != nil {
			return changed, err
		}
		// Pull uses the same fast-forward-only merge, but also fetches again.
		// Merge the already fetched and archived tip so later network fetches
		// cannot overwrite an unprotected tip or introduce a new path conflict.
		wasPending, err := pendingBranchSync(repo)
		if err != nil {
			return changed, err
		}
		if err := recordBranchSync(repo); err != nil {
			return changed, err
		}
		if err := repo.Merge(*tracking, git.MergeOptions{Strategy: git.FastForwardMerge}); err != nil {
			if errors.Is(err, git.ErrFastForwardMergeNotPossible) {
				// No ref changed. Avoid republishing an unchanged divergent branch
				// on every run, but retain any pending work from earlier operations.
				if !wasPending {
					if err := completeBranchSync(repo); err != nil {
						return changed, err
					}
				}
				ui.Printf("Keeping local branch %s unchanged: remote history is not a fast-forward; remote tip is archived.\n", local.Short())
				continue
			}
			return changed, err
		}
		changed = true
		if err := checkoutManagedWorktree(repo, w, &git.CheckoutOptions{Branch: local, Force: true}); err != nil {
			return changed, err
		}
		ui.Printf("local branch %s has successed pull from remote branch origin/%s, already up to date. \n", local.Short(), branch.Short())
	}
	if err := ctx.Err(); err != nil {
		return changed, err
	}
	if initialHead.Type() != plumbing.SymbolicReference || initialHead.Target() != defaultLocal {
		if err := recordBranchSync(repo); err != nil {
			return changed, err
		}
		changed = true
	}
	if err := checkoutManagedWorktree(repo, w, &git.CheckoutOptions{Branch: defaultLocal, Force: true}); err != nil {
		return changed, err
	}
	if err := restoreManagedWorktree(repo, w); err != nil {
		return changed, err
	}
	return changed, nil
}

func archiveRefName(ref *plumbing.Reference) plumbing.ReferenceName {
	// Reversible lowercase hex avoids case folding and invalid path characters.
	// Chunk long names below filesystem component limits. A non-hex leaf keeps
	// encoded prefix names from conflicting with longer encoded names.
	encoded := hex.EncodeToString([]byte(ref.Name()))
	parts := []string{"refs/gitrieve/archive"}
	for len(encoded) > 120 {
		parts = append(parts, encoded[:120])
		encoded = encoded[120:]
	}
	parts = append(parts, encoded, "tip-"+ref.Hash().String())
	return plumbing.ReferenceName(strings.Join(parts, "/"))
}

func archiveTrackingRefs(ctx context.Context, repo *git.Repository) (bool, error) {
	refs, err := repo.References()
	if err != nil {
		return false, err
	}
	changed := false
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(ref.Name().String(), originRefPrefix) || ref.Type() != plumbing.HashReference {
			return nil
		}
		name := archiveRefName(ref)
		existing, err := repo.Reference(name, false)
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			if _, err := repo.CommitObject(ref.Hash()); err != nil {
				return fmt.Errorf("cannot archive %s: %w", ref.Name(), err)
			}
			if err := recordBranchSync(repo); err != nil {
				return err
			}
			if err := repo.Storer.SetReference(plumbing.NewHashReference(name, ref.Hash())); err != nil {
				return fmt.Errorf("archive %s: %w", ref.Name(), err)
			}
			changed = true
			existing, err = repo.Reference(name, false)
		}
		if err != nil {
			return err
		}
		if existing.Type() != plumbing.HashReference || existing.Hash() != ref.Hash() {
			return fmt.Errorf("archive verification failed for %s", ref.Name())
		}
		return nil
	})
	return changed, err
}

func refPathsConflict(a, b string) bool {
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func relocateConflictingRefs(ctx context.Context, repo *git.Repository, branches []plumbing.ReferenceName) error {
	refs, err := repo.References()
	if err != nil {
		return err
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(ref.Name().String(), originRefPrefix) {
			return nil
		}
		for _, branch := range branches {
			if !refPathsConflict(ref.Name().String(), originRefPrefix+branch.Short()) {
				continue
			}
			if ref.Type() != plumbing.HashReference {
				return fmt.Errorf("conflicting symbolic reference %s requires manual inspection", ref.Name())
			}
			archived, err := repo.Reference(archiveRefName(ref), false)
			if err != nil {
				return err
			}
			if archived.Type() != plumbing.HashReference || archived.Hash() != ref.Hash() {
				return fmt.Errorf("missing verified archive for %s", ref.Name())
			}
			if err := recordBranchSync(repo); err != nil {
				return err
			}
			if err := repo.Storer.RemoveReference(ref.Name()); err != nil {
				return err
			}
			ui.Printf("Preserved %s at %s; released conflicting remote-tracking name.\n", ref.Name(), archived.Name())
			break
		}
		return nil
	})
	if err != nil {
		return err
	}
	// RemoveRef leaves directories behind. Only remove empty directories under
	// the incoming origin ref paths, including after an interrupted earlier run.
	fs, ok := repo.Storer.(refFilesystem)
	if !ok {
		return nil
	}
	for _, branch := range branches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeEmptyRefDirs(fs.Filesystem(), originRefPrefix+branch.Short()); err != nil {
			return err
		}
	}
	return nil
}

func removeEmptyRefDirs(fs billy.Filesystem, name string) error {
	info, err := fs.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := fs.ReadDir(name)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := removeEmptyRefDirs(fs, path.Join(name, entry.Name())); err != nil {
				return err
			}
		}
	}
	entries, err = fs.ReadDir(name)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("ref path %s still contains files; refusing to remove them", name)
	}
	return fs.Remove(name)
}

// Persist aliases using Git's ordinary branch upstream configuration. Existing
// local branches never move just to make room for a new remote branch name.
func localBranchForRemote(repo *git.Repository, remote plumbing.ReferenceName) (plumbing.ReferenceName, error) {
	cfg, err := repo.Config()
	if err != nil {
		return "", err
	}
	refs, err := repo.References()
	if err != nil {
		return "", err
	}
	occupied := make(map[plumbing.ReferenceName]bool)
	if err := refs.ForEach(func(ref *plumbing.Reference) error { occupied[ref.Name()] = true; return nil }); err != nil {
		return "", err
	}
	available := func(candidate plumbing.ReferenceName) bool {
		for name := range occupied {
			if refPathsConflict(name.String(), candidate.String()) {
				return false
			}
		}
		return true
	}
	base := fmt.Sprintf("gitrieve-%x", sha256.Sum256([]byte(remote)))
	// Reuse a recorded alias even if the original name has since become free.
	for _, branch := range cfg.Branches {
		if branch.Remote == "origin" && branch.Merge == remote && (branch.Name == base || strings.HasPrefix(branch.Name, base+"-")) {
			local := plumbing.NewBranchReferenceName(branch.Name)
			if available(local) {
				return local, nil
			}
		}
	}
	local := remote
	configured := cfg.Branches[local.Short()]
	ownedByAnotherRemote := configured != nil && configured.Remote == "origin" && configured.Merge != remote
	if !available(local) || ownedByAnotherRemote {
		for suffix := 0; ; suffix++ {
			name := base
			if suffix != 0 {
				name = fmt.Sprintf("%s-%d", base, suffix)
			}
			candidate := plumbing.NewBranchReferenceName(name)
			if !occupied[candidate] && cfg.Branches[name] == nil && available(candidate) {
				local = candidate
				break
			}
		}
		ui.Printf("Using local branch %s for remote branch %s; preserving existing local branch names.\n", local.Short(), remote.Short())
	}
	name := local.Short()
	if branch := cfg.Branches[name]; branch == nil {
		cfg.Branches[name] = &config.Branch{Name: name, Remote: "origin", Merge: remote}
		if err := recordBranchSync(repo); err != nil {
			return "", err
		}
		if err := repo.SetConfig(cfg); err != nil {
			return "", err
		}
	}
	return local, nil
}
