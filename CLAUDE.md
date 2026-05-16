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
