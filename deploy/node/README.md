# 节点容器镜像

把节点守护进程 `agent-teams-node` 和员工要用的命令行工具打成一个镜像，用容器代替
`install-node.sh` + systemd 用户服务来跑节点。

镜像里有：Node 22、守护进程（与控制面板下发的发布包同一份）、Claude Code CLI（Agent SDK 自带的那一份，
和员工会话用的版本一致）、Codex CLI（守护进程依赖的同一版本）、git、ssh、gh、python3/make/gcc（基础镜像是完整版
`node:22-bookworm`，方便员工构建和测试项目）、tini。守护进程是入口进程，以普通用户 `agent`（uid 1000）运行：
Claude Code 的 bypassPermissions 模式不能在 root 下运行，入口脚本发现 root 会直接退出。

## 快速开始

```bash
cd deploy/node
cp node.env.example node.env        # 填 AGENT_TEAMS_NODE_TOKEN（控制面板“节点”页 → 添加节点）
docker compose up -d                # 拉取 ghcr.io/touwaeriol/agent-teams-node:latest；看日志：docker compose logs -f node
```

镜像由 `.github/workflows/node-image.yml` 发布：develop 上改到守护进程或镜像时自动构建 amd64 + arm64，
推送 `:latest`、`:develop`、`:sha-<短哈希>`。想用自己改过的代码，就在仓库里 `docker compose up -d --build` 就地构建
（同一个标签会被本地镜像覆盖），或用 `AGENT_TEAMS_NODE_IMAGE` 指定别的镜像。GHCR 新建的包默认私有：
要么在包设置里改成公开，要么先 `docker login ghcr.io`（用有 `read:packages` 权限的令牌）。

`node.env` 里的变量：

| 变量 | 说明 |
| --- | --- |
| `AGENT_TEAMS_CONSOLE` | 控制面板地址，默认 `https://agent.prophey.ai` |
| `AGENT_TEAMS_NODE_TOKEN` | 节点令牌（必填）。一个令牌只能有一个容器在用，同令牌的第二个连接会把前一个顶掉 |
| `AGENT_TEAMS_MAX_SESSIONS` | 可选，同时运行的员工会话上限，留空 = 不限 |
| `AGENT_TEAMS_LOG_LEVEL` | 可选，默认 `info` |
| `AGENT_TEAMS_ALLOW_INSECURE` | 可选，默认关。控制面板地址必须是 `https://`；只有在可信内网里确实要用 `http://` 时才设成 `1`（localhost/127.0.0.1 不需要），启动时会打警告 |
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | Claude 授权方式一，见下 |
| `OPENAI_API_KEY` | 可选，Codex 用 API key；也可以 `codex login` |

## Claude 授权（两种都支持）

1. **环境变量**：在 `node.env` 里设置 `CLAUDE_CODE_OAUTH_TOKEN`（订阅账号，在任意装了 Claude Code 的机器上执行
   `claude setup-token` 得到）或 `ANTHROPIC_API_KEY`。守护进程会把它传给每个员工会话。
2. **进容器登录**：两个变量都不设，启动后执行

   ```bash
   docker compose exec node claude login     # 按提示在浏览器里完成授权
   docker compose restart node               # 守护进程启动时检测授权，登录后重启一次
   ```

   凭据写在 `~/.claude`（镜像里设置了 `CLAUDE_CONFIG_DIR=/home/agent/.claude`，`claude login` 和员工会话用同一个目录），
   这个目录是 volume，重建容器不用重新登录。Codex 同理：`docker compose exec node codex login`。

控制面板“节点”页能看到这台节点的 Claude / Codex 授权状态（`subscription` / `api_key` / `none`）。

## 持久化

| 容器内路径 | volume | 内容 |
| --- | --- | --- |
| `/home/agent/.claude` | `claude` | Claude 登录凭据、配置、会话记录（会话恢复靠它） |
| `/home/agent/.codex` | `codex` | Codex 登录和线程 |
| `/home/agent/.agents_team` | `workspace` | 工作区：仓库、worktree、员工的 HOME |
| `/home/agent/.local/state/agent-teams` | `state` | 守护进程状态：消息收发队列、会话记录 |
| `/home/agent/.local/share/agent-teams` | `data` | 守护进程版本（`releases/`、`current`、`previous`），控制面板“更新”切换的就是这里 |

想用宿主目录代替命名 volume 时，先 `chown -R 1000:1000` 这些目录，否则入口脚本会提示不可写并退出。
git 走 ssh 的话，在 `compose.yml` 里打开 `~/.ssh:/home/agent/.ssh:ro` 那一行。

## 不用 compose

```bash
docker run -d --name agent-teams-node --restart unless-stopped --env-file deploy/node/node.env \
  -v at-claude:/home/agent/.claude -v at-codex:/home/agent/.codex \
  -v at-workspace:/home/agent/.agents_team -v at-state:/home/agent/.local/state/agent-teams \
  -v at-data:/home/agent/.local/share/agent-teams \
  ghcr.io/touwaeriol/agent-teams-node:latest
docker exec -it agent-teams-node claude login
```

除 `run`（默认）以外的命令会代替守护进程执行，例如 `docker run --rm -it ghcr.io/touwaeriol/agent-teams-node bash`。自己构建：在仓库根目录 `docker build -f deploy/node/Dockerfile -t agent-teams-node .`。

## 其他说明

- **更新守护进程**：控制面板“节点”页点“更新”即可。入口脚本跑的是更新启动器：守护进程在容器里下载新版本到 `data`
  volume、等员工手上的回合结束、切换后由启动器重启，新版本 5 分钟内没确认正常就自动切回旧版，容器本身不重建，也不需要 docker.sock。
- **更新镜像**（git、gh、Node 等系统工具变了时）：`docker compose pull && docker compose up -d`（自己构建的就 `up -d --build`）。
  镜像带的版本比 `data` volume 里的新时，启动器以镜像为准。旧的 compose（没有 `data` volume）按新的 `compose.yml` 重建一次后才能一键更新。
- **构建参数**：`GH_VERSION`、`TINI_VERSION`、`NODE_IMAGE`。在 TLS 拦截代理后面构建时，可以把代理的 CA 作为
  BuildKit secret 传入（`--secret id=build_ca,src=ca.crt`），只在下载步骤里挂载，不会留在镜像里。
- **隔离**：容器里没有 bubblewrap，节点报告的隔离级别是 `none`；容器本身就是隔离边界。
- **需要更多工具**（Go、Java、数据库客户端……）：以这个镜像为基础写自己的 Dockerfile，`USER root` 装完再 `USER agent`。
