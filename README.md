# gitrieve

English | [简体中文](README_zh.md)

Git Retrieve(gitrieve) is a tool to archive repositories from any Git servers.

- [Features](#features)
- [Installation](#installation)
- [Usage](#usage)
  - [repository](#repository)
  - [run](#run)
  - [release](#release)
  - [daemon](#daemon)
- [Configuration](#configuration)
- [Storage](#storage)
- [Run as docker container](#run-as-docker-container)
  - [Docker CLI](#docker-cli)
  - [Docker Compose](#docker-compose)

## Features

- Archive repositories from any Git servers
- Archive repositories of a user/an organization (see [Configuration](https://github.com/wnarutou/gitrieve/wiki/Configuration#repository))
- Cron support
- Multiple storage types (see [Storage](#storage))
- Docker support (see [Run as docker container](#run-as-docker-container))

## Installation

```bash
curl -sSfL https://raw.githubusercontent.com/wnarutou/gitrieve/main/install.sh | sh -s -- -b /usr/local/bin
```

Or get from [Release](https://github.com/wnarutou/gitrieve/releases).

## Usage

You have to create a configuration file to use gitrieve.

```yaml
repository:
  - name: gitrieve
    url: github.com/wnarutou/gitrieve
    cron: "0 * * * *"
    storage:
      - localFile
      - backblaze
    useCache: True
    allBranches: True
    depth: 0
    downloadReleases: True
    downloadIssues: True
    downloadWiki: True
    downloadDiscussion: True

storage:
  - name: localFile
    type: file
    path: ./repo
  - name: backblaze
    type: s3
    endpoint: s3.us-west-000.backblazeb2.com
    region: us-west-000
    bucket: your-bucket-name
    accessKeyID: your-access-key-id
    secretAccessKey: your-secret-access-key
```

Then you can run gitrieve with the configuration file.

```bash
gitrieve -c config.yaml
# or simply call gitrieve if your configuration file is named config.yaml
gitrieve
```

### repository

`repository` archives a single repository defined in configuration.

```bash
gitrieve repository gitrieve
```

### run

`run` archives all repositories defined in configuration.

```bash
gitrieve run
```

Combined with cron, you can archive repositories periodically.

### release

`release` archives all release assets of a repository.

```bash
gitrieve release gitrieve
```

### daemon

`daemon` runs gitrieve as a daemon. It will archive all repositories defined in configuration periodically.

```bash
gitrieve daemon
# You might want to run it with something like nohup
nohup gitrieve daemon &
```

## Configuration

For configuration, you can check out this [example](config/example.config.yaml).

For more details, see [Configuration](https://github.com/wnarutou/gitrieve/wiki/Configuration) in wiki.

## Storage

gitrieve supports multiple storage types.

- [x] File
- [x] AWS S3

## Run as docker container

### Docker CLI

One-off run. 
- Change `${pwd}/config/example.config.yaml` to your config file path.
- Customize `${pwd}/repo:/repo` to be your desired storage path. The in-container path needs to be the same as the path in config file.

```bash
docker run --rm \
    -v ${pwd}/config/example.config.yaml:/config.yaml \
    -v ${pwd}/repo:/repo \
    wnarutou/gitrieve:latest \
    run
```

### Docker Compose

For example compose file, see [docker-compose.yml](docker-compose.yml).

```bash
docker compose up -d
```

## FAQ

See [FAQ](https://github.com/wnarutou/gitrieve/wiki/FAQ).

## Stargazers over time

[![Stargazers over time](https://starchart.cc/wnarutou/gitrieve.svg)](https://starchart.cc/wnarutou/gitrieve)

