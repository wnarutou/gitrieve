# Web UI Guide

gitrieve ships with a built-in web UI for managing archive jobs and configuration through a browser, instead of the CLI commands. This guide covers how to start the server, access the UI, and configure it.

## Starting the server

Build gitrieve and launch the server command:

```bash
gitrieve build -o gitrieve main.go
# or, if already installed:
gitrieve server
```

You must have a `config.yaml` in place (or pass `-c <path>`), just like the CLI commands. The server reads your repository and storage configuration and lets you manage them at runtime.

```bash
gitrieve -c config.yaml server
```

On startup you will see:

```
Starting server on localhost:8080
```

## Accessing the UI

Open your browser at:

```
http://localhost:8080
```

The default host is `localhost` and the default port is `8080`. Static assets (CSS/JS) are served from `/static/*`.

## Features

### Job management

- **Trigger an archive** — start a job for any repository defined in your config.
- **View job history** — list past and current jobs with status, start/end time, and error messages. Filter by status or repository, and paginate.
- **Cancel a job** — stop a running or pending job.
- **Real-time logs** — every job has a live log stream that updates as the job runs, using Server-Sent Events. The stream closes automatically when the job finishes.

### Configuration management

- **Repositories** — add, edit, and remove repository entries from your `config.yaml` without editing the file by hand. Changes are persisted back to the config file.
- **Storage backends** — add, edit, and remove `file` and `s3` storage backends. Changes are persisted back to the config file.

All write operations update the in-memory config immediately and then attempt to save it to disk. If the save fails (for example, the config file is read-only), the change still takes effect for the running server, and the API response will carry a warning message.

## Configuration

The server is configured via an optional `server` section in `config.yaml`:

```yaml
server:
  host: localhost        # bind address
  port: 8080             # listen port
  authEnabled: false     # require a bearer token for API access
  authToken: ""          # the token clients must send when authEnabled is true
```

| Field | Default | Description |
|---|---|---|
| `host` | `localhost` | Network address to bind |
| `port` | `8080` | Port to listen on |
| `authEnabled` | `false` | When `true`, API requests must include the bearer token |
| `authToken` | `""` | Token value sent as `Authorization: Bearer <token>` |

A complete example combining the server section with the rest of the config:

```yaml
server:
  host: 0.0.0.0
  port: 8080
  authEnabled: true
  authToken: "change-me-in-production"

repository:
  - name: gitrieve
    url: github.com/wnarutou/gitrieve
    cron: "0 * * * *"
    storage:
      - localFile
    useCache: true
    allBranches: true
    depth: 0
    downloadReleases: true
    downloadIssues: true
    downloadWiki: true
    downloadDiscussion: true

storage:
  - name: localFile
    type: file
    path: ./repo

githubToken: your-github-token
```

## Security notes

- **Auth is off by default.** With `authEnabled: false`, anyone who can reach the server can trigger archive jobs, view logs, and modify your repository/storage configuration. Only run with auth disabled on a trusted network.
- **Use a strong `authToken`.** When `authEnabled: true`, set a long, random token. Clients send it as `Authorization: Bearer <token>` on every API request. See the [API reference](api.md#authentication) for details.
- **Bind carefully.** The default `host: localhost` only accepts connections from the same machine. To expose the server to other machines, set `host: 0.0.0.0` (or a specific interface) and put it behind a reverse proxy with TLS.
- **No TLS built in.** The server listens over plain HTTP. For remote access, terminate TLS at a reverse proxy (nginx, Caddy, etc.) in front of gitrieve.
- **Config file writes.** The server writes back to `config.yaml` when you edit repositories or storage through the UI/API. Ensure the file is writable by the user running gitrieve, and protect the file because it may contain storage credentials and tokens.

## Next steps

- [API reference](api.md) — for scripting or integrating gitrieve into automation.
- [README](../README.md) — for the CLI commands (`run`, `repository`, `release`, `daemon`) and the full configuration schema.
