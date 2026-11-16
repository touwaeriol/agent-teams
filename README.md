# agent-teams

在你自己的机器上组建 Claude Code / Codex 团队：你只和项目统筹对话，它开讨论串；每个讨论串有一个组长拆任务、审查、合并，成员按岗位从任务池领小任务，在各自的节点上并行干活，结果以分支和 PR 交付。

多用户平台：网页注册，各自管理自己的节点、GitHub 连接、项目和团队，管理员后台。所有模型调用都走节点上用户自己登录的 Claude Code / Codex，平台不持有 API key。

## 平台怎么用

```
浏览器 ──▶ 控制台（Go，atctl serve）◀──WebSocket（协议 2）── 节点 A：agent-teams-node ─ Claude / Codex 员工
              │  PostgreSQL 17 / Valkey 9                       节点 B：agent-teams-node ─ …
              └─ GitHub App / 访问令牌：分支、合并队列、CI、PR
```

1. **注册登录**：开放注册（管理员可以关闭）。第一个注册的账号，以及 `AGENT_TEAMS_ADMIN_EMAILS` 里的邮箱，自动成为管理员。
2. **添加节点**：节点是你自己的机器，先装好 Node.js ≥ 22 并登录 Claude Code（Codex 可选）。在“设置 → 节点”添加后会显示一条安装命令（令牌只显示一次），在节点上以要运行员工的用户执行：

   ```bash
   curl -fsSL https://控制台/install-node.sh | AGENT_TEAMS_NODE_TOKEN=… bash -s -- --console https://控制台 --name gpu-1
   ```

   守护进程以 systemd 用户服务运行，主动连回控制台，节点不用开放端口。重新执行同一条命令就是原地升级（沿用 `~/.config/agent-teams/node.env` 里的令牌），`--rollback` 切回上一版。
3. **连接 GitHub**：在“设置 → GitHub”点“连接 GitHub”，在 GitHub 上选账号或组织、勾选仓库，授权后自动回来；每次需要时现取 1 小时的安装令牌，只放进运行环境，不写进节点的文件。
4. **团队（岗位模板）**：在“设置 → 团队”配置统筹、组长和成员岗位（运行时 Claude / Codex、候选节点、模型、权限模式、技能与插件、办公室形象），也可以直接编辑 YAML：

   ```yaml
   coordinator:
     title: 项目统筹
   lead:
     title: 组长
   members:
     - role: dev
       title: 工程师
       count: 2
       nodes: [gpu-1]
     - role: review
       title: 审查
       gate: review
   ```

5. **项目**：新建项目时选一个或多个仓库、写项目说明（基准分支等约定写在说明里），所有员工开工前都会读到。
6. **对话与讨论串**：在项目里和统筹说需求；统筹开讨论串，组长写主要任务、把小任务派进任务池，成员按岗位领取，在 worktree 里干活并推送自己的分支；组长审查、合并队列跑 CI，最后每个仓库开一个 PR，由你验收。员工需要你决定时会发提问卡片。讨论串可以归档、恢复、删除，办公室页实时显示每个员工在做什么。
7. **管理员**：“设置 → 管理”管理用户（角色、停用、删除、节点上限、同时运行的讨论串上限）和平台设置；命令行 `atctl admin reset-password|set-role|reseal`。

**可靠性**：所有状态在 PostgreSQL；控制台重启、节点断线、守护进程重启后，会话按 SDK session id 恢复，恢复不了就用对话回顾重建。节点命令有发件箱、序号和确认，重连后补发。

**数据隔离**：每个用户只看到自己的项目、节点、连接和团队；节点令牌和会话令牌在库里只存哈希，Git 令牌用 `AGENT_TEAMS_SECRET_KEY` 加密保存。

## 部署

只需要 Docker。`deploy/docker-compose.yml` 包含 `postgres`、`valkey`、一次性的 `migrate`（`atctl migrate up`）和 `control-plane`；一律通过 `deploy/compose.sh` 运行（compose 项目目录 = 仓库根，`./data`、`./office` 相对它解析）。

```bash
git clone <仓库地址> agent-teams && cd agent-teams
cp deploy/env.example .env && chmod 600 .env    # 填 PORT、AGENT_TEAMS_PUBLIC_URL，三个密钥用 openssl rand -hex 24 生成
deploy/compose.sh build
deploy/compose.sh up -d --wait                  # postgres、valkey → migrate → control-plane
curl -fsS http://127.0.0.1:$PORT/healthz
```

- `POSTGRES_PASSWORD`、`VALKEY_PASSWORD`、`AGENT_TEAMS_SECRET_KEY` 生成后不要改：数据目录用前两个初始化，第三个解开所有加密保存的 Git 令牌和 GitHub App 密钥（轮换用 `AGENT_TEAMS_SECRET_KEYS=new,old` + `atctl admin reseal`）。
- 服务默认只监听 `127.0.0.1:$PORT`，由宿主机的 Caddy 提供 HTTPS：`agent.example.com { reverse_proxy 127.0.0.1:3001 { flush_interval -1 } }`（SSE 不能缓冲；WebSocket 自动转发）。
- “连接 GitHub”需要一个 GitHub App：部署后用管理员账号打开 `/setup/github`，一键创建（GitHub 的 manifest 流程，只做一次）。
- 镜像里带网页和节点守护进程的发布包（`/node/dist/`），办公室布局从 `./office` 挂载并热加载。

**升级**（生产把源码放在 `./src`，`.env` 里设 `BUILD_CONTEXT=./src`、`OFFICE_HOST_DIR=./src/office`、`AGENT_TEAMS_IMAGE=agent-teams-server:<sha>`）：

```bash
docker build -f src/server/Dockerfile -t agent-teams-server:<sha> src   # 新源码
# 改 .env 里的 AGENT_TEAMS_IMAGE，然后：
docker compose run --rm migrate && docker compose up -d --wait control-plane
```

数据库迁移在 `server/migrations`（goose）；带数据删除的迁移（如 `00120_drop_v1`）不能回退，升级前留好数据库备份。

## 仓库结构

```
server/      Go 控制台 atctl：Gin + huma（OpenAPI）+ Casbin + GORM，goose 迁移，节点网关，编排引擎
node/        TypeScript 节点守护进程（Claude Agent SDK、codex app-server、平台 MCP 工具、本地 git）
web/         Vue 3 + Element Plus 网页
protocol/    JSON Schema（节点协议、平台工具、办公室布局）和生成的 Go / TS 类型
office/      默认办公室布局与像素素材
deploy/      compose、env.example、e2e/（本机端到端测试）
docs/v2/     设计文档：architecture、data-model、api、protocol、office-layout
docs/        threads.md、agent-comms.md（v1 时期的设计约定，作历史保留）
```

## 开发

```bash
server/scripts/test.sh                          # Go：gofmt、vet、测试（docker 里，临时 PG 17 + Valkey 9）
cd node && npm ci && npm test && npm run typecheck
cd web && npm ci && npm test && npm run typecheck && npm run lint && npm run build
cd web && npm run dev:mock                      # 带 mock 接口的前端开发
cd protocol && npm ci && npm test && npm run typecheck
make gen-protocol                               # 改了 protocol/schemas 之后重新生成
```

真实 Claude 的端到端测试见 `deploy/e2e/README.md`。

## 许可证

MIT
