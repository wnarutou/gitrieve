package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// These tests run Sync against real on-disk repositories and a file transport.
// No network service, global transport override, or mocked fetch is involved.
type refFixture struct {
	remote, local       *git.Repository
	remoteDir, localDir string
	base, old           plumbing.Hash
	options             typedef.Repository
}

func newRefFixture(t *testing.T, oldBranch string, allBranches bool, cloneDepth ...int) *refFixture {
	t.Helper()
	root := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	f := &refFixture{
		remoteDir: filepath.Join(root, "upstream"),
		localDir:  filepath.Join(root, ".gitrieve", "example.test", "owner", "repo", "code"),
		options:   typedef.Repository{Name: "repo", URL: "example.test/owner/repo", UseCache: true, AllBranches: allBranches},
	}
	f.remote, err = git.PlainInitWithOptions(f.remoteDir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	f.base = commitRefFile(t, f.remote, f.remoteDir, "base")
	w, err := f.remote.Worktree()
	require.NoError(t, err)
	require.NoError(t, w.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(oldBranch), Create: true}))
	f.old = commitRefFile(t, f.remote, f.remoteDir, "old branch only")
	require.NoError(t, w.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))
	depth := 0
	if len(cloneDepth) != 0 {
		depth = cloneDepth[0]
		f.options.Depth = depth
	}
	f.local, err = git.PlainClone(f.localDir, false, &git.CloneOptions{URL: f.remoteDir, Depth: depth})
	require.NoError(t, err)
	if allBranches {
		require.NoError(t, f.local.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(oldBranch), f.old)))
	}
	return f
}

func commitRefFile(t *testing.T, repo *git.Repository, dir, content string) plumbing.Hash {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "content.txt"), []byte(content), 0600))
	w, err := repo.Worktree()
	require.NoError(t, err)
	_, err = w.Add("content.txt")
	require.NoError(t, err)
	hash, err := w.Commit(content, &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.test", When: time.Now()}})
	require.NoError(t, err)
	return hash
}

func replaceRemoteBranch(t *testing.T, f *refFixture, old, next string) plumbing.Hash {
	t.Helper()
	require.NoError(t, f.remote.Storer.RemoveReference(plumbing.NewBranchReferenceName(old)))
	// go-git leaves empty directories behind when removing loose references.
	for dir := filepath.Dir(filepath.FromSlash(old)); dir != "."; dir = filepath.Dir(dir) {
		require.NoError(t, os.Remove(filepath.Join(f.remoteDir, ".git", "refs", "heads", dir)))
	}
	hash := commitRefFile(t, f.remote, f.remoteDir, "new branch")
	require.NoError(t, f.remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(next), hash)))
	return hash
}

func requireRefHash(t *testing.T, repo *git.Repository, name string, hash plumbing.Hash) {
	t.Helper()
	ref, err := repo.Reference(plumbing.ReferenceName(name), true)
	require.NoError(t, err)
	require.Equal(t, hash, ref.Hash(), name)
}

func requireArchived(t *testing.T, repo *git.Repository, original string, hash plumbing.Hash) {
	t.Helper()
	refs, err := repo.References()
	require.NoError(t, err)
	found := false
	require.NoError(t, refs.ForEach(func(ref *plumbing.Reference) error {
		if !strings.HasPrefix(ref.Name().String(), "refs/gitrieve/archive/") || ref.Hash() != hash {
			return nil
		}
		parts := strings.Split(strings.TrimPrefix(ref.Name().String(), "refs/gitrieve/archive/"), "/")
		decoded, err := hex.DecodeString(strings.Join(parts[:len(parts)-1], ""))
		require.NoError(t, err)
		if string(decoded) == original {
			found = true
		}
		return nil
	}))
	require.True(t, found, "old tip must have a durable, recoverable reference: %s", original)
	_, err = repo.CommitObject(hash)
	require.NoError(t, err)
}

func TestSyncPreservesHistoryAcrossRefPathConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		all, packed     bool
	}{
		{"file_to_directory", "noa/v8", "noa/v8/new", true, false},
		{"directory_to_file", "noa/v8/old", "noa/v8", true, false},
		{"tracking_only", "noa/v8", "noa/v8/new", false, false},
		{"packed_refs", "noa/v8/old", "noa/v8", true, true},
		{"long_encoded_names", strings.Repeat("a", 90), strings.Repeat("a", 90) + "/new", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefFixture(t, tc.old, tc.all)
			if tc.packed {
				require.NoError(t, f.local.Storer.(*filesystem.Storage).PackRefs())
			}
			// A non-conflicting, deleted upstream reference must remain untouched.
			require.NoError(t, f.local.Storer.SetReference(plumbing.NewHashReference("refs/remotes/origin/retired", f.old)))
			newHash := replaceRemoteBranch(t, f, tc.old, tc.next)
			// Exercise the final default-branch checkout through the alias as well.
			require.NoError(t, f.remote.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(tc.next))))
			require.NoError(t, Sync(context.Background(), f.options, false, nil))
			requireArchived(t, f.local, "refs/remotes/origin/"+tc.old, f.old)
			requireRefHash(t, f.local, "refs/remotes/origin/retired", f.old)
			requireRefHash(t, f.local, "refs/remotes/origin/"+tc.next, newHash)
			if tc.all {
				requireRefHash(t, f.local, "refs/heads/"+tc.old, f.old)
			}
			head, err := f.local.Head()
			require.NoError(t, err)
			require.Equal(t, newHash, head.Hash())
			content, err := os.ReadFile(filepath.Join(f.localDir, "content.txt"))
			require.NoError(t, err)
			require.Equal(t, "new branch", string(content))
			cfg, err := f.local.Config()
			require.NoError(t, err)
			require.Equal(t, plumbing.NewBranchReferenceName(tc.next), cfg.Branches[head.Name().Short()].Merge)
			// Repeated syncs keep the same branch mapping and advance its content.
			nextHash := commitRefFile(t, f.remote, f.remoteDir, "next update")
			require.NoError(t, Sync(context.Background(), f.options, false, nil))
			nextHead, err := f.local.Head()
			require.NoError(t, err)
			require.Equal(t, head.Name(), nextHead.Name())
			require.Equal(t, nextHash, nextHead.Hash())
			if tc.all {
				requireRefHash(t, f.local, "refs/heads/"+tc.old, f.old)
			}
		})
	}
}

func TestSyncArchivesTrackingOnlyHistoryBeforeForcePush(t *testing.T) {
	f := newRefFixture(t, "feature", false)
	require.NoError(t, f.remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/feature", f.base)))
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireArchived(t, f.local, "refs/remotes/origin/feature", f.old)
	requireRefHash(t, f.local, "refs/remotes/origin/feature", f.base)
}

func TestSyncArchiveFailureLeavesConflictingReferenceUntouched(t *testing.T) {
	f := newRefFixture(t, "noa/v8", true)
	replaceRemoteBranch(t, f, "noa/v8", "noa/v8/new")
	// A file blocking the archive namespace simulates a real write failure.
	blocker := filepath.Join(f.localDir, ".git", "refs", "gitrieve")
	require.NoError(t, os.WriteFile(blocker, []byte("blocked"), 0600))
	err := Sync(context.Background(), f.options, false, nil)
	require.Error(t, err)
	requireRefHash(t, f.local, "refs/remotes/origin/noa/v8", f.old)
	requireRefHash(t, f.local, "refs/heads/noa/v8", f.old)
	require.NoError(t, os.Remove(blocker))
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireArchived(t, f.local, "refs/remotes/origin/noa/v8", f.old)
}

type interruptRefRemoval struct {
	gitstorage.Storer
	cancel context.CancelFunc
}

func (s *interruptRefRemoval) Filesystem() billy.Filesystem {
	return s.Storer.(refFilesystem).Filesystem()
}

func (s *interruptRefRemoval) RemoveReference(name plumbing.ReferenceName) error {
	err := s.Storer.RemoveReference(name)
	if err == nil {
		s.cancel()
	}
	return err
}

func TestSyncResumesAfterInterruptedConflictRelocation(t *testing.T) {
	f := newRefFixture(t, "noa/v8/old", true)
	newHash := replaceRemoteBranch(t, f, "noa/v8/old", "noa/v8")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := f.local.Storer
	f.local.Storer = &interruptRefRemoval{Storer: original, cancel: cancel}
	_, err := syncBranches(ctx, f.local, true, nil, newGitAccounts(context.Background(), "example.test"), "code")
	require.ErrorIs(t, err, context.Canceled)
	f.local.Storer = original
	requireArchived(t, f.local, "refs/remotes/origin/noa/v8/old", f.old)
	requireRefHash(t, f.local, "refs/heads/noa/v8/old", f.old)
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireRefHash(t, f.local, "refs/remotes/origin/noa/v8", newHash)
}

func TestSyncUnavailableRemoteLeavesReferencesUntouched(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	before, err := f.local.References()
	require.NoError(t, err)
	want := make(map[string]string)
	require.NoError(t, before.ForEach(func(ref *plumbing.Reference) error { want[ref.Name().String()] = ref.String(); return nil }))
	cfg, err := f.local.Config()
	require.NoError(t, err)
	cfg.Remotes["origin"].URLs = []string{filepath.Join(t.TempDir(), "missing")}
	require.NoError(t, f.local.SetConfig(cfg))
	require.Error(t, Sync(context.Background(), f.options, false, nil))
	after, err := f.local.References()
	require.NoError(t, err)
	got := make(map[string]string)
	require.NoError(t, after.ForEach(func(ref *plumbing.Reference) error { got[ref.Name().String()] = ref.String(); return nil }))
	require.Equal(t, want, got)
}

func TestSyncForcePushPreservesLocalBranchAndArchivedTipAfterGC(t *testing.T) {
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git executable needed to verify archive reachability after GC")
	}
	f := newRefFixture(t, "feature", true)
	require.NoError(t, f.remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/feature", f.base)))
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireRefHash(t, f.local, "refs/heads/feature", f.old)
	// Remove the test's extra local branch so only our archive protects its tip.
	require.NoError(t, f.local.Storer.RemoveReference("refs/heads/feature"))
	// The fixture path exceeds Windows MAX_PATH; native Git must use the
	// same long-path support as Go when traversing the archive references.
	cmd := exec.Command(gitExe, "-c", "core.longpaths=true", "-C", f.localDir, "gc", "--prune=now", "--quiet")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	reopened, err := git.PlainOpen(f.localDir)
	require.NoError(t, err)
	requireArchived(t, reopened, "refs/remotes/origin/feature", f.old)
}

func TestSyncRetriesArchiveStorageAfterReferencesAreAlreadyUpdated(t *testing.T) {
	f := newRefFixture(t, "feature", false)
	bad := []typedef.MultiStorage{{Storage: typedef.Storage{Type: "invalid"}}}
	require.Error(t, Sync(context.Background(), f.options, false, bad))
	destination := t.TempDir()
	good := []typedef.MultiStorage{{Storage: typedef.Storage{Type: "file", Path: destination}}}
	require.NoError(t, Sync(context.Background(), f.options, false, good))
	_, err := os.Stat(filepath.Join(destination, "example.test", "owner", "repo", "repo.tar.gz"))
	require.NoError(t, err, "retry must publish the saved history even if fetch is now up to date")
}

func TestLocalBranchAliasCannotBeReusedByADifferentRemoteBranch(t *testing.T) {
	f := newRefFixture(t, "noa/v8", true)
	first, err := localBranchForRemote(f.local, "refs/heads/noa/v8/new")
	require.NoError(t, err)
	require.NoError(t, f.local.Storer.SetReference(plumbing.NewHashReference(first, f.old)))
	// An upstream branch may legitimately have the name chosen for our alias.
	second, err := localBranchForRemote(f.local, first)
	require.NoError(t, err)
	require.NotEqual(t, first, second, "an alias already tracking another remote must not be commandeered")
	again, err := localBranchForRemote(f.local, "refs/heads/noa/v8/new")
	require.NoError(t, err)
	require.Equal(t, first, again)
	requireRefHash(t, f.local, first.String(), f.old)
}

type failedArchiveWrite struct{ gitstorage.Storer }

func (s *failedArchiveWrite) Filesystem() billy.Filesystem {
	return s.Storer.(refFilesystem).Filesystem()
}

func (s *failedArchiveWrite) SetReference(ref *plumbing.Reference) error {
	if strings.HasPrefix(ref.Name().String(), "refs/gitrieve/archive/") {
		return errors.New("archive storage failed")
	}
	return s.Storer.SetReference(ref)
}

func TestArchiveWriteFailureNeverRemovesConflictingRef(t *testing.T) {
	f := newRefFixture(t, "noa/v8", true)
	replaceRemoteBranch(t, f, "noa/v8", "noa/v8/new")
	f.local.Storer = &failedArchiveWrite{Storer: f.local.Storer}
	_, err := syncBranches(context.Background(), f.local, true, nil, newGitAccounts(context.Background(), "example.test"), "code")
	require.ErrorContains(t, err, "archive storage failed")
	requireRefHash(t, f.local, "refs/remotes/origin/noa/v8", f.old)
}

type corruptArchiveWrite struct {
	gitstorage.Storer
	wrongHash plumbing.Hash
}

func (s *corruptArchiveWrite) Filesystem() billy.Filesystem {
	return s.Storer.(refFilesystem).Filesystem()
}

func (s *corruptArchiveWrite) SetReference(ref *plumbing.Reference) error {
	if strings.HasPrefix(ref.Name().String(), "refs/gitrieve/archive/") {
		return s.Storer.SetReference(plumbing.NewHashReference(ref.Name(), s.wrongHash))
	}
	return s.Storer.SetReference(ref)
}

func TestArchiveVerificationFailureNeverRemovesConflictingRef(t *testing.T) {
	f := newRefFixture(t, "noa/v8", true)
	replaceRemoteBranch(t, f, "noa/v8", "noa/v8/new")
	f.local.Storer = &corruptArchiveWrite{Storer: f.local.Storer, wrongHash: f.base}
	_, err := syncBranches(context.Background(), f.local, true, nil, newGitAccounts(context.Background(), "example.test"), "code")
	require.ErrorContains(t, err, "archive verification failed")
	requireRefHash(t, f.local, "refs/remotes/origin/noa/v8", f.old)
	requireRefHash(t, f.local, "refs/heads/noa/v8", f.old)
}

func TestSyncShallowCacheAdvancesAcrossMultipleUpstreamCommits(t *testing.T) {
	f := newRefFixture(t, "feature", false, 1)
	// go-git's merge uses the first shallow boundary. Keep main first so this
	// test isolates fetch depth rather than its unrelated multi-branch ordering.
	require.NoError(t, f.local.Storer.SetShallow([]plumbing.Hash{f.base, f.old}))
	commitRefFile(t, f.remote, f.remoteDir, "one")
	commitRefFile(t, f.remote, f.remoteDir, "two")
	latest := commitRefFile(t, f.remote, f.remoteDir, "three")
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireRefHash(t, f.local, "refs/heads/main", latest)
}

type requirePendingBeforeMerge struct{ gitstorage.Storer }

func (s *requirePendingBeforeMerge) Filesystem() billy.Filesystem {
	return s.Storer.(refFilesystem).Filesystem()
}

func (s *requirePendingBeforeMerge) SetReference(ref *plumbing.Reference) error {
	if ref.Name() == "refs/heads/feature" {
		current, err := s.Storer.Reference(ref.Name())
		if err != nil {
			return err
		}
		if _, err := s.Filesystem().Stat("gitrieve-sync-pending"); current.Hash() != ref.Hash() && err != nil {
			return errors.New("local branch changed without pending archive marker")
		}
	}
	return s.Storer.SetReference(ref)
}

func TestSyncRecordsPendingBeforeAdvancingPreviouslySkippedBranch(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	w, err := f.remote.Worktree()
	require.NoError(t, err)
	require.NoError(t, w.Checkout(&git.CheckoutOptions{Branch: "refs/heads/feature"}))
	latest := commitRefFile(t, f.remote, f.remoteDir, "feature update")
	require.NoError(t, w.Checkout(&git.CheckoutOptions{Branch: "refs/heads/main"}))
	f.options.AllBranches = false
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireRefHash(t, f.local, "refs/heads/feature", f.old)
	f.local, err = git.PlainOpen(f.localDir)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(f.localDir, ".git", "gitrieve-sync-pending"))
	require.True(t, os.IsNotExist(err))
	f.local.Storer = &requirePendingBeforeMerge{Storer: f.local.Storer}
	_, err = syncBranches(context.Background(), f.local, true, nil, newGitAccounts(context.Background(), "example.test"), "code")
	require.NoError(t, err)
	requireRefHash(t, f.local, "refs/heads/feature", latest)
}

func TestSyncPublishesWhenOnlyDefaultBranchChanges(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	require.NoError(t, f.remote.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/feature")))
	destination := t.TempDir()
	storages := []typedef.MultiStorage{{Storage: typedef.Storage{Type: "file", Path: destination}}}
	require.NoError(t, Sync(context.Background(), f.options, false, storages))
	_, err := os.Stat(filepath.Join(destination, "example.test", "owner", "repo", "repo.tar.gz"))
	require.NoError(t, err, "changing default branch changes the archived working tree")
	head, err := f.local.Head()
	require.NoError(t, err)
	require.Equal(t, plumbing.ReferenceName("refs/heads/feature"), head.Name())
}
