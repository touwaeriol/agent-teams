# 用 Docker / Docker Compose 添加 agent-teams 节点

这份文档公开访问，可以直接把链接交给 AI 助手，让它照着在你的机器上部署节点。

- 控制面板：`__CONSOLE__`
- 节点镜像：`__IMAGE__`（amd64 + arm64）
- 本文地址：`__CONSOLE__/node-docker.md`

节点 = 你机器上的一个守护进程，主动连上控制面板；统筹、组长、成员（Claude Code / Codex 会话）都运行在节点上，
用的是节点上 Claude Code / Codex 的登录。容器方式把守护进程、Claude Code CLI、Codex CLI、git、gh、ssh、
python3/make/gcc 打在一个镜像里，不用在宿主机装 Node 或 systemd 服务。

## 给 AI 助手的要点

1. 需要 Docker 20.10+（Compose 方式需要 `docker compose` v2）。容器以 uid 1000 的 `agent` 用户运行，**不要** 加 `--user root`：Claude Code 的跳过权限模式拒绝 root。
2. 必须的环境变量只有两个：`AGENT_TEAMS_CONSOLE`（控制面板地址）和 `AGENT_TEAMS_NODE_TOKEN`（节点令牌）。令牌由用户在控制面板生成，**不要猜、不要编**，向用户要。
3. 五个目录必须持久化（见“持久化”一节），否则重建容器会丢登录和会话。
4. 一个令牌只能有一个容器在用；同一台机器跑多个节点 = 多个令牌、多个容器、各自独立的卷。
5. 部署后检查：`docker logs` 里出现已连接控制面板的日志，控制面板“节点”页这台节点显示在线；Claude 未授权时按“登录 Claude Code / Codex”一节处理。

## 1. 拿到节点令牌

登录控制面板 → 设置 → **节点** → **添加节点**，起个名字（字母、数字、`-`、`_`）。创建后弹窗里有三种启动方式：
“宿主机”“Docker”“Docker Compose”，后两种已经把令牌填好，复制即可。令牌只显示这一次；丢了就在节点卡片上“重新生成令牌”，旧令牌立刻失效。

## 2. 方式一：docker run

```bash
docker run -d --name agent-teams-node-mynode --hostname mynode \
  --restart unless-stopped --stop-timeout 60 \
  -e AGENT_TEAMS_CONSOLE=__CONSOLE__ \
  -e AGENT_TEAMS_NODE_TOKEN='<节点令牌>' \
  -v agent-teams-node-mynode-claude:/home/agent/.claude \
  -v agent-teams-node-mynode-codex:/home/agent/.codex \
  -v agent-teams-node-mynode-workspace:/home/agent/.agents_team \
  -v agent-teams-node-mynode-state:/home/agent/.local/state/agent-teams \
  -v agent-teams-node-mynode-data:/home/agent/.local/share/agent-teams \
  --log-opt max-size=20m --log-opt max-file=5 \
  __IMAGE__
docker logs -f agent-teams-node-mynode
```

把 `mynode` 换成节点名。令牌放在命令行里会进 shell 历史；介意的话改用 `--env-file node.env`（文件里一行一个 `KEY=value`，`chmod 600`）。

## 3. 方式二：Docker Compose（推荐）

新建一个目录，保存为 `compose.yml`（含令牌，`chmod 600 compose.yml`），然后 `docker compose up -d`：

```yaml
name: agent-teams-node-mynode
services:
  node:
    image: __IMAGE__
    hostname: "mynode"              # 加引号：节点名是 123、true 这类时也按字符串处理
    restart: unless-stopped
    stop_grace_period: 60s          # 留时间让守护进程暂停会话，下次启动可恢复
    environment:
      AGENT_TEAMS_CONSOLE: "__CONSOLE__"
      AGENT_TEAMS_NODE_TOKEN: "<节点令牌>"
      # AGENT_TEAMS_MAX_SESSIONS: "4"     # 同时运行的员工会话上限，默认不限
      # CLAUDE_CODE_OAUTH_TOKEN: ""       # Claude 授权，见第 5 节
      # ANTHROPIC_API_KEY: ""
      # OPENAI_API_KEY: ""                # Codex
      # HTTPS_PROXY: "http://user:pass@proxy.example:8080"   # 代理，见第 4 节
      # HTTP_PROXY: "http://user:pass@proxy.example:8080"
      # NO_PROXY: "localhost,127.0.0.1"
    volumes:
      - claude:/home/agent/.claude
      - codex:/home/agent/.codex
      - workspace:/home/agent/.agents_team
      - state:/home/agent/.local/state/agent-teams
      - data:/home/agent/.local/share/agent-teams
      # - ~/.ssh:/home/agent/.ssh:ro     # git 走 ssh 时打开（宿主机 key 需 uid 1000 可读）
    # cpus: "4"                          # 资源上限（可选）
    # mem_limit: 8g
    logging:
      driver: json-file
      options: { max-size: "20m", max-file: "5" }
volumes:                             # 和 docker run 方式同名的卷：两种方式互换时登录和工作区都在
  claude: { name: agent-teams-node-mynode-claude }
  codex: { name: agent-teams-node-mynode-codex }
  workspace: { name: agent-teams-node-mynode-workspace }
  state: { name: agent-teams-node-mynode-state }
  data: { name: agent-teams-node-mynode-data }
```

也可以把变量放进 `node.env`，在服务里写 `env_file: node.env` 代替 `environment:`。

常用命令：`docker compose logs -f node`、`docker compose restart node`、`docker compose down`（卷保留）。

## 4. 代理

机器要通过代理才能访问外网时，设置 `HTTPS_PROXY` / `HTTP_PROXY`（大小写都认）和 `NO_PROXY`：

- 守护进程连控制面板的 WebSocket 会走 `HTTPS_PROXY`，遵守 `NO_PROXY`；
- git、Claude Code、Codex 会话都会继承这些变量；
- 代理 URL 带账号密码（`http://user:pass@host:port`）时，git 自动用 basic 认证；需要别的方式时设置 `GIT_HTTP_PROXY_AUTHMETHOD`；
- 代理做 TLS 拦截时，把代理的 CA 证书挂进容器并设置 `NODE_EXTRA_CA_CERTS=/path/ca.crt`（Node / Claude Code）和 `SSL_CERT_FILE` / `GIT_SSL_CAINFO`（git）。

代理地址写 `127.0.0.1` 指的是容器自己，不是宿主机。宿主机上的代理请用宿主机的局域网 IP，或加
`extra_hosts: ["host.docker.internal:host-gateway"]` 后写 `http://host.docker.internal:端口`（代理需监听非 127.0.0.1 的地址）。

## 5. 登录 Claude Code / Codex

二选一：

1. **环境变量**：`CLAUDE_CODE_OAUTH_TOKEN`（订阅账号，在任意装了 Claude Code 的机器上执行 `claude setup-token` 得到）或 `ANTHROPIC_API_KEY`。Codex 用 `OPENAI_API_KEY`。改完 `docker compose up -d` 重建容器。
2. **进容器登录**（不设上面的变量）：

   ```bash
   docker compose exec node claude login      # docker run 方式：docker exec -it agent-teams-node-mynode claude login
   docker compose exec node codex login       # 可选，用 Codex 时
   docker compose restart node                # 守护进程启动时检测授权，登录后重启一次
   ```

   登录状态存在 `claude` / `codex` 卷里，重建容器不用重登。

控制面板“节点”页会显示每台节点上 Claude Code / Codex 的安装和登录状态，可以点“重新检测”。

## 6. 持久化

| 容器内路径 | 内容 | 丢了会怎样 |
| --- | --- | --- |
| `/home/agent/.claude` | Claude 登录、配置、会话记录 | 要重新登录，进行中的会话无法恢复 |
| `/home/agent/.codex` | Codex 登录和线程 | 要重新登录 |
| `/home/agent/.agents_team` | 工作区：仓库、worktree、员工 HOME | 仓库要重新克隆，未推送的改动丢失 |
| `/home/agent/.local/state/agent-teams` | 守护进程状态：消息队列、会话记录 | 会话无法恢复 |
| `/home/agent/.local/share/agent-teams` | 守护进程版本（网页一键更新用） | 回到镜像自带版本 |

用命名卷最省事。想用宿主机目录代替时，先 `mkdir -p` 并 `chown -R 1000:1000`，否则容器会提示目录不可写并退出。

## 7. 更新

- **守护进程**：控制面板“节点”页点这台节点的“更新”（可选等空闲 / 立即）。容器内下载新版本到 `data` 卷，切换后自动重启，新版本 5 分钟内没确认正常会自动切回旧版。容器不重建，不需要挂 docker.sock。
- **镜像**（git、gh、Node、Claude Code / Codex CLI 等系统工具）：`docker compose pull && docker compose up -d`；docker run 方式则 `docker pull` 后删掉旧容器、用同样的命令（同样的卷名）重新 `docker run`。
- **换令牌**：在网页重新生成令牌，改 `compose.yml` / `node.env` 里的 `AGENT_TEAMS_NODE_TOKEN`，`docker compose up -d`。卷不用动。

## 8. 排错

| 现象 | 原因和处理 |
| --- | --- |
| 容器反复重启，日志“不能用 root 运行” | 去掉 `--user` / `user:` 覆盖 |
| 日志“… 不可写（uid 1000）” | 绑定挂载的宿主目录要 `chown -R 1000:1000` |
| 日志“缺少 AGENT_TEAMS_CONSOLE / AGENT_TEAMS_NODE_TOKEN” | 环境变量没传进去，检查 `environment:` / `--env-file` |
| 节点一直离线，日志 401 / token | 令牌错了或已被重新生成，换成最新令牌 |
| 节点上线后又掉线、两个容器轮流上线 | 同一个令牌被两个容器（或宿主机安装 + 容器）同时使用，每个节点一个令牌 |
| 连不上控制面板（超时、ECONNRESET） | 检查网络和代理（第 4 节），容器内试 `curl -I __CONSOLE__/healthz` |
| “Claude 尚未授权”，员工会话起不来 | 按第 5 节登录后重启 |
| git clone / fetch 失败、407 | 代理认证问题，确认代理 URL 带账号密码，或设置 `GIT_HTTP_PROXY_AUTHMETHOD=basic` |
| 需要 Go / Java / 数据库客户端等工具 | 以本镜像为基础写 Dockerfile：`FROM __IMAGE__`，`USER root` 装完再 `USER agent` |

进容器看情况：`docker compose exec node bash`。容器里没有 bubblewrap，节点报告的隔离级别是 `none`，容器本身就是隔离边界。

镜像源码和更多说明：<https://github.com/touwaeriol/agent-teams/tree/develop/deploy/node>
