# gitrieve

[English](README.md) | 简体中文

Git Retrieve（gitrieve）是一个用于从任何Git服务器归档 Git 仓库的工具。

- [功能](#功能)
- [安装](#安装)
- [使用方法](#使用方法)
  - [repository](#repository)
  - [run](#run)
  - [daemon](#daemon)
  - [release](#release)
- [配置](#配置)
- [存储](#存储)
- [使用 Docker 运行](#使用-docker-运行)
  - [Docker CLI](#docker-cli)
  - [Docker Compose](#docker-compose)

## 功能

- 从任何Git服务器归档 Git 仓库
- 归档用户/组织的仓库（见 [配置](https://github.com/wnarutou/gitrieve/wiki/Configuration#repository))
- 定时任务
- 多种存储类型（见 [存储](#存储)）
- Docker 支持（见 [使用 Docker 运行](#使用-docker-运行)）

## 安装

```bash
curl -sSfL https://raw.githubusercontent.com/wnarutou/gitrieve/main/install.sh | sh -s -- -b /usr/local/bin
```

或从 [Release](https://github.com/wnarutou/gitrieve/releases) 获取。

## 使用方法

你需要创建一个配置文件来使用gitrieve。

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

然后，你可以使用配置文件运行gitrieve。

```bash
gitrieve -c config.yaml
# 或者如果你的配置文件名为config.yaml，只需调用gitrieve
gitrieve
```

### repository

`repository`命令会归档在配置中定义的单个 Git 仓库。

```bash
gitrieve repository gitrieve
```

### run

`run`命令会归档在配置中定义的所有 Git 仓库。

```bash
gitrieve run
```

结合cron，你可以定期归档 Git 仓库。

### release

`release`命令会归档指定 Git 仓库的所有发布产物。

```bash
gitrieve release gitrieve
```

### daemon

`daemon`命令会启动一个守护进程，它会在后台运行，归档在配置中定义的所有 Git 仓库。

```bash
gitrieve daemon
# 使用 nohup 后台运行
nohup gitrieve daemon &
```

## 配置

有关配置，你可以查看此[示例](config/example.config.yaml)。

更多细节，可查看[配置文档](https://github.com/wnarutou/gitrieve/wiki/Configuration)。

## 存储

gitrieve支持多种存储类型。

- [x] 文件
- [x] AWS S3

## 使用 Docker 运行

### Docker CLI

一次性运行。 
- 修改 `${pwd}/config/example.config.yaml` 为你的配置文件本地路径。
- 自定义 `${pwd}/repo:/repo` 为你需要的存储路径。容器内路径需要与配置文件中的路径一致。

```bash
docker run --rm \
    -v ${pwd}/config/example.config.yaml:/config.yaml \
    -v ${pwd}/repo:/repo \
    wnarutou/gitrieve:latest \
    run
```

### Docker Compose

示例Compose配置，见 [docker-compose.yml](docker-compose.yml)。

```bash
git clone https://github.com/wnarutou/gitrieve.git
docker compose up -d
```

## 常见问题

见 [FAQ](https://github.com/wnarutou/gitrieve/wiki/FAQ)。

## Stargazers over time

[![Stargazers over time](https://starchart.cc/wnarutou/gitrieve.svg)](https://starchart.cc/wnarutou/gitrieve)
