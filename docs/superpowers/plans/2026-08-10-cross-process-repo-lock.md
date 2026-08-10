# Cross-Process Per-Repo Sync Lock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serialize same-repo + same-component syncs across goroutines and processes so concurrent writers never corrupt the shared `.git` object database or storage paths.

**Architecture:** A new `internal/lock` package exposes `Acquire(ctx, r *scm.Repository, component) (release func(), err error)`, a two-layer lock: a per-key in-process binary semaphore (ctx-aware) plus a cross-process OS advisory file lock via `github.com/gofrs/flock` on `.gitrieve/locks/<host>/<owner>/<repo>/<component>.lock`. Each component sync function (`repository.Sync` for code/wiki, `issue.Sync`, `discussion.Sync`, `release.DownloadAllAssets`) acquires the lock at its top and releases via `defer`. No function signatures change.

**Tech Stack:** Go 1.23, `github.com/gofrs/flock v0.12.1` (only new dependency), standard `sync`/`context`, `testify` for tests.

## Global Constraints

- Go 1.23 (go.mod already declares `go 1.23.0`); do not bump it.
- Only new dependency: `github.com/gofrs/flock v0.12.1`. Use `go get github.com/gofrs/flock@v0.12.1`. (Ruling: v0.13.0 declares `go 1.24.0` and pulls `x/sys v0.37.0`, incompatible with the Go 1.23 constraint above; v0.12.1 has the identical API — `New`, `TryLockContext(ctx, retryDelay) (bool, error)`, `Unlock`, `Close`.)
- Lock key = `path.Join(r.Host, r.Owner, r.Name, component)`; component ∈ `{code, wiki, issue, discussion, release}`.
- Lock file = `<cwd>/.gitrieve/locks/<host>/<owner>/<repo>/<component>.lock`, created via `os.MkdirAll`, **never deleted**.
- Lock acquisition order: in-process semaphore first, then `flock.TryLockContext` (both honor `ctx`; a pre-cancelled or mid-wait-cancelled ctx returns `ctx.Err()`).
- **wiki double-lock avoidance:** only `repository.Sync` acquires a lock (keyed `code` or `wiki` by `iswiki`); `wiki.Sync` itself MUST NOT call `lock.Acquire` (it delegates to `repository.Sync` — locking twice deadlocks).
- No component function signature changes; daemon/executor/CLI call sites are untouched.
- Scope boundary (document, do not implement): lock protects same host + same working directory only; multi-host S3 writes and the existing `os.Chdir` archive race are out of scope.
- Clean up test-created `.gitrieve` dirs with `os.RemoveAll(".gitrieve")` in `t.Cleanup`.

---
## Task 1: `internal/lock` package with two-layer Acquire

**Files:**
- Modify: `go.mod`, `go.sum` (via `go get`)
- Create: `internal/lock/lock.go`
- Create: `internal/lock/lock_test.go`

**Interfaces:**
- Produces: `Acquire(ctx context.Context, r *scm.Repository, component string) (release func(), err error)` — exclusive lock for the (repo, component) key. Returns a release function that must be called exactly once; on any error returns nil release. Blocked waits are interrupted by `ctx` cancellation (returns `ctx.Err()`).

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/gofrs/flock@v0.12.1`
Expected: go.mod gains `require github.com/gofrs/flock v0.12.1`; no other code changes.

- [ ] **Step 2: Write the failing tests**

Create `internal/lock/lock_test.go`:

```go
package lock

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/scm"
)

func TestAcquireWithCancelledCtxFailsFast(t *testing.T) {
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Acquire(ctx, r, "code")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAcquireDifferentKeysDoNotBlock(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	a := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo-a"}
	b := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo-b"}

	releaseA, err := Acquire(context.Background(), a, "code")
	require.NoError(t, err)
	defer releaseA()

	releaseB, err := Acquire(context.Background(), b, "code")
	require.NoError(t, err)
	releaseB()
}

func TestAcquireSameKeySerializes(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo"}

	releaseA, err := Acquire(context.Background(), r, "code")
	require.NoError(t, err)

	acquired := make(chan struct{})
	var acquireErr error
	go func() {
		releaseB, err := Acquire(context.Background(), r, "code")
		if err == nil {
			releaseB()
		}
		acquireErr = err
		close(acquired)
	}()

	// Second Acquire must block while the first holds the lock.
	select {
	case <-acquired:
		t.Fatal("second Acquire should block while the first holds the lock")
	case <-time.After(200 * time.Millisecond):
	}

	releaseA() // unblock the waiter

	select {
	case <-acquired:
		require.NoError(t, acquireErr)
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire should proceed after the first releases")
	}
}

func TestAcquireCancelledWhileWaiting(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo"}

	releaseA, err := Acquire(context.Background(), r, "code")
	require.NoError(t, err)
	defer releaseA()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		releaseB, err := Acquire(ctx, r, "code")
		if err == nil {
			releaseB()
		}
		result <- err
	}()

	time.Sleep(100 * time.Millisecond) // let the waiter start blocking
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire should return when the context is cancelled while waiting")
	}
}

// TestCrossProcessLock proves the file lock excludes a second *process*, and
// that the OS releases it when the holder is killed.
func TestCrossProcessLock(t *testing.T) {
	if os.Getenv("LOCK_HELPER") == "1" {
		// Child: acquire and hold until killed; never release explicitly.
		r := &scm.Repository{Host: "github.com", Owner: "test", Name: "xproc-repo"}
		release, err := Acquire(context.Background(), r, "code")
		if err != nil {
			fmt.Fprintf(os.Stderr, "child acquire: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("LOCKED")
		time.Sleep(60 * time.Second)
		_ = release
		os.Exit(0)
	}

	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "xproc-repo"}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessLock$")
	cmd.Env = append(os.Environ(), "LOCK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	// Always reap the child, even if a later assertion fails.
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	lockedCh := make(chan struct{})
	go func() {
		br := bufio.NewReader(stdout)
		line, _ := br.ReadString('\n')
		if strings.Contains(line, "LOCKED") {
			close(lockedCh)
		}
	}()
	select {
	case <-lockedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("child process never acquired the lock")
	}

	// The child holds the lock: a same-key Acquire from this process blocks.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, err = Acquire(ctx, r, "code")
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded, "parent must be blocked while the child holds the lock")

	// Killing the child releases the lock (advisory lock dies with the process).
	// cmd.Wait returns *exec.ExitError here: the child was killed, so it did not
	// exit cleanly — that is expected, not a failure.
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	killed = true

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	release, err := Acquire(ctx, r, "code")
	cancel()
	require.NoError(t, err)
	release()
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/lock/`
Expected: build failure — `undefined: Acquire` (the package has no implementation yet). This is the red phase.

- [ ] **Step 4: Write the implementation**

Create `internal/lock/lock.go`:

```go
// Package lock serializes concurrent writes to the same gitrieve resource.
// go-git has no internal locking, so two goroutines or processes syncing the
// same repo+component (daemon overlap, executor re-trigger, parallel CLI runs)
// could corrupt the shared .git object database or storage paths. The lock is
// keyed by (repo, component): different repos and the code/wiki components stay
// fully parallel.
package lock

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/wnarutou/gitrieve/internal/scm"
)

// In-process layer: a binary semaphore per key. This guarantees exclusion
// between goroutines in the same process on every platform — Windows byte-range
// lock semantics for same-process re-locking across handles are unreliable, so
// we cannot rely on the file lock alone for the executor fast-re-trigger case.
var (
	procLocks   = make(map[string]chan struct{})
	procLocksMu sync.Mutex
)

// Acquire takes an exclusive lock for (r, component) and returns a release
// function that must be called exactly once. While waiting, a cancelled ctx
// makes Acquire return ctx.Err() promptly. The lock file lives under
// .gitrieve/locks and is never deleted (unlinking would race between holders).
// The lock is advisory and per-host + per-working-directory: it does not guard
// multi-host writes to shared storage.
func Acquire(ctx context.Context, r *scm.Repository, component string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := path.Join(r.Host, r.Owner, r.Name, component)

	procLocksMu.Lock()
	ch, ok := procLocks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		procLocks[key] = ch
	}
	procLocksMu.Unlock()

	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseProc := func() { <-ch }

	cwd, err := os.Getwd()
	if err != nil {
		releaseProc()
		return nil, err
	}
	lockPath := filepath.Join(cwd, ".gitrieve", "locks", r.Host, r.Owner, r.Name, component+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		releaseProc()
		return nil, err
	}

	f := flock.New(lockPath)
	locked, err := f.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		releaseProc()
		return nil, err
	}
	if !locked {
		releaseProc()
		return nil, fmt.Errorf("lock for %s not acquired", key)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = f.Unlock()
			_ = f.Close()
			releaseProc()
		})
	}, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/lock/`
Expected: all 5 tests PASS, including `TestCrossProcessLock` (spawns a subprocess, verifies it blocks a same-key Acquire in this process, then verifies the lock is freed when the subprocess is killed).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/lock/lock.go internal/lock/lock_test.go
git commit -m "feat: add cross-process per-repo lock package (internal/lock)"
```

---
## Task 2: Integrate the lock into `repository.Sync` (code + wiki)

**Files:**
- Modify: `internal/repository/repository.go` (imports + insert lock after the invalid-name check, after `repository.go:114`)
- Modify: `internal/repository/repository_test.go` (imports + two new tests)

**Interfaces:**
- Consumes: `lock.Acquire(ctx, r, component)` from Task 1; `scm.NewRepository` (already used in the file).
- Produces: `repository.Sync` now holds the lock for component `"code"` (iswiki=false) or `"wiki"` (iswiki=true) for its entire body.

- [ ] **Step 1: Write the failing tests**

Append to `internal/repository/repository_test.go` and update its imports:

```go
import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)
```

```go
func TestSyncBlocksWhileCodeLockHeld(t *testing.T) {
	repo := typedef.Repository{Name: "test-repo", URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "code")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	// If Sync respects the lock it blocks here and hits the ctx timeout instead
	// of reaching the network (github.com/test/repo does not exist). Exact
	// equality (not ErrorIs): without the lock, the network clone's error wraps
	// context.DeadlineExceeded and would satisfy ErrorIs — exact equality
	// distinguishes "blocked at the lock" (bare sentinel) from "failed at the
	// network" (wrapped).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, false, nil)
	require.Equal(t, context.DeadlineExceeded, err, "Sync must block on the held code lock")
}

func TestSyncBlocksWhileWikiLockHeld(t *testing.T) {
	repo := typedef.Repository{Name: "test-repo", URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "wiki")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, true, nil)
	require.Equal(t, context.DeadlineExceeded, err, "wiki Sync must block on the held wiki lock")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/repository/ -run 'TestSyncBlocks'`
Expected: FAIL — without the lock, Sync proceeds to clone `github.com/test/repo` (network); the clone error is a wrapped error (e.g. `*url.Error` wrapping `context.DeadlineExceeded`), so `require.Equal(t, context.DeadlineExceeded, err)` fails. The exact-equality assertion is what makes this red reliable regardless of network timing.

- [ ] **Step 3: Implement — add the import and the lock in `Sync`**

Add to the import block in `internal/repository/repository.go`:

```go
	"github.com/wnarutou/gitrieve/internal/lock"
```

After the invalid-name check (after the `if repoName == "." || repoName == "/"` block) insert:

```go

	// Serialize concurrent syncs of the same repo+component across goroutines
	// and processes (daemon overlap, executor re-trigger, parallel CLI runs).
	// go-git has no internal locking; two writers to the same .git corrupt the
	// object database. Keyed by (repo, component): code and wiki use different
	// keys so they stay parallel. Held across the whole sync so the
	// clone-failure RemoveAll can never delete a directory another sync is
	// actively using.
	component := "code"
	if iswiki {
		component = "wiki"
	}
	unlock, err := lock.Acquire(ctx, r, component)
	if err != nil {
		return err
	}
	defer unlock()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/repository/`
Expected: PASS — both new `TestSyncBlocks*` tests now return `context.DeadlineExceeded`, and the existing `TestSyncCancelledContextFailsPromptlyAndCleansUp` still passes (a pre-cancelled sync now returns at `Acquire` before cloning; the `.git` is never created, so the assertion still holds).

- [ ] **Step 5: Commit**

```bash
git add internal/repository/repository.go internal/repository/repository_test.go
git commit -m "feat: serialize code/wiki syncs with per-repo lock"
```

---
## Task 3: Integrate the lock into `issue.Sync`

**Files:**
- Modify: `internal/issue/issue.go` (imports + insert lock after the invalid-name check, after `issue.go:56`)
- Modify: `internal/issue/issue_test.go` (imports + one new test)

**Interfaces:**
- Consumes: `lock.Acquire(ctx, r, "issue")`.
- Produces: `issue.Sync` holds the lock for component `"issue"` for its entire body.

- [ ] **Step 1: Write the failing test**

Append to `internal/issue/issue_test.go` and update its imports:

```go
import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)
```

```go
func TestSyncBlocksWhileLockHeld(t *testing.T) {
	repo := typedef.Repository{URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "issue")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, nil)
	require.Equal(t, context.DeadlineExceeded, err, "issue Sync must block on the held issue lock")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/issue/ -run TestSyncBlocksWhileLockHeld`
Expected: FAIL — without the lock, Sync proceeds to list issues from the GitHub API (network) instead of timing out.

- [ ] **Step 3: Implement — add the import and the lock in `Sync`**

Add to the import block in `internal/issue/issue.go`:

```go
	"github.com/wnarutou/gitrieve/internal/lock"
```

After the invalid-name check (after the `if repoName == "." || repoName == "/"` block) insert:

```go

	// Serialize concurrent syncs of the same repo's issues (in-process and
	// cross-process): they share the .gitrieve/issues cache dir and the
	// issues.tar.gz storage path.
	unlock, err := lock.Acquire(ctx, r, "issue")
	if err != nil {
		return err
	}
	defer unlock()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/issue/`
Expected: PASS — new block test returns `context.DeadlineExceeded`; existing `TestSyncCancelledContextReturnsImmediately` still passes (returns before the lock on a pre-cancelled ctx).

- [ ] **Step 5: Commit**

```bash
git add internal/issue/issue.go internal/issue/issue_test.go
git commit -m "feat: serialize issue syncs with per-repo lock"
```

---
## Task 4: Integrate the lock into `discussion.Sync`

**Files:**
- Modify: `internal/discussion/discussion.go` (imports + insert lock after the invalid-name check, after `discussion.go:178`)
- Modify: `internal/discussion/discussion_test.go` (imports + one new test)

**Interfaces:**
- Consumes: `lock.Acquire(ctx, r, "discussion")`.
- Produces: `discussion.Sync` holds the lock for component `"discussion"` for its entire body.

- [ ] **Step 1: Write the failing test**

Append to `internal/discussion/discussion_test.go` and update its imports:

```go
import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)
```

```go
func TestSyncBlocksWhileLockHeld(t *testing.T) {
	repo := typedef.Repository{URL: "github.com/test/repo", UseCache: true}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "discussion")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Sync(ctx, repo, nil)
	require.Equal(t, context.DeadlineExceeded, err, "discussion Sync must block on the held discussion lock")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/discussion/ -run TestSyncBlocksWhileLockHeld`
Expected: FAIL — without the lock, Sync proceeds to the GitHub GraphQL API (network).

- [ ] **Step 3: Implement — add the import and the lock in `Sync`**

Add to the import block in `internal/discussion/discussion.go`:

```go
	"github.com/wnarutou/gitrieve/internal/lock"
```

After the invalid-name check (after the `if repoName == "." || repoName == "/"` block) insert:

```go

	// Serialize concurrent syncs of the same repo's discussions: they share
	// the .gitrieve/discussion cache dir and the discussions.tar.gz path.
	unlock, err := lock.Acquire(ctx, r, "discussion")
	if err != nil {
		return err
	}
	defer unlock()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/discussion/`
Expected: PASS — new block test returns `context.DeadlineExceeded`; existing `TestSyncCancelledContextReturnsImmediately` still passes.

- [ ] **Step 5: Commit**

```bash
git add internal/discussion/discussion.go internal/discussion/discussion_test.go
git commit -m "feat: serialize discussion syncs with per-repo lock"
```

---
## Task 5: Integrate the lock into `release.DownloadAllAssets`

**Files:**
- Modify: `internal/release/release.go` (imports + insert lock after `scm.NewRepository`, after `release.go:42`)
- Modify: `internal/release/release_test.go` (imports + one new test)

**Interfaces:**
- Consumes: `lock.Acquire(ctx, r, "release")`.
- Produces: `release.DownloadAllAssets` holds the lock for component `"release"` for its entire body.

- [ ] **Step 1: Write the failing test**

Append to `internal/release/release_test.go` and update its imports:

```go
import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)
```

```go
func TestDownloadAllAssetsBlocksWhileLockHeld(t *testing.T) {
	// DownloadAllAssets reads the release limits from the package-global config
	// before acquiring the lock, so point config at a minimal file (same
	// pattern as the executor tests) to avoid a nil-pointer dereference.
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString("githubtoken: test-token\n")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	config.Path = tmp.Name()
	config.Init()
	t.Cleanup(func() { config.Path = "" })

	repo := typedef.Repository{URL: "github.com/test/repo"}
	r, err := scm.NewRepository(repo.URL)
	require.NoError(t, err)
	release, err := lock.Acquire(context.Background(), r, "release")
	require.NoError(t, err)
	defer release()
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = DownloadAllAssets(ctx, repo, nil)
	require.Equal(t, context.DeadlineExceeded, err, "DownloadAllAssets must block on the held release lock")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/release/ -run TestDownloadAllAssetsBlocksWhileLockHeld`
Expected: FAIL — without the lock, DownloadAllAssets proceeds to the GitHub API (network).

- [ ] **Step 3: Implement — add the import and the lock in `DownloadAllAssets`**

Add to the import block in `internal/release/release.go`:

```go
	"github.com/wnarutou/gitrieve/internal/lock"
```

After the `scm.NewRepository` error check (after `release.go:42`, before `c, err := github.New()`) insert:

```go

	// Serialize concurrent syncs of the same repo's releases: they read-modify-
	// write the same storage <tag>/<asset> paths and delete stale ones.
	unlock, err := lock.Acquire(ctx, r, "release")
	if err != nil {
		return err
	}
	defer unlock()
```

Note: the existing local variable is named `r` (the parsed repository), so the lock release variable must be named `unlock`, not `release`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/release/`
Expected: PASS — new block test returns `context.DeadlineExceeded`; existing `TestDownloadAllAssetsCancelledContextReturnsImmediately` still passes (pre-cancelled ctx returns at the top before config/lock).

- [ ] **Step 5: Commit**

```bash
git add internal/release/release.go internal/release/release_test.go
git commit -m "feat: serialize release downloads with per-repo lock"
```

---
## Task 6: Full verification and docs

**Files:**
- Modify: `CLAUDE.md` (add a short "Concurrency" paragraph under Architecture → Key Patterns)

- [ ] **Step 1: Run the full build, vet, and test suite**

Run:
```bash
go build ./...
go vet ./...
go test ./...
```
Expected: all packages build, vet clean, and every test passes (including the existing executor/server tests).

- [ ] **Step 2: Verify go.mod is tidy**

Run: `go mod tidy`
Expected: no unexpected changes; `github.com/gofrs/flock v0.12.1` remains the only new direct dependency.

- [ ] **Step 3: Document the lock in CLAUDE.md**

In `CLAUDE.md`, under `### Key Patterns`, add:

```markdown
### Concurrency: cross-process per-repo locks
Same repo + same component syncs are serialized across goroutines AND processes
by `internal/lock` — a per-key in-process semaphore plus a `gofrs/flock` advisory
file lock on `.gitrieve/locks/<host>/<owner>/<repo>/<component>.lock`. Covers
code/wiki/issue/discussion/release. The lock is per-host and per-working-directory:
multi-host writes to shared storage (e.g. two machines writing one S3 bucket) are
not guarded. Lock files are never deleted.
```

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: note cross-process per-repo lock in CLAUDE.md"
```

---
## Self-Review Notes

- **Spec coverage:** Spec §4 (Acquire API) → Task 1; §5 integration points → Tasks 2-5; §7 cancellation → Task 1's ctx-aware layers + existing cancel checks; §8 boundaries → Task 6 docs + Global Constraints; §9 tests → Tasks 1-5.
- **Type consistency:** `Acquire` signature `(ctx context.Context, r *scm.Repository, component string) (func(), error)` is used identically in Tasks 2-5. The release lock variable is `unlock` everywhere to avoid shadowing anything.
- **Test isolation:** each component test cleans up `.gitrieve` via `t.Cleanup`; the cross-process test uses a distinct repo name (`xproc-repo`) and always reaps the child.
