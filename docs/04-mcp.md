# 04 MCP

一套工具定义，两种身份：

| 身份 | 入口 | 认证 | 能操作的范围 |
| --- | --- | --- | --- |
| 人 | 控制面 `https://<控制面>/mcp`（Streamable HTTP） | 访问令牌（之后加 OAuth） | 令牌允许的项目和权限 |
| 员工 | 守护进程提供的本地 stdio 服务 `agent-teams-mcp`，经守护进程转发 | 会话令牌 | 只有自己的项目；工具集合由身份决定 |

工具实现都在控制面；员工调用只是多了一跳转发。

## 安装（人）

```bash
# Claude Code
claude mcp add --transport http agent-teams https://<控制面>/mcp \
  --header "Authorization: Bearer $AGENT_TEAMS_TOKEN"
```

```toml
# Codex（~/.codex/config.toml，字段名以所用版本文档为准）
[mcp_servers.agent-teams]
url = "https://<控制面>/mcp"
bearer_token_env_var = "AGENT_TEAMS_TOKEN"
```

其他支持 Streamable HTTP 的客户端同理。访问令牌在 Web 的“MCP 接入”页创建。

## 权限范围

| scope | 允许 |
| --- | --- |
| `read` | 所有只读工具 |
| `chat` | `send_message` |
| `approve` | `answer` |
| `manage` | `create_project`、`add_repo` |

## 对外工具（人）

参数用 TypeScript 风格写，实现时转成 JSON Schema。`project` 参数接受 id 或 slug。所有工具立即返回，不会等任务跑完。

### 项目

```ts
list_projects(): {
  projects: { id, slug, name, status, coordinator: string, plan: { done: number, total: number } | null,
              pending_requests: number, last_activity_at: string }[]
}

create_project(args: {
  name: string
  brief: string                       // 需求，成为对话第一条消息
  repositories: string[]              // GitHub 仓库链接或 owner/repo（第一版只支持 GitHub）
  templates?: { template: string,     // 模板名或 id
                nodes?: Record<string, number>  // 节点名 → 数量；省略 = 自动分配 1 个
              }[]                     // 省略 = 用平台默认组合；统筹总会自动加上
  plan_mode?: "confirm" | "auto"      // 默认 confirm
}): { project: { id, slug }, coordinator: string, web_url: string }

get_project(args: { project: string }): {
  project, repositories, plan: { version, status, steps[] } | null,
  employees: { name, template, node, status, current_task? }[],
  tasks: { counts: Record<state, number>, running: TaskBrief[] },
  deliveries: { task, branch, pr_url?, state }[],
  pending_requests: RequestBrief[]
}

add_repo(args: { project: string, repository: string, role?: "primary" | "readonly" }): { ok: true }
// repository：GitHub 链接或 owner/repo；平台要有能访问它的 GitHub 连接
```

### 对话

```ts
send_message(args: {
  project: string
  text: string
  wait_seconds?: number               // 0–120，等统筹回一句；默认 0
}): {
  message: { seq: number }
  replies: Message[]                  // 等待期间统筹发出的回复（可能为空）
}

get_messages(args: {
  project: string
  after_seq?: number
  limit?: number                      // 默认 50
  include_activity?: boolean          // 是否带折叠的员工汇报，默认 false
}): { messages: Message[], next_seq: number }

// Message = { seq, author: "user" | "coordinator" | "system" | <员工名>, source, kind, text?, plan?, tasks?, request?, created_at }
```

### 任务与审批

```ts
list_tasks(args: { project: string, state?: TaskState }): { tasks: TaskBrief[] }
get_task(args: { task: string }): {   // "#1315" 需配合 project，或直接用 id
  task, assignee?, deliver_branch?, pr_url?, handoff_md?, recent_events: EventBrief[]
}

list_pending(args: { project?: string }): { requests: RequestBrief[] }
answer(args: {
  request: string
  decision?: "allow" | "deny"         // 权限请求
  remember?: boolean                  // 本会话同类操作不再问
  message?: string                    // 拒绝理由
  answers?: Record<string, string>    // 提问的回答
}): { ok: true }
```

### 平台（只读）

```ts
list_nodes(): { nodes: { name, status, runtimes: { kind, version, logged_in, quota_remaining_pct?, slots_used, slots }[] }[] }
list_employees(args: { project?: string }): { employees: { name, project, template, node, status }[] }
```

## 员工内部工具

本地服务名 `agent-teams`，在员工会话里显示为 `mcp__agent-teams__<tool>`。

### 所有员工

```ts
report(args: { kind: "progress" | "done" | "blocked", text: string }): { ok: true }
// done：任务结束，text 作为结果摘要；blocked：员工去维修台，等人处理
// 汇报进入对话的“工作动态”（折叠），并唤醒统筹

ask(args: { question: string, options?: string[] }): { request: string }
// 不阻塞：问题转给统筹（或按规则转给人），答复作为新消息送回会话
// 需要原地等待答复时，员工用 Claude Code 内置的 AskUserQuestion（钩子会转成提问请求）

remember(args: { content: string, promote?: boolean }): { ok: true, lines: number, limit: number }
// 写员工记忆；promote=true 同时申请提升为项目知识（等统筹确认）
// 超过上限时返回提示，要求员工先整理（见 forget）

forget(args: { content_contains: string }): { removed: number }
// 整理记忆时删掉过时的条目

write_handoff(args: { content: string }): { ok: true, version: number }
// 覆盖当前任务的 HANDOFF.md

permission_prompt(args: { tool_name: string, input: object }): { behavior: "allow" | "deny", message?: string, updatedInput?: object }
// 只给 Claude Code 的 --permission-prompt-tool 用，员工不直接调用：转成权限请求并等待答复
```

### 项目统筹另有

```ts
reply_user(args: { text: string }): { seq: number }
// 统筹说给人听的话只走这个工具。它的普通文字输出只进时间线，不进对话，避免内部思考刷屏

update_plan(args: {
  steps: { title: string, identity: Identity, detail?: string }[]
  request_confirm?: boolean           // 项目 plan_mode=confirm 时强制为 true
}): { plan: { id, version, status } }
// 在对话里发计划卡片；确认后统筹会收到“计划 vN 已确认”的消息

create_task(args: {
  title: string
  brief: string
  require: { identity: Identity } | { template: string } | { employee: string }
  repository?: string                 // 默认项目主仓库
  base_ref?: string                   // 默认项目起点分支，可以是别的员工的交付分支
  depends_on?: string[]               // 任务 id 或 #编号
  plan_step?: number
  priority?: number
}): { task: { id, number, state } }
// 计划处于 pending_confirm 时拒绝创建（返回错误，提示等人确认）

cancel_task(args: { task: string, reason: string }): { ok: true }
message_employee(args: { employee: string, text: string }): { ok: true }
answer_request(args: { request: string, decision?, remember?, message?, answers? }): { ok: true }
// must_human 的请求拒绝代答，返回错误

request_user(args: { text: string, request?: string }): { seq: number }
// 把需要人定的事发进对话（可以附带一个员工请求，显示为对话里的审批卡片）

promote_memory(args: { memory: string, accept: boolean }): { ok: true }
// 处理员工的提升申请

// 另外可用的只读工具：get_project、list_tasks、get_task、list_employees、list_pending、get_messages
```

## 统筹被唤醒的时机

统筹不常驻。以下事件发生时，控制面让统筹员工续跑会话（`session.start` 带 resume），把事件作为消息送进去；处理完一轮、没有新消息时会话停下。

| 事件 | 送给统筹的消息 |
| --- | --- |
| 人在对话里发消息（Web 或 MCP） | 原文 |
| 计划被确认 / 被提修改意见 | “计划 v2 已确认”或意见原文 |
| 员工 `report` | `[cc5 · #1315 · done] …` |
| 员工 `ask` | `[提问 #43 · cc1] …` |
| 员工权限请求（统筹可代答的） | `[权限请求 #42 · qa3] Bash rm -rf build/` |
| 任务因节点离线等原因变为 blocked | 系统通知 |

多条事件在统筹忙时排队，下一轮一次送入。
