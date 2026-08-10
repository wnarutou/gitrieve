# Download Component Cancellation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the four download components (release / issue / wiki / discussion) honor job cancellation with graceful semantics — finish the current unit, then stop and exit the task — and give the four CLI commands Ctrl-C support.

**Architecture:** Thread `context.Context` as the first parameter through `release.DownloadAllAssets`, `issue.Sync`, `wiki.Sync`, `discussion.Sync`. The cancelling ctx is used **only** for boundary checks (`ctx.Err()`) and the wiki's nested git sync; underlying HTTP requests keep `context.Background()` so an in-flight page/asset is never aborted mid-stream (graceful). Each component returns `ctx.Err()` on cancel, skipping archiving/uploading and any storage deletion.

**Tech Stack:** Go, go-github (`github.com/google/go-github/v56`), githubv4 (`github.com/shurcooL/githubv4`) + oauth2, gocron/v2, cobra, testify. `signal.NotifyContext` for CLI Ctrl-C.

## Global Constraints

- All four functions gain `ctx context.Context` as their **first** parameter; no signature other change.
- **Graceful semantics:** an in-flight HTTP request and the current unit of work (one issue file, one discussion file, one release asset) run to completion. Check `ctx.Err()` only at unit boundaries. **Never** pass the cancelling ctx into go-github / githubv4 / oauth2 requests — keep `context.Background()` there.
- On cancel, **skip** the archive+upload step; in release, **skip** the trailing storage delete loop (never delete on cancel). Return `ctx.Err()`.
- Every component function begins with `if ctx.Err() != nil { return ctx.Err() }` as the **very first** statement (this makes the pre-cancelled-ctx tests run without network or config).
- `useCache=false`: clean up the temp `gitDir` on cancel via `defer` (same `os.RemoveAll` as normal completion). `useCache=true`: keep the cache in place.
- CLI commands use `signal.NotifyContext(context.Background(), os.Interrupt)`; on cancel print a `Cancelled` message (not an error) and break out of the repo loop.
- daemon gocron tasks pass `context.Background()` as the new first arg (scheduled jobs have no cancel entry point).
- Executor keeps the `ui.Printf("Downloading %s", name)` line — the existing test `TestExecuteJobRunsConfiguredComponents` depends on it.
- Commit messages end with `Co-Authored-By: Claude <noreply@anthropic.com>`.

---

### Task 1: `issue.Sync` graceful cancellation

**Files:**
- Modify: `internal/issue/issue.go`
- Create: `internal/issue/issue_test.go`
- Modify: `cmd/issue/issue.go`
- Modify: `cmd/daemon/daemon.go` (line ~69)
- Modify: `internal/executor/executor.go` (line ~189 + the `run` closure at ~179-187)

**Interfaces:**
- Consumes: `typedef.Repository`, `typedef.MultiStorage` (unchanged).
- Produces: `func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error` — returns `ctx.Err()` when cancelled. Callers in Task 2+ rely on this signature shape.

- [ ] **Step 1: Write the failing test**

Create `internal/issue/issue_test.go`:

```go
package issue

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 2: Run test to verify it fails (compile error)**

Run: `go test ./internal/issue/...`
Expected: FAIL to compile — `not enough arguments in call to Sync` (signature has no ctx yet).

- [ ] **Step 3: Add `ctx` param, early check, and cancellation boundary in `internal/issue/issue.go`**

3a. Change the signature (line 22):

```go
func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
```

`context` is already imported in this file.

3b. Make the early check the first statement of the function body:

```go
func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	isUpdated := false
```

3c. Convert the trailing cleanup into a `defer` right after `gitDir` is created, so it also runs on the cancelled return. Find this block (after `err = storage.CreateDirIfNotExist(gitDir)`):

```go
	gitDir := path.Join(workingDir, r.Host, r.Owner, repoName, "issues")
	err = storage.CreateDirIfNotExist(gitDir)
	if err != nil {
		ui.Errorf("Error creating working directory, %s", err)
		return err
	}
```

and append after it:

```go
	if !useCache {
		defer func() {
			if err := os.RemoveAll(gitDir); err != nil {
				ui.Errorf("Error cleaning up working directory, %s", err)
				return
			}
			ui.Printf("Cleanup completed for directory: %s", gitDir)
		}()
	}
```

3d. Add the per-issue boundary check. Inside the `for _, issue := range issues` loop, directly after this line:

```go
			ui.Printf("Success writing issue #%d to file %s", issue.GetNumber(), issueFilePath)
```

add:

```go
			if ctx.Err() != nil {
				return ctx.Err()
			}
```

3e. Remove the now-redundant trailing cleanup block at the end of the function (it is replaced by the defer). Replace:

```go
	// Cleanup
	if !useCache {
		err = os.RemoveAll(gitDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory, %s", err)
			return err
		}
		ui.Printf("Cleanup completed for directory: %s", gitDir)
	}
	return nil
```

with just:

```go
	return nil
```

- [ ] **Step 4: Update all `issue.Sync` callers**

4a. `cmd/issue/issue.go` — add imports `"context"`, `"os"`, `"os/signal"` to the import block. In `runIssue`, after the storage-setup block and before the `for _, repo := range repository.GetRepositories(repoName)` loop, add:

```go
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
```

Replace the loop body:

```go
	for _, repo := range repository.GetRepositories(repoName) {
		if ctx.Err() != nil {
			ui.Printf("Cancelled")
			break
		}
		ui.Printf("Running %s", repo.Name)
		if err := issue.Sync(ctx, repo, storages); err != nil {
			if ctx.Err() != nil {
				ui.Printf("Download cancelled")
				break
			}
			ui.Errorf("Error running %s, %s", repo.Name, err)
			// move on to next repo
		}
	}
```

4b. `cmd/daemon/daemon.go` (line ~69):

```go
			gocron.NewTask(issue.Sync, repo, storages),
```
becomes
```go
			gocron.NewTask(issue.Sync, context.Background(), repo, storages),
```
(`context` is already imported in daemon.go.)

4c. `internal/executor/executor.go` — change the `issue` line in `downloadComponents` (line ~189):

```go
	run("issues", job.DownloadIssues, func() error { return issue.Sync(job, storages) })
```
becomes
```go
	run("issues", job.DownloadIssues, func() error { return issue.Sync(ctx, job, storages) })
```

Also refine the `run` closure (lines ~179-187) so a cancelled download is logged distinctly from a real failure:

```go
	run := func(name string, enabled bool, fn func() error) {
		if !enabled || ctx.Err() != nil {
			return
		}
		ui.Printf("Downloading %s", name)
		if err := fn(); err != nil {
			if ctx.Err() != nil {
				ui.Printf("%s download cancelled", name)
			} else {
				ui.Errorf("Failed to download %s: %v", name, err)
			}
		}
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./...`
Expected: builds cleanly (all call sites updated).

Run: `go test ./internal/issue/...`
Expected: PASS (the new pre-cancelled-ctx test returns `context.Canceled` without network).

Run: `go test ./internal/executor/... -run TestExecuteJobWritesBoundLogs`
Expected: PASS — confirms the `Downloading`/`run` closure changes didn't break executor logging. (Skip `TestExecuteJobRunsConfiguredComponents` here; it hits the real network and is slow.)

- [ ] **Step 6: Commit**

```bash
git add internal/issue/issue.go internal/issue/issue_test.go cmd/issue/issue.go cmd/daemon/daemon.go internal/executor/executor.go
git commit -m "feat: support graceful cancellation in issue download

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: `discussion.Sync` graceful cancellation

**Files:**
- Modify: `internal/discussion/discussion.go`
- Create: `internal/discussion/discussion_test.go`
- Modify: `cmd/discussion/discussion.go`
- Modify: `cmd/daemon/daemon.go` (line ~87)
- Modify: `internal/executor/executor.go` (line ~191)

**Interfaces:**
- Consumes: `func Sync(ctx context.Context, repo, storages) error` shape from Task 1.
- Produces: `func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/discussion/discussion_test.go`:

```go
package discussion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 2: Run test to verify it fails (compile error)**

Run: `go test ./internal/discussion/...`
Expected: FAIL to compile — `not enough arguments in call to Sync`.

- [ ] **Step 3: Add `ctx` param, early check, and cancellation boundary in `internal/discussion/discussion.go`**

3a. Change the signature (line 144):

```go
func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
```

3b. Make the early check the first statement:

```go
func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	isUpdated := false
```

3c. Convert trailing cleanup into a `defer` right after `gitDir` is created. Find:

```go
	gitDir := path.Join(workingDir, r.Host, r.Owner, repoName, "discussion")
	err = storage.CreateDirIfNotExist(gitDir)
	if err != nil {
		ui.Errorf("Error creating working directory, %s", err)
		return err
	}
```

append after it:

```go
	if !useCache {
		defer func() {
			if err := os.RemoveAll(gitDir); err != nil {
				ui.Errorf("Error cleaning up working directory: %s", err)
				return
			}
			ui.Printf("Cleanup completed for directory: %s", gitDir)
		}()
	}
```

3d. Add the per-discussion boundary check. Inside the `for _, discussion := range query.Repository.Discussions.Nodes` loop, directly after:

```go
			ui.Printf("Success writing discussion %s to file %s", discussion.Title, discussionFilePath)
```

add:

```go
			if ctx.Err() != nil {
				return ctx.Err()
			}
```

3e. Remove the trailing cleanup block. Replace:

```go
	if !useCache {
		err = os.RemoveAll(gitDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory: %s", err)
			return err
		}
		ui.Printf("Cleanup completed for directory: %s", gitDir)
	}
	return nil
```

with just:

```go
	return nil
```

- [ ] **Step 4: Update all `discussion.Sync` callers**

4a. `cmd/discussion/discussion.go` — add imports `"context"`, `"os"`, `"os/signal"`. In `runDiscussion`, after the storage-setup block and before the repo loop, add:

```go
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
```

Replace the loop body:

```go
	for _, repo := range repository.GetRepositories(repoName) {
		if ctx.Err() != nil {
			ui.Printf("Cancelled")
			break
		}
		ui.Printf("Running %s", repo.Name)
		if err := discussion.Sync(ctx, repo, storages); err != nil {
			if ctx.Err() != nil {
				ui.Printf("Download cancelled")
				break
			}
			ui.Errorf("Error running %s, %s", repo.Name, err)
			// move on to next repo
		}
	}
```

4b. `cmd/daemon/daemon.go` (line ~87):

```go
			gocron.NewTask(discussion.Sync, repo, storages),
```
becomes
```go
			gocron.NewTask(discussion.Sync, context.Background(), repo, storages),
```

4c. `internal/executor/executor.go` (line ~191):

```go
	run("discussion", job.DownloadDiscussion, func() error { return discussion.Sync(job, storages) })
```
becomes
```go
	run("discussion", job.DownloadDiscussion, func() error { return discussion.Sync(ctx, job, storages) })
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./...`
Expected: builds cleanly.

Run: `go test ./internal/discussion/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/discussion/discussion.go internal/discussion/discussion_test.go cmd/discussion/discussion.go cmd/daemon/daemon.go internal/executor/executor.go
git commit -m "feat: support graceful cancellation in discussion download

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: `release.DownloadAllAssets` graceful cancellation

**Files:**
- Modify: `internal/release/release.go`
- Create: `internal/release/release_test.go`
- Modify: `cmd/release/release.go`
- Modify: `cmd/daemon/daemon.go` (line ~60)
- Modify: `internal/executor/executor.go` (line ~188)

**Interfaces:**
- Consumes: `typedef.Repository`, `typedef.MultiStorage`.
- Produces: `func DownloadAllAssets(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/release/release_test.go`:

```go
package release

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestDownloadAllAssetsCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := DownloadAllAssets(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 2: Run test to verify it fails (compile error)**

Run: `go test ./internal/release/...`
Expected: FAIL to compile — `not enough arguments in call to DownloadAllAssets`.

- [ ] **Step 3: Add `ctx` param and cancellation boundaries in `internal/release/release.go`**

3a. Add `"context"` to the import block.

3b. Change the signature (line 32):

```go
func DownloadAllAssets(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
```

3c. Make the early check the first statement:

```go
func DownloadAllAssets(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	releaseNumLimit := config.GetReleaseNumLimit()
```

3d. After the release list is truncated (directly after `releases = releases[:releaseNumLimit]`), add:

```go
	if ctx.Err() != nil {
		return ctx.Err()
	}
```

3e. Add the per-asset boundary check. The asset loop is:

```go
		for _, asset := range assets {
			...
			for _, s := range needDownloadStorage {
				...
			}
		}
```

At the end of the `for _, asset := range assets` body (after the inner `for _, s := range needDownloadStorage` PutObject loop), add:

```go
			if ctx.Err() != nil {
				return ctx.Err()
			}
```

3f. Add a check before the trailing storage delete loop (the `for _, s := range storages {` block that calls `backend.DeleteObject`), immediately before that loop:

```go
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, s := range storages {
		...
	}
	return nil
```

- [ ] **Step 4: Update all `DownloadAllAssets` callers**

4a. `cmd/release/release.go` — add imports `"context"`, `"os"`, `"os/signal"`. In `runRelease`, after the storage-setup block and before the repo loop, add:

```go
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
```

Replace the loop body:

```go
	for _, repo := range repository.GetRepositories(repoName) {
		if ctx.Err() != nil {
			ui.Printf("Cancelled")
			break
		}
		ui.Printf("Running %s", repo.Name)
		if err := release.DownloadAllAssets(ctx, repo, storages); err != nil {
			if ctx.Err() != nil {
				ui.Printf("Download cancelled")
				break
			}
			ui.Errorf("Error running %s, %s", repo.Name, err)
			// move on to next repo
		}
	}
```

4b. `cmd/daemon/daemon.go` (line ~60):

```go
			gocron.NewTask(release.DownloadAllAssets, repo, storages),
```
becomes
```go
			gocron.NewTask(release.DownloadAllAssets, context.Background(), repo, storages),
```

4c. `internal/executor/executor.go` (line ~188):

```go
	run("releases", job.DownloadReleases, func() error { return release.DownloadAllAssets(job, storages) })
```
becomes
```go
	run("releases", job.DownloadReleases, func() error { return release.DownloadAllAssets(ctx, job, storages) })
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./...`
Expected: builds cleanly.

Run: `go test ./internal/release/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/release/release.go internal/release/release_test.go cmd/release/release.go cmd/daemon/daemon.go internal/executor/executor.go
git commit -m "feat: support graceful cancellation in release download

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: `wiki.Sync` graceful cancellation

**Files:**
- Modify: `internal/wiki/wiki.go`
- Create: `internal/wiki/wiki_test.go`
- Modify: `cmd/wiki/wiki.go`
- Modify: `cmd/daemon/daemon.go` (line ~78)
- Modify: `internal/executor/executor.go` (line ~190)

**Interfaces:**
- Consumes: `repository.Sync(ctx context.Context, repo typedef.Repository, iswiki bool, storages []typedef.MultiStorage) error` (existing — already ctx-aware).
- Produces: `func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/wiki/wiki_test.go`:

```go
package wiki

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestSyncCancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Sync(ctx, typedef.Repository{URL: "github.com/test/repo"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 2: Run test to verify it fails (compile error)**

Run: `go test ./internal/wiki/...`
Expected: FAIL to compile — `not enough arguments in call to Sync`.

- [ ] **Step 3: Add `ctx` param and thread it to the git sync in `internal/wiki/wiki.go`**

3a. Change the signature (line 14):

```go
func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
```

3b. Make the early check the first statement:

```go
func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// get the repo name from the URL
	r, err := scm.NewRepository(repo.URL)
```

3c. Change the nested git sync (line 40) to pass the ctx through:

```go
	if err := repository.Sync(context.Background(), repo, true, storages); err != nil {
```
becomes
```go
	if err := repository.Sync(ctx, repo, true, storages); err != nil {
```

Leave `client.Repositories.Get(context.Background(), ...)` unchanged (single fast metadata request).

- [ ] **Step 4: Update all `wiki.Sync` callers**

4a. `cmd/wiki/wiki.go` — add imports `"context"`, `"os"`, `"os/signal"`. In `runWiki`, after the storage-setup block and before the repo loop, add:

```go
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
```

Replace the loop body:

```go
	for _, repo := range repository.GetRepositories(repoName) {
		if ctx.Err() != nil {
			ui.Printf("Cancelled")
			break
		}
		ui.Printf("Running %s", repo.Name)
		if err := wiki.Sync(ctx, repo, storages); err != nil {
			if ctx.Err() != nil {
				ui.Printf("Download cancelled")
				break
			}
			ui.Errorf("Error running %s, %s", repo.Name, err)
			// move on to next repo
		}
	}
```

4b. `cmd/daemon/daemon.go` (line ~78):

```go
			gocron.NewTask(wiki.Sync, repo, storages),
```
becomes
```go
			gocron.NewTask(wiki.Sync, context.Background(), repo, storages),
```

4c. `internal/executor/executor.go` (line ~190):

```go
	run("wiki", job.DownloadWiki, func() error { return wiki.Sync(job, storages) })
```
becomes
```go
	run("wiki", job.DownloadWiki, func() error { return wiki.Sync(ctx, job, storages) })
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./...`
Expected: builds cleanly.

Run: `go test ./internal/wiki/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/wiki/wiki.go internal/wiki/wiki_test.go cmd/wiki/wiki.go cmd/daemon/daemon.go internal/executor/executor.go
git commit -m "feat: support graceful cancellation in wiki download

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: Full build and test-suite verification

**Files:** (none modified — pure verification)

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: clean build, no errors.

Run: `go vet ./...`
Expected: no vet findings.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./... 2>&1`
Expected: all tests pass. Note: `TestExecuteJobRunsConfiguredComponents` performs a real (failing) clone of a nonexistent repo and a real GitHub API call, so it can be slow or flaky on a constrained network — if it fails, re-run it once before investigating. The pre-cancelled-ctx tests added in Tasks 1–4 must pass without any network.

- [ ] **Step 3: Manual smoke check of Ctrl-C (optional, on a real repo)**

Run: `./gitrieve issue <repo-with-many-issues>` then press Ctrl-C mid-download.
Expected: current issue finishes, a `Download cancelled` / `Cancelled` message prints, and the command exits promptly without a `Failed ... context canceled` error line.

- [ ] **Step 4: Report results**

No commit in this task unless a fix is needed; any fix found here should be folded into the task that owns the affected code.
