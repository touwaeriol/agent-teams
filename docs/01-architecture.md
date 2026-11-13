# 01 架构

## 组件

```
                     ┌──────────────────────────── 控制面 ────────────────────────────┐
浏览器 ──HTTPS/WS──▶ │ Web 前端   API(REST+WS)   MCP(/mcp)   调度器   凭证签发   节点网关 │ ◀──WSS── 守护进程（每节点一个）
MCP 客户端 ─HTTPS──▶ │                         PostgreSQL（状态 · 对话 · 事件 · 记忆）        │            │
                     └───────────────────────────────────┬─────────────────────────────┘            ├─ 员工目录 employees/<项目>/<员工>/
                                                         │                                          ├─ 会话：claude / codex 子进程
                                                   GitHub（第一版只支持 GitHub）       ◀── git ────┘   （凭证由 helper 现取）
```

### 控制面（apps/server）

| 模块 | 职责 |
| --- | --- |
| API | Web 前端用的 REST 接口和 WebSocket 推送（办公室动画、对话、任务、请求的实时更新） |
| MCP | `/mcp`，Streamable HTTP。对外给人用；同一套工具以“员工”身份经守护进程转给员工用 |
| 节点网关 | 守护进程的 WebSocket 长连接：注册、心跳、指令下发、事件接收、断线重连补报 |
| 调度器 | 任务队列、员工分配、槽位 / 通道 / 额度检查、负载均衡、统筹唤醒 |
| 凭证签发 | 按员工身份签发 Git 短期令牌，写审计 |
| 存储 | PostgreSQL。状态表 + 只追加的事件表（用于时间线和回放） |

控制面是唯一的事实来源：员工配置、记忆、对话、任务都存在这里；节点上的文件是副本，丢了可以重建。

### 守护进程（apps/daemon）

每个节点一个，以登录了 Claude Code 的那个系统用户运行（systemd 用户服务）。

| 模块 | 职责 |
| --- | --- |
| 连接 | 用节点凭证连控制面 WSS，断线指数退避重连，重连后补报未确认的事件 |
| 扫描 | 找 `claude` / `codex`，读版本、登录状态、额度（定期重扫） |
| 员工目录 | 按控制面下发的员工规格建目录、写配置文件、同步记忆；回收时检查未推送改动 |
| 会话监管 | 启动 / 续跑 / 停止 agent 子进程，按员工加锁（同一时间一个会话），进程组管理（含 agent team 队友） |
| 事件转换 | 把 Claude Code stream-json、Codex app-server 的输出转成统一事件上报 |
| 本地 MCP | 给员工会话提供 `agent-teams` stdio MCP 服务（report / ask / remember / write_handoff / 统筹工具），调用转发给控制面 |
| Git helper | `git-credential-agent-teams`，经本机 unix socket 向守护进程要凭证，守护进程再向控制面要 |
| 钩子 | `agent-teams-hook`：Claude Code 的 SessionStart（注入记忆）、PreToolUse（拦截 AskUserQuestion）等 |

### Web 前端（apps/web）

页面见 [10-ui.md](10-ui.md)。办公室页的人物位置由员工状态推出（[05-scheduling.md](05-scheduling.md#员工状态机)），动画来自事件流。

## 技术选型（建议）

| 部分 | 选型 | 理由 |
| --- | --- | --- |
| 语言 | TypeScript，pnpm monorepo | 和 agent-remote-worker 一致；控制面、守护进程、前端共享协议类型 |
| 控制面 | Node.js 22 + Fastify（或 Hono）+ `ws` | 成熟、轻 |
| 数据库 | PostgreSQL 16 + Drizzle ORM | 关系模型清楚；用 LISTEN/NOTIFY 做进程内广播，第一版不引入 Redis |
| MCP | `@modelcontextprotocol/sdk` 的 Streamable HTTP 服务端 | 官方 SDK |
| 前端 | React + Vite；办公室画布用 Canvas 2D 或 PixiJS | 像素办公室可沿用现有素材 |
| 守护进程 | Node.js（esbuild 打成单文件）| 节点本来就要装 Claude Code；以后需要无依赖安装再考虑换 Go |
| 密钥 | 信封加密：主密钥来自环境变量 / KMS，数据密钥加密后存表 | Git 私钥、令牌不明文落库 |

目录结构：

```
apps/server     控制面
apps/web        前端
apps/daemon     守护进程（含 mcp-stdio、git-credential、hook 三个入口）
packages/protocol   守护进程协议、统一事件、MCP 工具的类型和 schema
packages/core       调度算法、状态机等纯逻辑（便于单测）
docs/
```

## 部署形态

- 控制面：单实例 + PostgreSQL 即可起步。WebSocket 连接粘在实例上；以后多实例时用 LISTEN/NOTIFY 广播，节点网关按 node_id 路由。
- 节点：执行安装命令或由控制面经 SSH 安装一次，之后只有出站连接，节点可以在内网 / NAT 后。
- 控制面需要被节点和 MCP 客户端访问到（公网域名或内网地址 + TLS）。

## 安全边界

| 边界 | 措施 |
| --- | --- |
| 人 → 控制面 | Web 登录（第一版邮箱密码或 GitHub 登录）；MCP 用访问令牌，之后加 OAuth |
| 节点 → 控制面 | 一次性注册令牌换长期节点凭证（存 `~/agent-teams/daemon/credential`，0600），可轮换、可吊销 |
| 员工 → 控制面 | 会话启动时守护进程生成会话令牌（只在该会话进程的环境变量里），员工工具调用都带它；控制面据此确定“哪个员工、哪个项目” |
| Git | 节点不存长期凭证；令牌按员工身份现签，最短有效期 |
| 员工之间 | 第一版只做目录隔离（同一系统用户）。沙箱策略留扩展点：系统用户隔离、容器隔离 |

## Web API 概要

REST，前缀 `/api`，JSON。列出主要资源，字段见 [02-data-model.md](02-data-model.md)。

| 资源 | 接口 |
| --- | --- |
| 项目 | `GET/POST /projects`，`GET/PATCH /projects/:id`，`POST /projects/:id/repositories`，`PUT /projects/:id/allocations`（模板 × 节点数量矩阵） |
| 对话 | `GET /projects/:id/messages?after=seq`，`POST /projects/:id/messages`，`POST /projects/:id/plans/:planId/confirm` |
| 任务 | `GET /projects/:id/tasks`，`POST /projects/:id/tasks`，`POST /tasks/:id/cancel` |
| 员工 | `GET /projects/:id/employees`，`GET /employees/:id`，`PATCH /employees/:id`，`POST /employees/:id/{pause,resume,stop,message}` |
| 会话 | `GET /employees/:id/sessions`，`GET /sessions/:id/events` |
| 请求 | `GET /requests?state=pending`，`POST /requests/:id/answer` |
| 记忆 | `GET/POST /employees/:id/memories`，`PATCH/DELETE /memories/:id`，`POST /memories/:id/promote`，`GET /projects/:id/knowledge` |
| 模板 | `GET/POST /templates`，`GET/PATCH /templates/:id` |
| 节点 | `GET /nodes`，`POST /nodes`（返回安装命令或启动 SSH 安装），`POST /nodes/:id/{rescan,disable}`，`PATCH /nodes/:id`（槽位等） |
| Git | `GET/POST /git-connections`，`GET /git-connections/:id/repositories`，`GET /credential-issuances` |
| MCP 授权 | `GET/POST /api-tokens`，`DELETE /api-tokens/:id` |

WebSocket `/api/ws`：订阅 `project:<id>`（对话、任务、员工状态、办公室事件）、`employee:<id>`（时间线）、`nodes`。
