package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

// Mutate the real upstream at a phase boundary, keeping discovery, fetch,
// reference storage and archive publication on their production paths.
type missingBranchLogSink struct {
	recSink
	beforeFetch     func()
	beforeDiscovery func()
	errors          []string
}

func (s *missingBranchLogSink) Log(executionID, jobName, level, message string) error {
	_ = s.recSink.Log(executionID, jobName, level, message)
	if level == "error" {
		s.errors = append(s.errors, message)
	}
	if message == "Sync phase: Git fetch started" && s.beforeFetch != nil {
		s.beforeFetch()
	}
	if message == "Sync phase: remote reference discovery started" && s.beforeDiscovery != nil {
		s.beforeDiscovery()
	}
	return nil
}

func bindMissingBranchLogs(t *testing.T, beforeFetch func()) *missingBranchLogSink {
	t.Helper()
	sink := &missingBranchLogSink{beforeFetch: beforeFetch}
	ui.SetSink(sink)
	unbind := ui.Bind("missing-branch-sync", "repo")
	t.Cleanup(func() { unbind(); ui.SetSink(nil) })
	return sink
}

func requireMissingBranchNotice(t *testing.T, sink *missingBranchLogSink, branch string) {
	t.Helper()
	for _, message := range sink.snapshot() {
		if strings.Contains(message, branch) && strings.Contains(message, "no longer exists upstream") {
			return
		}
	}
	t.Fatalf("missing informational notice for disappeared branch %s", branch)
}

func TestSyncKeepsMissingUpstreamBranchesAndLogsNotice(t *testing.T) {
	for _, allBranches := range []bool{false, true} {
		t.Run(fmt.Sprint(allBranches), func(t *testing.T) {
			f := newRefFixture(t, "fix/secret-syncs-alarms", allBranches)
			require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/fix/secret-syncs-alarms"))
			latest := commitRefFile(t, f.remote, f.remoteDir, "main update")
			sink := bindMissingBranchLogs(t, nil)
			require.NoError(t, Sync(context.Background(), f.options, false, nil))
			require.Empty(t, sink.errors)
			requireMissingBranchNotice(t, sink, "fix/secret-syncs-alarms")
			requireRefHash(t, f.local, "refs/remotes/origin/fix/secret-syncs-alarms", f.old)
			if allBranches {
				requireRefHash(t, f.local, "refs/heads/fix/secret-syncs-alarms", f.old)
			}
			requireArchived(t, f.local, "refs/remotes/origin/fix/secret-syncs-alarms", f.old)
			requireRefHash(t, f.local, "refs/heads/main", latest)
		})
	}
}

func TestSyncContinuesWhenBranchesDisappearBetweenDiscoveryAndFetch(t *testing.T) {
	f := newRefFixture(t, "fix/secret-syncs-alarms", true)
	require.NoError(t, f.remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/another-retired", f.old)))
	latest := commitRefFile(t, f.remote, f.remoteDir, "main update")
	deletions := []plumbing.ReferenceName{"refs/heads/fix/secret-syncs-alarms", "refs/heads/another-retired"}
	sink := bindMissingBranchLogs(t, func() {
		if len(deletions) > 0 {
			require.NoError(t, f.remote.Storer.RemoveReference(deletions[0]))
			deletions = deletions[1:]
		}
	})
	destination := t.TempDir()
	storages := []typedef.MultiStorage{{Storage: typedef.Storage{Type: "file", Path: destination}}}
	require.NoError(t, Sync(context.Background(), f.options, false, storages))
	require.Empty(t, sink.errors)
	require.Empty(t, deletions, "successive fetch attempts must handle further branch deletions")
	requireMissingBranchNotice(t, sink, "fix/secret-syncs-alarms")
	requireMissingBranchNotice(t, sink, "another-retired")
	requireRefHash(t, f.local, "refs/heads/fix/secret-syncs-alarms", f.old)
	requireRefHash(t, f.local, "refs/remotes/origin/fix/secret-syncs-alarms", f.old)
	requireArchived(t, f.local, "refs/remotes/origin/fix/secret-syncs-alarms", f.old)
	requireRefHash(t, f.local, "refs/heads/main", latest)
	_, err := os.Stat(filepath.Join(destination, "example.test", "owner", "repo", "repo.tar.gz"))
	require.NoError(t, err, "a disappeared branch must not prevent publishing surviving branch updates")
}

func TestSyncKeepsOldDefaultWhenItDisappearsDuringFetch(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	deleted := false
	sink := bindMissingBranchLogs(t, func() {
		if deleted {
			return
		}
		deleted = true
		require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/main"))
		require.NoError(t, f.remote.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/feature")))
	})
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	require.Empty(t, sink.errors)
	requireMissingBranchNotice(t, sink, "main")
	requireRefHash(t, f.local, "refs/heads/main", f.base)
	requireRefHash(t, f.local, "refs/remotes/origin/main", f.base)
	requireArchived(t, f.local, "refs/remotes/origin/main", f.base)
	head, err := f.local.Head()
	require.NoError(t, err)
	require.Equal(t, plumbing.ReferenceName("refs/heads/feature"), head.Name())
}

func TestSyncKeepsCheckoutWhenUpstreamDefaultWasDeletedBeforeDiscovery(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	// Leave upstream HEAD pointing at main after deleting main, as can happen
	// on a generic Git server. The feature branch must still advance locally.
	worktree, err := f.remote.Worktree()
	require.NoError(t, err)
	require.NoError(t, worktree.Checkout(&git.CheckoutOptions{Branch: "refs/heads/feature"}))
	latest := commitRefFile(t, f.remote, f.remoteDir, "feature update")
	require.NoError(t, worktree.Checkout(&git.CheckoutOptions{Branch: "refs/heads/main"}))
	require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/main"))
	sink := bindMissingBranchLogs(t, nil)
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	require.Empty(t, sink.errors)
	requireMissingBranchNotice(t, sink, "main")
	requireRefHash(t, f.local, "refs/heads/main", f.base)
	requireRefHash(t, f.local, "refs/remotes/origin/main", f.base)
	requireArchived(t, f.local, "refs/remotes/origin/main", f.base)
	requireRefHash(t, f.local, "refs/heads/feature", latest)
	head, err := f.local.Head()
	require.NoError(t, err)
	require.Equal(t, plumbing.ReferenceName("refs/heads/main"), head.Name())
}

func TestSyncRejectsUnexplainedMissingRemoteHEAD(t *testing.T) {
	f := newRefFixture(t, "feature", true)
	// Our cached main is still advertised. An unrelated dangling remote HEAD
	// is not evidence that the default branch we archived was deleted.
	require.NoError(t, f.remote.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/unknown")))
	err := Sync(context.Background(), f.options, false, nil)
	require.ErrorContains(t, err, "remote HEAD does not identify an advertised branch")
	requireRefHash(t, f.local, "refs/heads/main", f.base)
	requireRefHash(t, f.local, "refs/heads/feature", f.old)
}

func TestSyncKeepsHistoryWhenAllUpstreamBranchesDisappear(t *testing.T) {
	for _, duringFetch := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_fetch=%t", duringFetch), func(t *testing.T) {
			f := newRefFixture(t, "retired", true)
			remove := func() {
				require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/main"))
				require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/retired"))
			}
			var beforeFetch func()
			if duringFetch {
				beforeFetch = remove
			} else {
				remove()
			}
			sink := bindMissingBranchLogs(t, beforeFetch)
			require.NoError(t, Sync(context.Background(), f.options, false, nil))
			require.Empty(t, sink.errors)
			requireMissingBranchNotice(t, sink, "main")
			requireMissingBranchNotice(t, sink, "retired")
			requireRefHash(t, f.local, "refs/heads/main", f.base)
			requireRefHash(t, f.local, "refs/heads/retired", f.old)
			requireRefHash(t, f.local, "refs/remotes/origin/main", f.base)
			requireRefHash(t, f.local, "refs/remotes/origin/retired", f.old)
			requireArchived(t, f.local, "refs/remotes/origin/retired", f.old)
			contents, err := os.ReadFile(filepath.Join(f.localDir, "content.txt"))
			require.NoError(t, err)
			require.Equal(t, "base", string(contents))
		})
	}
}

func TestSyncDefersNewConflictingBranchesDiscoveredAfterMissingRef(t *testing.T) {
	f := newRefFixture(t, "retired", true)
	latest := commitRefFile(t, f.remote, f.remoteDir, "main update")
	deleted := false
	bindMissingBranchLogs(t, func() {
		if deleted {
			return
		}
		deleted = true
		require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/retired"))
		require.NoError(t, f.remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/retired/new", latest)))
	})
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireRefHash(t, f.local, "refs/heads/retired", f.old)
	requireRefHash(t, f.local, "refs/remotes/origin/retired", f.old)
	requireRefHash(t, f.local, "refs/heads/main", latest)
	// The next sync examines the new path and archives the conflicting tip
	// before releasing its tracking name. It never deletes the local branch.
	require.NoError(t, Sync(context.Background(), f.options, false, nil))
	requireRefHash(t, f.local, "refs/heads/retired", f.old)
	requireArchived(t, f.local, "refs/remotes/origin/retired", f.old)
	requireRefHash(t, f.local, "refs/remotes/origin/retired/new", latest)
}

func TestSyncMissingBranchRecheckFailureStillFailsAndPreservesCache(t *testing.T) {
	f := newRefFixture(t, "retired", true)
	disappeared := false
	sink := bindMissingBranchLogs(t, func() {
		require.NoError(t, f.remote.Storer.RemoveReference("refs/heads/retired"))
		disappeared = true
	})
	sink.beforeDiscovery = func() {
		if disappeared {
			// Make the real transport fail during re-discovery, after the
			// initial fetch has reported a missing branch.
			moved := f.remoteDir + "-unavailable"
			require.NoError(t, os.Rename(f.remoteDir, moved))
			t.Cleanup(func() { require.NoError(t, os.Rename(moved, f.remoteDir)) })
		}
	}
	destination := t.TempDir()
	storages := []typedef.MultiStorage{{Storage: typedef.Storage{Type: "file", Path: destination}}}
	err := Sync(context.Background(), f.options, false, storages)
	require.ErrorContains(t, err, "recheck remote branches")
	require.NotEmpty(t, sink.errors)
	requireRefHash(t, f.local, "refs/heads/retired", f.old)
	requireRefHash(t, f.local, "refs/remotes/origin/retired", f.old)
	requireArchived(t, f.local, "refs/remotes/origin/retired", f.old)
	_, err = os.Stat(filepath.Join(destination, "example.test", "owner", "repo", "repo.tar.gz"))
	require.True(t, os.IsNotExist(err), "an unconfirmed remote failure must not publish an archive")
}
