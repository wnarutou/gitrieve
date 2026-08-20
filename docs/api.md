# API Reference

gitrieve provides an HTTP API for managing archive jobs, repositories, and storage backends through its web server. This document describes every endpoint exposed by `gitrieve server`.

## Overview

| | |
|---|---|
| **Base URL** | `http://localhost:8080` (default; see [Web UI](web-ui.md) for how to change host/port) |
| **Content type** | `application/json` for request and response bodies |
| **Response envelope** | All JSON endpoints return a `Response` object (see below) |

Every JSON endpoint returns the same envelope:

```json
{
  "code": 200,
  "data": { /* endpoint-specific payload, or null */ },
  "message": "" /* non-empty on partial failure, e.g. config not persisted */
}
```

The HTTP status code matches `code` on the happy path (e.g. `200`, `404`, `500`). `message` carries human-readable detail on error or when a write succeeded in memory but could not be persisted to the config file.

## Authentication

The server supports an optional bearer-token mode, controlled by the `server` section of `config.yaml`:

```yaml
server:
  host: localhost
  port: 8080
  authEnabled: true
  authToken: "your-secret-token"
```

When `server.authEnabled` is `true`, API clients must send the token in the `Authorization` header:

```
Authorization: Bearer your-secret-token
```

Requests missing or mismatching the token are rejected. When `authEnabled` is `false` (the default), no authentication is required. This applies to API endpoints under `/api/*`; the web UI at `/` and static assets under `/static/*` are served regardless.

> Note: token-based auth is defined in the server config schema. Verify your build wires up the auth middleware before relying on it in production.

## Jobs

### Create a job

Trigger an archive job for a repository defined in configuration.

```
POST /api/jobs
```

**Request body**

```json
{
  "repository_key": "github.com/wnarutou/gitrieve"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `repository_key` | string | yes | The repository identity key — the normalized URL (e.g. `github.com/wnarutou/gitrieve`) of a repository defined in `config.yaml`. For `user`/`org` entries, use the synthesized `https://github.com/<orgName>` (server normalizes it). A bare display name is not accepted. |

**Response `data`**

```json
{
  "job_ids": ["1719800000", "1719800001"],
  "status": "running"
}
```

| Field | Type | Description |
|---|---|---|
| `job_ids` | array of string | One job identifier per expanded repository; pass each to the logs/cancel endpoints. A `repo`-type entry yields a single ID; a `user`/`org` entry yields one ID per concrete member repository |
| `status` | string | Initial status, one of `pending` \| `running` \| `completed` \| `failed` \| `cancelled` |

**Example**

```bash
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"repository_key": "github.com/wnarutou/gitrieve"}'
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Job started |
| 400 | Invalid request body |
| 404 | Repository not found in configuration |
| 500 | Failed to start the job |

---

### Cancel a job

Cancel a running or pending job by its ID.

```
DELETE /api/jobs/:id
```

**Path parameters**

| Name | Description |
|---|---|
| `id` | A job identifier from the `job_ids` array returned by `POST /api/jobs` |

**Response `data`**

```json
{
  "status": "cancelled"
}
```

**Example**

```bash
curl -X DELETE http://localhost:8080/api/jobs/1719800000
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Job cancelled |
| 400 | Job ID is required |
| 500 | Failed to cancel the job |

---

### List jobs

List archive jobs with pagination and optional filters.

```
GET /api/jobs
```

**Query parameters**

| Param | Type | Default | Description |
|---|---|---|---|
| `page` | int | `1` | Page number (1-based) |
| `limit` | int | `20` | Items per page; clamped to `1`–`100` |
| `status` | string | — | Filter by status; `all` (or omitted) returns all statuses |
| `repository` | string | — | Fuzzy (partial) match on repository **name or URL**, case-insensitive for ASCII. A URL term is normalized server-side (e.g. `https://github.com/wnarutou/gitrieve.git` matches the stored key `github.com/wnarutou/gitrieve`) (SQL `LIKE '%value%'`; `%`, `_`, and `\` in the value are matched literally) |

**Response `data`**

```json
{
  "jobs": [
    {
      "id": "1719800000",
      "name": "gitrieve",
      "url": "github.com/wnarutou/gitrieve",
      "status": "completed",
      "start_time": "2026-08-05T10:00:00Z",
      "end_time": "2026-08-05T10:02:30Z",
      "error_message": ""
    }
  ],
  "total": 42,
  "page": 1,
  "limit": 20
}
```

| Field | Type | Description |
|---|---|---|
| `jobs[].id` | string | Job ID |
| `jobs[].name` | string | Repository name |
| `jobs[].url` | string | Repository URL (from config) |
| `jobs[].status` | string | `pending` \| `running` \| `completed` \| `failed` \| `cancelled` |
| `jobs[].start_time` | string \| null | RFC3339 timestamp |
| `jobs[].end_time` | string \| null | RFC3339 timestamp, `null` if still running |
| `jobs[].error_message` | string | Error detail, empty on success |
| `total` | int | Total matching jobs (before pagination) |
| `page` | int | Current page |
| `limit` | int | Items per page |

Results are ordered by `start_time` descending.

**Examples**

```bash
# First page, default limit
curl http://localhost:8080/api/jobs

# Failed jobs only, page 2
curl "http://localhost:8080/api/jobs?status=failed&page=2&limit=50"

# Jobs whose repository name or URL contains "gitrieve" (fuzzy match)
curl "http://localhost:8080/api/jobs?repository=gitrieve"

# Match by URL (term is normalized server-side)
curl "http://localhost:8080/api/jobs?repository=github.com/wnarutou/gitrieve"
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Jobs returned |
| 500 | Failed to query jobs |

---

### Stream job logs (SSE)

Stream the logs of a job in real time using Server-Sent Events.

```
GET /api/jobs/:id/logs
```

**Path parameters**

| Name | Description |
|---|---|
| `id` | The job ID |

**Response**

- `Content-Type: text/event-stream`
- `Cache-Control: no-cache`
- `Connection: keep-alive`

Each log line is sent as a `data:` event terminated by `\n\n`:

```
data: {"id":1,"execution_id":"1719800000","timestamp":"2026-08-05T10:00:01Z","level":"info","message":"Fetching repository"}

data: {"id":2,"execution_id":"1719800000","timestamp":"2026-08-05T10:00:02Z","level":"info","message":"Archiving releases"}
```

A heartbeat comment (`: heartbeat\n\n`) is sent every 15 seconds to keep the connection alive. The stream closes automatically once the job reaches a terminal status (`completed`, `failed`, or `cancelled`), after flushing any remaining logs. The stream also stops if the client disconnects.

**Event payload**

| Field | Type | Description |
|---|---|---|
| `id` | int | Log entry ID (monotonic) |
| `execution_id` | string | Job ID this log belongs to |
| `timestamp` | string | RFC3339 timestamp |
| `level` | string | Log level, e.g. `info`, `error` |
| `message` | string | Log text |

**Example**

```bash
curl -N http://localhost:8080/api/jobs/1719800000/logs
```

`-N` disables curl's output buffering so events are printed as they arrive. In a browser, use the `EventSource` API:

```javascript
const es = new EventSource("/api/jobs/1719800000/logs");
es.onmessage = (e) => {
  const entry = JSON.parse(e.data);
  console.log(entry.timestamp, entry.level, entry.message);
};
es.onerror = () => es.close(); // stream ended by server or network
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Stream opened (then `text/event-stream`) |
| 400 | Job ID is required |
| 404 | Job not found |

## Repositories

Repositories are read from and written back to `config.yaml`. The `:id` path parameter is the repository **identity key** — the normalized URL (e.g. `github.com/wnarutou/gitrieve`) — not the name. Because the identity key contains slashes, the route is registered as a gin catch-all. Names are display labels and may repeat; the normalized URL is the unique key in the config.

### List repositories

List repositories from `config.yaml` (see [Configuration](../README.md#configuration)) with per-repository execution stats, pagination, and an optional fuzzy name-or-URL filter.

```
GET /api/repositories
```

**Query parameters**

| Param | Type | Default | Description |
|---|---|---|---|
| `page` | int | `1` | Page number (1-based) |
| `limit` | int | `20` | Items per page; clamped to `1`–`100` |
| `search` | string | — | Fuzzy (partial) match on repository **name or URL**, case-insensitive for ASCII |

**Response `data`** — a paginated object (not a bare array). Each item embeds the repository fields (PascalCase, matching the `repository:` config entries) plus lower-cased execution stats.

```json
{
  "repositories": [
    {
      "Name": "gitrieve",
      "URL": "github.com/wnarutou/gitrieve",
      "Cron": "0 * * * *",
      "Storage": ["localFile"],
      "UseCache": true,
      "AllBranches": false,
      "Type": "repo",
      "OrgName": "",
      "Depth": 0,
      "DownloadReleases": true,
      "DownloadIssues": false,
      "DownloadWiki": false,
      "DownloadDiscussion": false,
      "last_run_time": "2026-08-05T10:02:30Z",
      "next_run_time": "2026-08-05T11:00:00Z",
      "total_runs": 42,
      "success_runs": 40,
      "failed_runs": 2
    }
  ],
  "total": 42,
  "page": 1,
  "limit": 20
}
```

| Field | Type | Description |
|---|---|---|
| `repositories[]` | object | A repository from config plus its per-run stats |
| `repositories[].Name` | string | Repository display name (identity is the URL) |
| `repositories[].URL` | string | Repository URL |
| `repositories[].Cron` | string | Cron schedule, empty if none |
| `repositories[].Storage` | array of string | Storage backend names |
| `repositories[].Type` | string | `repo` \| `user` \| `org` |
| `repositories[].OrgName` | string | Organization name for `user`/`org` types |
| `repositories[].last_run_time` | string \| null | RFC3339 timestamp of the most recent execution |
| `repositories[].next_run_time` | string \| null | RFC3339 timestamp of the next scheduled run (computed from `Cron`), `null` if no valid cron |
| `repositories[].total_runs` | int | Total executions for the repository |
| `repositories[].success_runs` | int | Executions that finished `completed` |
| `repositories[].failed_runs` | int | Executions that finished `failed` |
| `total` | int | Total matching repositories (before pagination) |
| `page` | int | Current page |
| `limit` | int | Items per page |

Other embedded repository fields (`UseCache`, `AllBranches`, `Depth`, `DownloadReleases`, `DownloadIssues`, `DownloadWiki`, `DownloadDiscussion`) are the boolean/int options from the config entry.

**Examples**

```bash
# All repositories, first page with the default limit
curl http://localhost:8080/api/repositories

# Fuzzy name or URL search
curl "http://localhost:8080/api/repositories?search=gitr"

# URL terms match too
curl "http://localhost:8080/api/repositories?search=github.com/wnarutou"

# Page 2 with a custom limit
curl "http://localhost:8080/api/repositories?page=2&limit=50"
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Repositories returned |
| 500 | Failed to query execution stats |

---

### Create a repository

```
POST /api/repositories
```

**Request body** — a repository object. `name` is required.

```json
{
  "name": "gitrieve",
  "url": "github.com/wnarutou/gitrieve",
  "cron": "0 * * * *",
  "storage": ["localFile"],
  "useCache": true,
  "allBranches": true,
  "depth": 0,
  "downloadReleases": true,
  "downloadIssues": true,
  "downloadWiki": true,
  "downloadDiscussion": true
}
```

Identity is the URL. A `repo`-type entry must supply a `url`; a `user`/`org` entry may omit `url` and set `orgName` instead — an empty URL is auto-synthesized to `https://github.com/<orgName>`. An entry with no identity (empty URL and no `orgName`) is rejected with `400`. Names may repeat: duplicate detection compares the normalized URL, and a conflicting URL returns `409`.

**Response `data`** — the created repository object.

**Example**

```bash
# repo-type entry with a URL
curl -X POST http://localhost:8080/api/repositories \
  -H "Content-Type: application/json" \
  -d '{"name":"gitrieve","url":"github.com/wnarutou/gitrieve","storage":["localFile"],"useCache":true}'

# org entry with no URL — the server synthesizes https://github.com/acme
curl -X POST http://localhost:8080/api/repositories \
  -H "Content-Type: application/json" \
  -d '{"name":"acme","type":"org","orgName":"acme"}'
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Repository added |
| 400 | Invalid body, missing `name`, or no identity (empty URL for `repo` type; empty `orgName` for `user`/`org` type) |
| 409 | A repository with that URL already exists (identity is the URL; names may repeat) |

---

### Update a repository

Partial update via JSON merge — only supplied fields are changed, unspecified fields keep their current value.

```
PUT /api/repositories/:id
```

**Path parameters**

| Name | Description |
|---|---|
| `id` | Repository identity key — the normalized URL (contains slashes; the route is a gin catch-all) |

**Request body** — any subset of repository fields.

```json
{
  "cron": "0 */6 * * *",
  "allBranches": false
}
```

**Response `data`** — the merged, updated repository object.

**Example**

```bash
curl -X PUT http://localhost:8080/api/repositories/github.com/wnarutou/gitrieve \
  -H "Content-Type: application/json" \
  -d '{"cron":"0 */6 * * *"}'
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Repository updated |
| 400 | Invalid body |
| 404 | Repository not found |
| 409 | The updated URL conflicts with another repository (identity is the URL) |
| 500 | Failed to merge fields |

---

### Delete a repository

```
DELETE /api/repositories/:id
```

**Path parameters**

| Name | Description |
|---|---|
| `id` | Repository identity key — the normalized URL (contains slashes; the route is a gin catch-all) |

**Response `data`**

```json
{
  "success": true
}
```

**Example**

```bash
curl -X DELETE http://localhost:8080/api/repositories/github.com/wnarutou/gitrieve
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Repository deleted |
| 404 | Repository not found |

## Storage

Storage backends are also read from and written back to `config.yaml`. The `:id` path parameter is the storage **name**.

### List storage backends

```
GET /api/storage
```

**Response `data`** — an array of storage objects matching the `storage:` entries in `config.yaml`.

**Example**

```bash
curl http://localhost:8080/api/storage
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Storage backends returned |

---

### Create a storage backend

```
POST /api/storage
```

**Request body** — a storage object. `name` is required and `type` must be `file` or `s3`.

For a file backend:

```json
{
  "name": "localFile",
  "type": "file",
  "path": "./repo"
}
```

For an S3 backend:

```json
{
  "name": "backblaze",
  "type": "s3",
  "endpoint": "s3.us-west-000.backblazeb2.com",
  "region": "us-west-000",
  "bucket": "your-bucket-name",
  "accessKeyID": "your-access-key-id",
  "secretAccessKey": "your-secret-access-key"
}
```

**Response `data`** — the created storage object.

**Example**

```bash
curl -X POST http://localhost:8080/api/storage \
  -H "Content-Type: application/json" \
  -d '{"name":"localFile","type":"file","path":"./repo"}'
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Storage added |
| 400 | Invalid body, missing `name`, or invalid `type` |
| 409 | A storage backend with that name already exists |

---

### Update a storage backend

Partial update via JSON merge.

```
PUT /api/storage/:id
```

**Path parameters**

| Name | Description |
|---|---|
| `id` | Storage `name` |

**Request body** — any subset of storage fields.

```json
{
  "path": "/data/repos"
}
```

**Response `data`** — the merged, updated storage object.

**Example**

```bash
curl -X PUT http://localhost:8080/api/storage/localFile \
  -H "Content-Type: application/json" \
  -d '{"path":"/data/repos"}'
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Storage updated |
| 400 | Invalid body |
| 404 | Storage not found |
| 500 | Failed to merge fields |

---

### Delete a storage backend

```
DELETE /api/storage/:id
```

**Path parameters**

| Name | Description |
|---|---|
| `id` | Storage `name` |

**Response `data`**

```json
{
  "success": true
}
```

**Example**

```bash
curl -X DELETE http://localhost:8080/api/storage/localFile
```

**Status codes**

| Code | Meaning |
|---|---|
| 200 | Storage deleted |
| 404 | Storage not found |

## System

### Health check

```
GET /health
```

Returns whether the server is up. *(Note: the `/health` route is part of the server's planned system endpoints; confirm it is registered in your build before relying on it.)*

**Example**

```bash
curl http://localhost:8080/health
```

---

### Metrics

```
GET /api/metrics
```

Returns Go runtime metrics for the server process. *(Note: the `/api/metrics` route is part of the server's planned system endpoints; confirm it is registered in your build before relying on it.)*

**Example**

```bash
curl http://localhost:8080/api/metrics
```

---

### Web UI and static assets

| Method | Path | Description |
|---|---|---|
| GET | `/` | Renders the web UI (`index.html`) |
| GET | `/static/*` | Static assets (CSS, JS) for the web UI |

These are not JSON APIs — the web UI is the intended way to interact with the endpoints above. See [Web UI guide](web-ui.md).
