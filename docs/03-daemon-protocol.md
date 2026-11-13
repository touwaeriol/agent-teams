# 03 守护进程协议

守护进程主动连控制面：`wss://<控制面>/daemon`。一条连接承载所有消息。类型定义放在 `packages/protocol`，两端共用。

## 接入与认证

1. **注册令牌**：在 Web 上添加节点时生成一次性注册令牌（30 分钟有效）。
   - 安装命令方式：`curl -fsSL https://<控制面>/install.sh | sh -s -- --token <注册令牌>`
   - SSH 方式：控制面用平台部署密钥登录一次，执行同样的安装脚本，然后断开。
2. **安装脚本**：检查 Node.js 与 Git → 下载守护进程到 `~/agent-teams/daemon/` → 安装 systemd 用户服务 `agent-teams-daemon.service`（并 `loginctl enable-linger`）→ 启动。
3. **换取节点凭证**：守护进程首次连接时带注册令牌，控制面返回长期节点凭证，守护进程存到 `~/agent-teams/daemon/credential`（0600），之后都用它。凭证可在 Web 上吊销和轮换。

连接时 `Authorization: Bearer <节点凭证>`。

## 消息格式

```jsonc
{
  "id": "m_01H…",        // 发送方生成，唯一
  "type": "session.start",
  "re": "m_01G…",        // 可选：这是对哪条消息的回复
  "ts": "2026-11-13T14:10:34.120Z",
  "payload": { … }
}
```

- 需要结果的指令用请求/响应：响应带 `re`，`type` 为 `ok` 或 `error`（`payload: {code, message}`）。
- **事件可靠投递**：守护进程上报的会话事件带节点内递增的 `seq`，控制面落库后批量回 `ack {upto}`。守护进程把未确认的事件写在本地队列文件里，断线重连后从 `hello.acked_seq` 之后补发。控制面按 `(node_id, seq)` 去重。
- 指令幂等：控制面下发的指令带 `op_id`，守护进程记住最近执行过的 `op_id`，重复收到直接回上次的结果。

## 守护进程 → 控制面

| type | payload | 说明 |
| --- | --- | --- |
| `hello` | `{daemon_version, acked_seq, sessions: [{session_id, employee_id, state, agent_session_id}]}` | 每次连上第一条。带上本机仍在跑的会话，控制面据此对账 |
| `heartbeat` | `{resources, sessions: [{session_id, state, context_pct, subagents_active}]}` | 默认每 10 秒 |
| `scan.result` | `{runtimes: [{kind, path, version, logged_in, account_label, quota}]}` | 首次连接、收到 `scan`、每 30 分钟 |
| `employee.synced` | `{employee_id, template_version, ok, error?}` | 员工目录建好或更新完 |
| `employee.removed` | `{employee_id, ok, error?}` | 有未推送改动时 `ok:false, error:"unpushed"` |
| `session.event` | `{seq, session_id, event}` | 统一事件，见下 |
| `session.state` | `{seq, session_id, state, agent_session_id?, end_reason?}` | 会话状态变化 |
| `tool.call` | `{session_token, tool, args}` → 响应 `{result}` | 员工本地 MCP 工具的调用转发（report / remember / create_task …），见 [04-mcp.md](04-mcp.md#员工内部工具) |
| `request.open` | `{session_token, kind, payload}` → 响应 `{request_id}` | 权限请求 / 提问，会话原地等待 |
| `credential.get` | `{session_token, host, path, operation}` → 响应 `{username, password, expires_at}` 或 error | Git 凭证 helper 转来的请求 |

## 控制面 → 守护进程

| type | payload | 说明 |
| --- | --- | --- |
| `welcome` | `{node_id, heartbeat_sec, scan_interval_sec}` | 回 `hello` |
| `scan` | `{}` | 立即重扫运行时 |
| `employee.upsert` | 员工规格，见下 | 建目录或更新配置、记忆、项目知识 |
| `employee.remove` | `{employee_id, force}` | 回收目录 |
| `session.start` | 见下 | 启动或续跑会话 |
| `session.send` | `{session_id, text}` | 给运行中的会话发消息（人、统筹、系统提醒） |
| `session.interrupt` | `{session_id}` | 打断当前这一轮，会话保留 |
| `session.stop` | `{session_id, mode: "graceful" \| "kill"}` | 停止会话（kill 杀整个进程组） |
| `request.answer` | `{request_id, answer}` | 答复后守护进程让会话继续 |
| `memory.sync` | `{employee_id, memory_md, knowledge_md}` | 记忆或项目知识变化时推送 |
| `daemon.upgrade` | `{version, url, sha256}` | 下载、校验、替换、重启（运行中的会话不受影响，重启后重新接管） |

### 员工规格（employee.upsert）

```jsonc
{
  "employee_id": "…",
  "dir": "~/agent-teams/employees/main/cc5",
  "agent_kind": "claude_code",
  "template_version": 7,
  "files": {                       // 守护进程原样写入，路径相对员工目录
    "CLAUDE.md": "…",              // Codex 员工为 AGENTS.md
    ".claude/settings.json": "…",  // 权限模式、钩子
    ".mcp.json": "…",              // agent-teams 本地 MCP + 模板额外的 MCP
    ".claude/skills/…": "…",
    "employee.json": "…"           // id、项目、模板、名字，扫描时用来认出员工
  },
  "git": { "name": "cc5", "email": "cc5@agents.local" },
  "memory_md": "…",                // 渲染好的 memory/MEMORY.md
  "knowledge_md": "…",             // 渲染好的项目知识
  "repositories": [{ "id": "…", "clone_url": "https://github.com/your-org/main-app.git", "dir": "repos/main-app" }]
}
```

守护进程还会写 `.gitconfig`（提交者、`credential.helper`、`credential.useHttpPath=true`），会话启动时用 `GIT_CONFIG_GLOBAL` 指向它。

### 启动会话（session.start）

```jsonc
{
  "session_id": "…",
  "session_token": "…",             // 只放进该会话进程的环境变量
  "employee_id": "…",
  "runtime": "claude_code",
  "resume": { "agent_session_id": "7f3c…a91" },   // 续跑时有
  "model": null,
  "permission_mode": "auto",
  "task": {                          // 统筹会话可以没有
    "id": "…", "number": 1315,
    "repository_dir": "repos/main-app",
    "base_ref": "origin/main",
    "worktree": "tasks/1315/main-app",
    "deliver_branch": "emp/cc5/1315-staging"
  },
  "handoff_md": "…",                 // 有交接时随启动下发
  "initial_message": "…"             // 任务说明，或统筹收到的新消息
}
```

守护进程启动顺序：加员工锁 → 写入最新记忆 / 交接 → fetch 并建 worktree（凭证走 helper）→ 按适配器启动进程 → 回 `ok` → 开始上报事件。

## 统一事件

适配器把各家输出转成下面这些类型（`session.event.event`）。办公室动画、时间线、统计都只看统一事件。

| type | data | 来源举例 |
| --- | --- | --- |
| `session.started` | `{agent_session_id, model}` | Claude `system/init`；Codex thread started |
| `assistant.text` | `{text, subagent?}` | 助手文字（只进时间线，不进对话） |
| `tool.call` | `{call_id, tool, summary, subagent?}` | `tool_use`；`summary` 是给人看的简短描述，如 `Bash sed -n 369,412p docs/…` |
| `tool.result` | `{call_id, ok, summary?}` | `tool_result` |
| `subagent.started` / `subagent.finished` | `{subagent, description}` | Task/Agent 工具的开始结束（按 parent_tool_use_id 归属） |
| `usage` | `{input_tokens, output_tokens, context_pct}` | 每轮结束 |
| `turn.finished` | `{status: "ok" \| "error", summary?}` | Claude `result`；Codex turn completed |
| `session.error` | `{message}` | 进程异常、续跑失败 |

`subagent` 字段让控制面把子代理的动作记在员工名下，同时能显示“×3 子代理在并行”。

## 本机接口（守护进程提供）

| 接口 | 使用者 | 说明 |
| --- | --- | --- |
| `~/agent-teams/daemon/run/daemon.sock` | MCP stdio 进程、Git helper、钩子 | 本机 unix socket（0600），按 `session_token` 识别员工，转发到控制面 |
| `agent-teams-mcp`（stdio） | 员工会话 | 员工的 `agent-teams` MCP 服务，工具调用 → daemon.sock → `tool.call` |
| `git-credential-agent-teams` | git | git 凭证 helper 协议 → daemon.sock → `credential.get` |
| `agent-teams-hook <event>` | Claude Code 钩子 | SessionStart 输出记忆注入；PreToolUse 拦截 AskUserQuestion → `request.open` |

## 断线与对账

- 守护进程断线：会话继续运行；事件存本地队列；员工发起的请求（`request.open` / `credential.get` / `tool.call`）立即返回“控制面不在线”：权限请求按拒绝处理，提问让员工自行决定并在汇报里说明（沿用 agent-remote-worker 的规则）。
- 重连后：`hello.sessions` 和控制面记录对账。控制面认为在跑、节点上没有的会话标为 `failed(node_lost)`；节点上有、控制面不认识的会话让守护进程停止。之后补发未确认事件，控制面重推仍在等待的请求状态。
- 控制面 60 秒收不到心跳把节点标为 `offline`，员工在办公室灰显；不主动重派任务（会话可能还在跑），超过 30 分钟仍离线时把该节点的运行中任务标为 `blocked`，由人决定重派。
