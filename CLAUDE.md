# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**gitrieve** is a Go-based tool for archiving repositories from any Git server (GitHub, etc.) to multiple storage backends. It supports scheduling via cron, downloading repository metadata (releases, issues, wiki, discussions), and storing to local filesystem or S3-compatible storage.

## Build & Development

```bash
# Build
go build -o gitrieve main.go

# Run
./gitrieve -c config.yaml
./gitrieve              # uses config.yaml by default

# Commands
./gitrieve run          # archive all repos in config
./gitrieve repository <name>  # archive single repo
./gitrieve release <name>     # download release assets
./gitrieve daemon       # run as daemon with cron schedules

# Release (tagged pushes only)
# Uses GoReleaser; builds multi-arch binaries and Docker image
```

## Architecture

### Directory Structure
- `cmd/` - Cobra CLI commands (root, run, repository, daemon, release, issue, wiki, discussion)
- `internal/` - Core logic organized by domain:
  - `config/` - Viper-based configuration loading
  - `repository/` - Git cloning/fetching logic
  - `scm/` - SCM abstraction (currently GitHub via `githubv4`/`go-github`)
  - `storage/` - Storage backends (file, S3)
  - `typedef/` - Shared types (Repository, Storage, MultiStorage)
  - `ui/` - Terminal output helpers

### Key Patterns
- Configuration initialized via `cobra.OnInitialize(config.Init)` in root.go
- Config parsed into `Config` struct with `Repository[]`, `Storage[]`, and global settings
- Storage backends selected by name reference in repository config
- Daemon mode uses `gocron/v2` for scheduled jobs
- Archives created with `archiver/v4`, uploaded via `minio-go` (S3) or direct file write

### Deletion-safe sync (critical invariant — do not regress)
A core design goal: **once code and history are pulled locally, a sync must never delete them**, even when the upstream repo is taken down, DMCA-disabled, deleted, made private, or replaced with a single README. This makes gitrieve a true archive/backup tool, not a mirror. When modifying `internal/repository/repository.go` (`Sync`), preserve these guarantees:

- **Remote unreachable → early exit.** `gitRepo.Fetch` failure returns early (`repository.go` ~line 159-162); no archiving and no `os.RemoveAll` cleanup runs, so the local cache and prior archived snapshots are untouched. Do NOT move cleanup before fetch or swallow fetch errors to continue.
- **Branches are added, never deleted.** The `refs.ForEach` loop (`repository.go` ~line 199-286) only creates/updates local branches; it never deletes a local branch. Upstream-deleted branches must remain locally.
- **Pull, not reset.** Updates use `w.Pull` (merge), never `git reset --hard origin`. A force-push rewriting the upstream default branch only moves the `origin/*` tracking refs; local branches and their old commit objects must not be overwritten or discarded.
- **Old commits retained.** Commits are immutable objects and the sync never force-moves local refs, so already-pulled history stays in the local `.git` object store and is recoverable via `git checkout <old-hash>`.

Recommended-for-recoverability config: `allBranches: true` (pull every branch's commits) and `useCache: true` (keep the local `.git` cache across syncs; otherwise the working dir is removed at the end of each sync).

Known limitation (not yet a regression to fix unless asked): archiving writes a fixed filename (e.g. `repo.tar.gz`) and overwrites any prior archive at the same path. If the upstream is still reachable but its default branch was rewritten to a single README, the new snapshot replaces the previous normal one at that path. Local cached code/history is still safe, but distinct historical snapshots need object-storage versioning or versioned archive paths.

### Configuration Schema
```yaml
repository:
  - name: <id>
    url: <host/owner/repo>        # OR use orgName + type: user/org
    cron: "<cron expression>"
    storage: [<storage names>]
    useCache: true/false
    allBranches: true/false
    depth: <int, 0=all>
    downloadReleases/Issues/Wiki/Discussion: true/false

storage:
  - name: <id>
    type: file | s3
    path: <local path>            # for file
    endpoint/bucket/region/keys   # for s3

githubToken: <token>
cocurrencyNum: <int>
releaseSizeLimit: <bytes>
releaseNumLimit: <count>
```

## Testing

No test files exist in this codebase as of the last review. When adding tests, follow Go conventions with `_test.go` files alongside the code they test.
