# 08 Agent 适配器

每种 agent 一个适配器，放在守护进程里。上层（调度、事件、办公室）只认统一接口和统一事件。以后接 Gemini CLI 等，加一个适配器即可。

## 接口

```ts
interface AgentAdapter {
  kind: "claude_code" | "codex"

  /** 扫描：是否安装、版本、登录状态 */
  detect(): Promise<RuntimeInfo | null>

  /** 读额度；没有额度概念时返回 null */
  quota(): Promise<Quota | null>

  /** 按员工规格写 agent 专属文件（提示词文件、权限、钩子、MCP 配置）、处理目录信任 */
  prepareEmployee(dir: string, spec: EmployeeSpec): Promise<void>

  /** 新开或续跑会话 */
  start(opts: StartOptions): Promise<SessionHandle>
}

interface StartOptions {
  dir: string                     // 员工目录（会话的 cwd）
  env: Record<string, string>     // AGENT_TEAMS_SESSION_TOKEN、GIT_CONFIG_GLOBAL …
  model?: string
  permissionMode: string
  resumeId?: string               // agent_session_id
  initialMessage?: string
  memoryContext: string           // 记忆 + 项目知识 + 交接（给没有钩子的 agent 用）
}

interface SessionHandle {
  agentSessionId: Promise<string>
  events: AsyncIterable<UnifiedEvent>   // 见 03 的统一事件
  send(text: string): Promise<void>     // 运行中追加消息
  interrupt(): Promise<void>            // 打断当前一轮
  stop(mode: "graceful" | "kill"): Promise<void>
  exited: Promise<{ code: number | null, reason: string }>
}
```

守护进程为每个会话建独立进程组，`stop("kill")` 杀整组（包括 agent team 的队友进程），并按进程组统计 CPU / 内存。

## Claude Code

### 扫描与额度

- `detect`：在 `PATH`、`~/.local/bin`、`/usr/local/bin` 找 `claude`，`claude --version` 读版本；登录状态和额度都用 `claude -p /usage --output-format json` 判断（agent-remote-worker 已在用，不消耗额度）。
- 同一节点所有员工共用这个系统用户的 `~/.claude` 登录。

### 员工目录里的文件

| 文件 | 内容 |
| --- | --- |
| `CLAUDE.md` | 模板渲染出的角色和规则 |
| `.claude/settings.json` | `permissions.defaultMode`；钩子：`SessionStart` → `agent-teams-hook session-start`（注入记忆）；`PreToolUse(AskUserQuestion)` → `agent-teams-hook ask`（转成提问请求，原地等待） |
| `.mcp.json` | `agent-teams` 本地 stdio MCP（`agent-teams-mcp`），以及模板的额外 MCP |
| `.claude/skills/` | 模板的技能 |

**目录信任**：Claude Code 只在受信任的目录里运行无人值守会话。守护进程信任 `~/agent-teams/employees/` 一次（写 `~/.claude.json` 的 `projects["<目录>"].hasTrustDialogAccepted`），下面的员工目录继承。限制沿用 agent-remote-worker：该目录不能是主目录、不能在 git 仓库里。员工的代码在 `repos/`、`tasks/` 下的子目录里，会话从员工目录启动，不从 git 检出里启动。

### 运行方式

守护进程持有子进程，用 stream-json 双向通信：

```bash
claude -p \
  --input-format stream-json --output-format stream-json --verbose \
  --permission-mode <mode> \
  --permission-prompt-tool mcp__agent-teams__permission_prompt \
  [--model <model>] [--resume <agent_session_id>]
```

- 第一条用户消息和之后的 `send()` 都写到 stdin（stream-json 的 user 消息）。
- stdout 每行一个 JSON 事件，映射：

| Claude Code 输出 | 统一事件 |
| --- | --- |
| `system` / `init`（含 session_id） | `session.started` |
| `assistant` 消息里的 `text` | `assistant.text` |
| `assistant` 消息里的 `tool_use` | `tool.call`（Task/Agent 工具另发 `subagent.started`） |
| `user` 消息里的 `tool_result` | `tool.result`（对应 Task/Agent 的结果另发 `subagent.finished`） |
| 带 `parent_tool_use_id` 的消息 | 同上，并填 `subagent` 字段 |
| `result` | `turn.finished` + `usage` |

- 需要确认的操作经 `--permission-prompt-tool` 调 `permission_prompt` 工具 → `request.open` → 等答复。已被允许规则或权限模式放行的操作不会走到这里。
- `auto` 模式下被分类器直接拒绝的操作不会产生权限请求，这一点沿用 agent-remote-worker 的结论：想让所有需确认的操作都经过审批就用 `default` 模式。
- `interrupt`：优先用 stream-json 的中断控制消息；不可用时发 SIGINT。

上面的参数组合（尤其是长驻进程 + 多条输入、`--permission-prompt-tool` 与权限模式的配合、中断方式）需要在当前版本上验证，见 [09-mvp.md](09-mvp.md#需要先验证的事)。备选：Claude Agent SDK（TypeScript）的 `query()`，提供同样的流式消息和权限回调。

## Codex

### 扫描与额度

- `detect`：找 `codex`，`codex --version`；登录状态用 `codex login status`（以所用版本为准）。
- 额度：按 API 计费时返回 `null`；用订阅登录时如有可读的用量接口再接入。

### 员工目录里的文件

| 文件 | 内容 |
| --- | --- |
| `AGENTS.md` | 模板渲染出的规则 + 记忆段落（`<!-- agent-teams:memory:start -->`） |

模型、审批策略、沙箱、MCP 服务（`agent-teams-mcp`）由守护进程在启动时以参数 / 配置传入。

### 运行方式

用 `codex app-server`（JSON-RPC over stdio，IDE 插件也用它）：

- 一个 app-server 进程可以承载多个线程（thread）。守护进程每个节点起一个，Codex 员工的会话各是一个线程。
- 新开：创建线程（cwd = 员工目录，带模型、审批策略）→ 开一轮（turn），输入任务说明。
- 续跑：按 thread id 恢复。
- 审批：app-server 向客户端发审批请求（执行命令、改文件），守护进程转成 `request.open`，答复后回给 app-server。
- 事件通知（item / turn 的开始、完成）映射为统一事件。

具体方法名和字段以所用版本的协议为准（可以用 app-server 自带的类型导出生成 TypeScript 定义）。这一节也在待验证清单里。

## 新增一种 agent 要做的事

1. 实现 `AgentAdapter`；
2. 写统一事件映射；
3. 定义它的“记忆注入”方式（钩子或提示词文件段落）；
4. 定义权限请求怎么接进 `request.open`；
5. 在模板的 `agent_kind` 里加上新值。
