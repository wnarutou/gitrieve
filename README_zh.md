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
- [Web UI](#web-ui)
- [配置](#配置)
- [存储](#存储)
- [防删除同步](#防删除同步)
- [使用 Docker 运行](#使用-docker-运行)
  - [Docker CLI](#docker-cli)
  - [Docker Compose](#docker-compose)

## 功能

- 从任何Git服务器归档 Git 仓库
- 归档用户/组织的仓库（见 [配置](https://github.com/wnarutou/gitrieve/wiki/Configuration#repository))
- 定时任务
- 多种存储类型（见 [存储](#存储)）
- **防删除同步** —— 同步过程绝不会删除本地已拉取的代码与完整历史，即使上游仓库被下线、DMCA 禁用、删除、私有化或被替换为单个 README（见 [防删除同步](#防删除同步)）
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

## Web UI

`server` 命令会启动一个 Web UI 和 HTTP API，让你可以通过浏览器管理归档任务和配置。

```bash
gitrieve server
# 默认监听 http://localhost:8080
```

在 UI 中你可以触发归档任务、查看实时日志，并直接编辑仓库/存储配置而无需手动修改 `config.yaml`。服务器通过 `config.yaml` 中可选的 `server` 配置段进行配置（host、port 以及可选的 Bearer Token 认证）。

详见 [Web UI 指南](docs/web-ui.md) 和 [API 文档](docs/api.md)。

## 配置

有关配置，你可以查看此[示例](config/example.config.yaml)。

更多细节，可查看[配置文档](https://github.com/wnarutou/gitrieve/wiki/Configuration)。

## 存储

gitrieve支持多种存储类型。

- [x] 文件
- [x] AWS S3

## 防删除同步

gitrieve 的一个核心设计目标是：**一旦代码和历史被拉取到本地，同步过程绝不删除它们** —— 即使上游仓库被下线、DMCA 禁用、删除、私有化或被替换为单个 README。这使得 gitrieve 适合作为真正的归档/备份工具，而不仅仅是镜像。

该特性由同步逻辑在以下几方面保证：

- **远端不可达 → 提前退出，什么都不动。** 当上游仓库被删除、被禁用（如 DMCA）或被私有化且无权访问时，`fetch` 会失败，同步随即提前返回。不会执行归档，也不会执行清理，本地缓存以及此前已归档的快照均原封不动。
- **分支只增不删。** 同步遍历远端分支来创建/更新本地分支，但从不删除本地分支。上游已删除的分支在本地依然保留。
- **使用 pull 而非 reset。** 更新通过 `git pull`（合并）应用，从不使用 `git reset --hard origin`。即便上游通过 force-push 重写了默认分支，也只会移动 `origin/*` 追踪引用；你的本地分支及其旧提交对象不会被覆盖或丢弃。
- **旧提交被保留。** 由于提交是不可变对象，且同步从不强制移动本地引用，已经拉取的完整历史会留在本地 `.git` 对象库中。任一历史提交都可通过 `git checkout <旧hash>` 恢复。

为获得最大可恢复性，建议配置：

- `allBranches: true` —— 确保每个分支的提交都被拉入本地对象库。
- `useCache: true` —— 跨同步保留本地缓存目录（含 `.git`）作为额外的磁盘安全网（若不开启，工作目录会在每次同步结束时被删除）。

需要注意的一点：归档写入固定文件名（如 `repo.tar.gz`），会覆盖同路径下的旧归档。因此，若上游仍可访问但其默认分支被重写为单个 README，*新*快照会替换该路径下此前正常的归档。本地缓存的代码与历史仍然安全（见上文），但若要保留不同的历史快照，建议在对象存储（S3/B2）上启用版本控制，或以带版本号的路径保存归档。

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
