# 控制台 ↔ 节点守护进程协议 v2

适用：Go 控制台（`/server/internal/nodegw`）与 TS 守护进程（`/node/src/link`）。消息 Schema 的唯一来源是 `/protocol/schemas/node/*.json`，本文是其说明。只有协议 2：v1 的协议 1 和 Python 守护进程已随 v1 退役，控制台对不支持协议 2 的 hello 以 4426 关闭。

## 1. 连接

- URL：`wss://<console>/nodes/connect`；`/nodes/v2/connect` 是同一入口的别名（两者都只接受协议 2）。
- 认证：`Authorization: Bearer <node token>`。控制台 `sha256(token)` 查 `at_nodes.token_hash`，所有者必须存在且 `active`，否则以 **4401** 关闭（守护进程视为“令牌无效”，每 10 分钟重试一次并在日志里提示）。
- 帧：WebSocket 文本帧，一帧一个 JSON 对象（UTF-8）。单帧上限 4 MiB；更大的内容（例如很长的命令输出）由发送方截断并标 `truncated: true`。启用 permessage-deflate。
- 心跳：双方每 20 s 发 WebSocket ping；45 s 收不到任何帧则断开重连。
- 重连：指数退避 1 s → 30 s，带 ±20% 抖动。
- 同一节点只允许一条链路：新链路成功握手后，旧链路以 **4409**（replaced）关闭。

## 2. 信封

每条消息：

```json
{
  "v": 2,
  "type": "session.send",
  "id": "c_01JD3W…",          // 消息 id：控制台发出的用 c_ 前缀 ULID，守护进程发出的用 n_ 前缀 ULID
  "seq": 1842,                 // 仅“可靠”消息有：发送方在本节点范围内单调递增
  "ts": "2026-11-14T12:00:00.123Z",
  "trace": "00-…-01",          // 可选，W3C traceparent
  "body": { … }                // 各类型的字段
}
```

### 2.1 可靠与不可靠

| 方向 | 可靠（有 `seq`，需 ACK，可重放） | 不可靠（无 `seq`，丢了无所谓） |
| --- | --- | --- |
| 控制台 → 守护进程 | 所有命令（`session.*`、`approval.resolve`、`tool.result`、`git.*`、`cred.response`、`node.*`（`node.usage_refresh` 除外）、`run.*`） | `ack`、`pong` 类、`node.usage_refresh` |
| 守护进程 → 控制台 | 会话事件、命令结果、审批请求/取消、工具调用、git 结果 | `ack`、`stream.delta`（逐字输出，可选）、`log`、`node.stats`、`node.usage` |

- **ACK**：`{type:"ack", body:{seq: N}}` 表示“seq ≤ N 的可靠消息我已持久处理”。接收方至少每 200 ms 或每 50 条发一次累计 ACK。
- **守护进程发件箱**：可靠消息先追加写入 `state/outbox/<epoch>.jsonl` 再发送；收到 ACK 后推进游标，游标之前的文件可删除。守护进程重启后 `seq` 从文件里的最大值继续（`epoch` 只在状态目录被清空时变化）。
- **控制台发件箱**：可靠命令写入 `at_node_commands(node_id, seq, …, status, expires_at)`；收到 ACK 标记 `acked`。重连后**只重发**同时满足以下条件的命令：`seq > hello.acked_cmd_seq`、`status IN ('pending','sent')`、`expires_at > now()`、没有被标为 `superseded`。不会把节点的整段命令历史重放一遍。
  - `expires_at`：按类型设定，`session.send`/`session.interrupt`/`session.set_mode` 10 分钟，`approval.resolve`/`tool.result` 24 h，`session.open` 与 `git.*` 30 分钟；过期的命令标 `expired`，由对应引擎决定是否重新生成（例如 orchestrator 重新算出“该有会话”后发新的 `session.open`）。
  - `superseded`：会话的 `open_gen` 前进、会话关闭、讨论串离开 `running/waiting`、项目删除时，控制台把该会话/讨论串所有未 ACK 的命令标为 `superseded`。
  - **纪元变化**（hello 的 `epoch` 与 `at_nodes.epoch` 不同，即守护进程状态目录被清空）：控制台把该节点所有未 ACK 的命令标 `expired`，`ack_seq` 归零，**不重发任何旧命令**；等 `sessions.report` 到达后由 orchestrator 按“库里应有的状态 vs 节点实际状态”重新生成需要的命令（新的 `session.open {resume}`、`git.prepare` 等）。

### 2.2 幂等

- 每个命令带业务幂等键（下表的“键”列）。守护进程维护 `键 → 结果` 表（`state/done.jsonl`），保留期 = 48 h，长于任何命令的 `expires_at`，所以在有效期内重发的命令一定能被去重；重复命令直接重发原结果，不再执行。
- 守护进程的每个上行可靠消息也有业务键（例如 `approval_id`、`(session_id, call_id)`、`(session_id, event_no)`），控制台按键去重写库（`ON CONFLICT DO NOTHING`）。
- 语义是 **至少一次投递，处理器幂等**；不保证恰好一次。仍可能重复的窗口：守护进程把消息推进 SDK/Codex 之后、写 `done.jsonl` 之前崩溃。各处理器因此自己做幂等：
  - `session.send`：Claude 的 `SDKUserMessage.uuid = msg_id`；守护进程重启后第一次 resume 一个会话时，读该会话转录里已有的 user 消息 uuid，命中的 `msg_id` 直接回 `session.delivered` 不再推送。Codex 用 `thread/read`（或 rollout）里已有的用户输入比对 `msg_id` 标记（消息正文前加不可见的 `<!-- m_… -->` 前缀）。
  - `session.open`：键 `session_id + open_gen`，本机已有同 gen 的活会话就直接回 `session.opened`。
  - `git.*`：操作本身幂等（worktree 已存在、分支已删除都视为成功）。
  - `approval.resolve`/`tool.result`：对应 Promise 已不存在时回 `stale: true`，不报错。

### 2.3 归属校验

守护进程上行的每条消息只要提到 `session_id` 或 `job_id`（`session.*`、`tool.call`、`tool.cancel`、`approval.request`、`approval.cancel`、`approval.ack`、`cred.request`、`git.result`、`sessions.report` 里的每一项），控制台都先核对：

- 会话：`at_agent_sessions.node_id = 当前链路认证出的节点`，会话未关闭，消息里的 `open_gen`（有则）等于当前 `open_gen`，且会话所属项目的所有者 = 节点所有者。
- 作业：`at_node_commands.node_id = 当前节点` 且该作业由控制台发出。

任何一项不符 → 丢弃该消息、回 `error{ref_id, code:'unknown_session'}`（作业为 `unknown_job`）、写 `at_audit_log {actor: node:<id>, action: 'node_message_rejected'}`；同一链路 1 分钟内超过 20 次则以 4401 关闭。这样拿到别的会话 id 的节点（从日志、界面或泄露）也无法以那个员工的身份要令牌、调工具或发审批。

## 3. 握手与版本协商

```
守护进程                                        控制台
  ├─ hello ───────────────────────────────────►│ 校验、选协议版本、记录节点信息
  │◄──────────────────────────────── welcome ──┤
  ├─ （重放发件箱中 seq > welcome.ack_seq 的消息）►│
  │◄──（重发 at_node_commands 中未 ACK 的命令）──┤
  ├─ sessions.report（本机现存会话清单）───────►│ orchestrator 对账
```

### `hello`（守护进程 → 控制台，第一帧，不可靠）

```json
{
  "protocols": [2, 1],                    // 支持的协议版本，优先级从高到低
  "daemon": {"version": "2.0.3", "build": "a1b2c3d", "node": "22.12.0", "os": "linux", "arch": "x64"},
  "epoch": "01JD3V…",                     // 发件箱纪元
  "last_seq": 1841,                        // 本纪元已发出的最大 seq
  "acked_cmd_seq": 977,                    // 已执行并持久化的控制台命令最大 seq
  "host": "rc-erio", "root": "/home/u/.agents_team",
  "capacity": {"max_active_sessions": 4, "cpus": 16, "mem_mb": 64000},
  "runtimes": {
    "claude": {"sdk": "0.3.325", "cli": "2.1.325", "auth": "subscription|api_key|none"},
    "codex":  {"cli": "0.169.0", "auth": "api_key|chatgpt|none", "sandbox_ok": true}
  },
  "features": ["sessions", "codex_app_server", "git_worktree", "git_sync", "git_review_sha", "cred_helper", "upgrade", "context_usage", "session_compact"],
  "isolated": false,
  "isolation": "agent_user|sandbox|none"     // architecture.md §10.3，守护进程启动时探测
}
```

`features` 含 `usage` 表示守护进程会发 `node.usage`，也能处理 `node.usage_refresh`（§9.1）。

`auth` 由守护进程启动时探测（Claude：`query().accountInfo()` 一次后关闭；Codex：app-server `account/read`），不上报邮箱等个人信息。

### `welcome`（控制台 → 守护进程，不可靠）

```json
{
  "protocol": 2,
  "node_id": "4f0c…",
  "ack_seq": 1838,                 // 控制台已持久处理的守护进程消息最大 seq（同 epoch；epoch 变了则为 0）
  "server_time": "…",
  "config": {"park_after_s": 900, "max_active_sessions": 4, "log_level": "info"},
  "min_daemon": "2.0.0",           // 低于此版本：控制台随后发 node.upgrade 或以 4426 关闭
  "features": ["sessions", "codex_app_server", "git_worktree", "cred_helper"]   // 双方交集
}
```

- 协议版本：取双方 `protocols` 交集的最大值。交集为空 → 4426（upgrade required）关闭。
- 新增字段一律可选；接收方忽略未知字段。新增消息类型必须挂在某个 `features` 名下，只在交集包含它时发送。破坏性变化才升 `protocol` 主版本。

### `sessions.report`（守护进程 → 控制台，可靠）

```json
{"sessions": [{"session_id": "s_…", "open_gen": 3, "state": "active|parked|turn_running|closed", "kind": "claude|codex",
               "sdk_session_id": "…", "codex_thread_id": null, "pending_approvals": ["ap_…"], "pending_tool_calls": ["tc_…"]}]}
```

控制台对账（每次重连和每次纪元变化后）：

- 库里应开着但节点上没有的 → 重新 `session.open {resume}`（新 `open_gen`）；节点上有但库里已关闭或 `open_gen` 落后的 → `session.close`。
- **审批与阻塞调用**：库里该会话 open 的审批，`approval_id` 不在 `pending_approvals` 里的 → 标 `cancelled`（卡片显示“已失效”）；`running/waiting` 的工具调用不在 `pending_tool_calls` 里的 → 标 `cancelled`，对应的阻塞型提问保持 open，答复到达时按非阻塞方式用 `session.send {kind:"answer"}` 送达。收到 `session.state crashed|parked|closed` 时对该会话做同样处理（parked 时本应没有挂起项，有则同样取消）。

## 4. 会话

会话 id `session_id` 由控制台生成（`s_` + ULID），对应 `at_agent_sessions.id`。

### 4.1 `session.open`（控制台 → 守护进程，键：`session_id + open_gen`）

```json
{
  "session_id": "s_01JD…",
  "open_gen": 3,                                  // 第几次打开（每次 resume 加 1）
  "employee": {"name": "dev-2", "role": "dev", "tier": "member", "project_id": "…", "thread_no": 12, "scope": "project"},
  "runtime": "claude",                             // claude | codex
  "cwd": "projects/7a1c…/threads/12/dev-2",       // 相对 root
  "prepare": "g_01JD…",                            // 可选：依赖的 git.prepare 命令键，守护进程先确保它已成功
  "prepare_jobs": ["g_01JD…", "g_01JE…"],           // 可选：每个仓库一个 git.prepare，全部成功才开会话（prepare = 第一个，给旧守护进程）
  "resume": {"sdk_session_id": "8d2f…"},           // 或 {"codex_thread_id": "…"}；首次打开为 null
  "claude": {                                      // runtime=claude 时
    "model": "claude-opus-5-5", "effort": null,
    "system_append": "…",
    "tools": ["Read","Glob","Grep","Edit","Write","Bash"],   // 传给 SDK options.tools；[] 表示无内置工具
    "allowed_tools": ["mcp__platform__*"],
    "disallowed_tools": [],
    "permission_mode": "default",                  // 总是显式
    "setting_sources": [],                         // 所有层级固定为 []（architecture.md §6.1）
    "max_turns": null, "max_budget_usd": null,
    "read_only": false,                            // 组长与 read_only 岗位 = true：启用下面的 bash_policy、沙箱无可写路径
    "bash_policy": {"mode": "argv", "allow": ["git log", "git diff", "git show", "git status", "git rev-parse", "git ls-files",
                    "git blame", "git merge-base", "git branch:list", "gh pr view", "gh pr diff", "gh pr checks"],
                    "deny_flags": ["--output", "-o", "--ext-diff", "--textconv", "-c", "--exec-path", "--git-dir", "--work-tree"],
                    "deny_metachar": true},        // 仅 read_only 会话；argv 解析，不是正则
    "sandbox": {"enabled": true, "write_paths": ["."], "extra_write_paths": ["<shared>/.git"], "deny_read": ["…"]},  // 按节点隔离级别生成（§10.3）；read_only 时 write_paths = []
    "deny_non_platform_tools": false,              // 统筹 = true（PreToolUse 钩子拒绝非 mcp__platform__*）
    "protected_refs": ["thread/12-employee-comms", "main"]  // 写进平台 pre-push 钩子（纵深防护；员工本就没有写令牌）
  },
  "codex": null,                                   // runtime=codex 时：{"model","sandbox","approval_policy","developer_instructions","config":{…}}（model 可为 ""：节点不传给 thread/start，用 Codex 自己的默认，init.model 报实际解析出的模型）；
                                                   // 成员 config 含 sandbox_workspace_write.writable_roots=[<shared>/.git]、network_access=true；
                                                   // mcp_servers.platform 由守护进程每次 start/resume 自己填（URL 路径秘密，architecture.md §6.3）
  "platform_tools": [ {"name": "complete_task", "description": "…", "input_schema": {…}, "blocking": false}, … ],
  "env": {"LANG": "C.UTF-8"},                      // 额外的非秘密环境；守护进程自己加 PATH/HOME/MCP_TOOL_TIMEOUT/git 凭证变量
  "git_credentials": true,                         // 是否装 credential helper 与 gh 包装
  "skills": [{"name": "design", "files": [{"path": "SKILL.md", "content": "…"}]}],  // 可选：岗位的技能（architecture.md §6.5）
  "plugins": [{"name": "tinyplug", "version": 2, "files": [{"path": ".claude-plugin/plugin.json", "content": "…"}], "skills": ["tinyplug:greet"], "mcp_servers": {}}],  // 可选：岗位的插件，确切版本（architecture.md §6.5）
  "first_message": {"msg_id": "m_…", "text": "…"}, // 可选：工具核对通过、收到 session.release 后才发送
  "context": {"limit_tokens": 600000, "in_task_pct": 80, "focus": "保留当前任务 id、分支…"}  // 可选：岗位的上下文管理（§4.10）
}
```

守护进程行为：

1. 校验 `cwd` 在 root 内；`prepare_jobs`（没有时 `prepare`）指定的 git 操作逐个等待成功（每个最多 10 分钟），任何一个失败就打开失败。
2. Claude：构造 `options`（`env` 逐项构造、`permissionMode` 显式、`settingSources: []`、`allowedTools`（含控制台由“总是允许”规则展开的项）、`settings`（沙箱与拒绝规则）、`canUseTool`、`hooks`、`mcpServers.platform = createSdkMcpServer(...)`、`includeHookEvents: true`、`resume`），用一个异步队列作为 `prompt` 调 `query()`，随即 `await q.initializationResult()` 拿工具列表和 MCP 状态（不等第一个回合的 `system/init`）。Codex：确保本机 app-server 在跑，`thread/start` 或 `thread/resume`（带新的 `mcp_servers.platform` 配置），工具列表取自 `mcpServerStatus/list` + 沙箱配置。
3. 回 `session.opened`。`first_message` 先扣住，收到 `session.release {session_id, open_gen}` 才推送。
4. `skills`（只有 Claude 会话会收到）：写成本地插件 `<root>/agent-skills/<project>/<thread_no 或 project>/<employee>/`（`employee.scope = "project"`——统筹和项目共用的成员——用 `project`，不随会话所在的讨论串变；`thread` 用讨论串号；旧控制台不带 scope 时 thread_no 为空就是 `project`）（每次打开重建，不在 worktree 里），`options.plugins = [{type:'local', path, skipMcpDiscovery:true}]`、`options.skills = ['agent-teams-skills:<name>', …]`；`Skill` 工具由控制台放进 `tools`/`allowed_tools`。
4a. `plugins`：每个写到 `<root>/agent-plugins/<project>/<thread_no 或 project>/<employee>/<name>@<version>/`（目录规则同上）（每次打开整目录重建，不在 worktree 里；统筹忽略）。Claude：`options.plugins` 追加一个 local 插件项（skipMcpDiscovery 为 true），`options.skills` 追加 bundle 的 `skills`，`mcp_servers` 里的 `${CLAUDE_PLUGIN_ROOT}` 换成插件目录后以 `plugin_<plugin>_<server>` 放进 `options.mcpServers`（`strictMcpConfig` 不变）。Codex：只把 `mcp_servers` 的 stdio/http 服务器并进 thread 配置的 `mcp_servers`。
5. `resume` 失败（转录不存在、thread 找不到）：不自动开新会话，回 `session.opened {resume_failed: true, error}`，由控制台走交接流程（architecture.md §6.2）。停放的会话被 `session.send` 唤醒、或崩溃后自动重启时 resume 失败，同样回这一条（`open_gen` 不变）并删掉本机记录；此前**不发** `session.state closed`。
6. Claude 的 `AskUserQuestion`：PreToolUse 钩子（`matcher: AskUserQuestion`）发 `tool.call ask_user {questions, via: "AskUserQuestion"}`（`tool_use_id` 是这次 AskUserQuestion 的），等 `tool.result` 的 `structured.answers`，以 `allow` + `updatedInput.answers` 交回 CLI（architecture.md §6.6）。

### 4.2 `session.opened`（守护进程 → 控制台，可靠，键：`session_id + open_gen`）

```json
{"session_id": "s_…", "open_gen": 3, "sdk_session_id": "8d2f…", "codex_thread_id": null,
 "init": {"model": "claude-opus-5-5", "tools": ["Read", "…", "mcp__platform__complete_task"], "permission_mode": "default",
          "claude_code_version": "2.1.325", "mcp_servers": [{"name": "platform", "status": "connected"}],
          "commands": [{"name": "agent-teams-skills:design", "description": "(agent-teams-skills) Make HTML prototypes…"}]}}
```

`init` 来自 `initializationResult()`（Codex 见上）。`init.commands` 是会话自己的斜杠命令（岗位技能、插件的技能和命令；`initializationResult().commands` 去掉 `builtin` 的 Claude Code 自带命令），控制台存在会话的 `init` 里，输入框的 “/” 列表用它（`api.md` §3.2）。用户消息以 `/名字` 开头时守护进程原样作为提示推给会话，CLI 当斜杠命令执行；`名字` 不带插件前缀且恰好对应会话的一个命令（如 `design` → `agent-teams-skills:design`）时先补全，免得落到 CLI 自带的同名命令（CLI 有自己的 `/design`）。带附件时文字块排在最前，CLI 才认得出命令（SDK 0.3.325 实测）。控制台用 `init.tools` 做角色工具核对（`architecture.md` §10.2）：通过 → 发 `session.release`（可靠，键 `session_id + open_gen`）；不符 → `session.close {reason:"policy_violation"}`。之后每个回合的 `system/init`（`turn_started` 事件带 `tools`）再核对一次，不符同样关闭。

### 4.3 `session.send`（控制台 → 守护进程，键：`msg_id`）

```json
{"session_id": "s_…", "msg_id": "m_01JD…", "text": "…", "priority": "now|next|later", "kind": "user|task|mail|answer|system",
 "interrupt_first": false, "images": [{"id": "img_…", "media_type": "image/png", "name": "截图.png", "size": 48213}]}
```

- `images`（可选，最多 8 张；`first_message` 也可以带）：用户附的图片。帧里只有引用（帧上限 4 MiB，图片单张可到 5 MB）；守护进程投递前用自己的节点令牌 `GET <控制台>/nodes/attachments/<id>`（`Authorization: Bearer <节点令牌>`，只能取同一用户项目的图片）下载到 `<state>/attachments/<session_id>/`，会话关闭时删掉。Claude：用户消息的 `content` 变成 `[{type:"image", source:{type:"base64", media_type, data}}…, {type:"text", text}]`（Agent SDK 0.3.325 的 `SDKUserMessage.message` 就是 Messages API 的 `MessageParam`，支持图片块）。Codex：在 `turn/start` / `turn/steer` 的 `input` 里追加一项 `localImage`（带本机 `path`）。`media_type` 为 `application/pdf` 时 Claude 收到一个 `document` 内容块（`source` 是 base64 的 `application/pdf`），Codex 没有文档输入，文字末尾写明 PDF 存在本机哪个路径。下载失败时照样投递文字，末尾加一行说明图片没取到。

- 会话处于 `parked`：守护进程先用 `resume` 重开进程（不需要控制台再发 open），再投递。
- 会话还没收到本 `open_gen` 的 `session.release`：消息在守护进程排队，release 后按顺序投递（park 后自动 resume 的同一 gen 不需要再 release，但 init 工具若与上次不同则当作违规上报 `session.event{kind:error, code:'protocol'}`）。
- Claude：向 prompt 队列推 `SDKUserMessage {priority}`。Codex：空闲 `turn/start`，回合中 `turn/steer {expectedTurnId}`（`later` 则排到 `turn/completed` 之后）。
- `interrupt_first: true`：先 `interrupt()` / `turn/interrupt`，再投递。
- `anchor`（可选，≤ 8000 字；`session.open.first_message` 也可以带）：任务锚点（任务 id、原始说明、分支、交接说明、最近进度），随任务简报下发。守护进程保存最新的一条（空串清掉），会话在任务中被运行时自己压缩后补给模型（§4.10）。控制台在成员手上没有任务时给它的消息带 `anchor: ""`；`complete_task` 的 `tool.result` 也带 `anchor: ""`。会话（重新）打开而首条消息不带锚点时（崩溃重开、守护进程重启后重开），`session.open.anchor` 带成员手上任务的锚点，新进程从它开始。
- 回 `session.delivered {session_id, msg_id, turn_id?}`（可靠）。

### 4.4 `session.interrupt`（键：`id`）

`{"session_id": "s_…", "reason": "user_stop|thread_paused|superseded"}` → Claude `q.interrupt()`，Codex `turn/interrupt`。回 `session.interrupted {session_id, queued_msg_ids?: […]}`（SDK 的 `interrupt_receipt_v1` 能力返回仍在排队的消息 uuid，映射回 msg_id）。

### 4.5 `session.set_mode`（键：`id`）

`{"session_id": "s_…", "permission_mode": "default|acceptEdits|bypassPermissions|auto", "model": null}` → Claude `q.setPermissionMode()` / `q.setModel()`；Codex 在下一次 `turn/start` 带 `approvalPolicy` / `model`。只带 `model` 的命令来自 /model（`PUT /api/conversations/{cid}/model`，只发给成员；统筹和组长固定用默认模型，不能切换）：运行中的会话从下一轮起换模型，守护进程同时改掉本机记录里的模型，之后 resume 也用新模型。`bypassPermissions` 只有会话以 `allowDangerouslySkipPermissions` 打开时才允许（守护进程否则回错误）。控制台不再发 `auto`（枚举里保留给旧控制台）。`bypassPermissions` 会话不发 `approval.request`：CLI 仍调到 `canUseTool` 时守护进程就地放行；会话打开时 `permission_mode` 是 `bypassPermissions` 的，守护进程总是带上 `allowDangerouslySkipPermissions`。

### 4.6 `session.close`（键：`session_id + open_gen`）

`{"session_id": "s_…", "reason": "thread_done|employee_removed|policy_violation|node_drain|switch_thread|context_clear", "forget": false}`（`context_clear`：同一讨论串里做满 `clear_after_tasks` 个任务或上下文太满，下一个任务开新会话；只发给有 `session_compact` 的守护进程，旧的收到 `switch_thread`） → Claude `q.close()`；Codex 不再路由该 thread（app-server 无需关闭）。`forget: true` 同时删除本机会话记录（SDK 转录文件保留，用于排查，按 7 天清理）。回 `session.closed`。

### 4.7 停放（守护进程自主）

空闲超过 `welcome.config.park_after_s` 且无运行中回合、无待审批、无待工具调用 → `q.close()`，发 `session.state {session_id, state:"parked"}`。下一次 `session.send` 自动 resume。设了 `max_active_sessions`（不设 = 不限）且活跃会话数达到它时，新 `session.send` 进入守护进程的等待队列，并发 `session.state {state:"queued"}`（办公室显示“排队等工位”）；需要时优先停放最久空闲的会话腾出位置。

### 4.8 会话事件（守护进程 → 控制台，可靠，键：`session_id + event_no`）

```json
{"type": "session.event", "body": {"session_id": "s_…", "event_no": 5531, "turn_id": "t_…",
  "kind": "tool_started", "tool": "Bash", "category": "shell", "subcategory": "test",
  "tool_use_id": "toolu_…", "summary": "pytest -q tests/test_comms.py", "at": "…"}}
```

| `kind` | 额外字段 | 来源（Claude / Codex） |
| --- | --- | --- |
| `turn_started` | `trigger_msg_ids[]`, `tools[]`（本回合 init 的工具列表，控制台再核对） | `system/init` / `turn/started` |
| `text` | `text`（助手文本，截断 8 KB）, `final: bool` | `assistant` text 块 / `agentMessage` |
| `thinking` | — | `assistant` thinking 块（不带内容） |
| `tool_started` | `tool`, `category`, `subcategory?`, `tool_use_id`, `summary` | PreToolUse 钩子 / `item/started` |
| `tool_ended` | `tool_use_id`, `ok`, `duration_ms`, `summary?` | PostToolUse(+Failure) / `item/completed` |
| `turn_ended` | `result_subtype`, `is_error`, `num_turns`, `duration_ms`, `cost_usd?`, `usage{}`, `model?`, `context?` | `result` / `turn/completed`（`model`：Claude 取本轮最后一条主代理 assistant 消息的模型，Codex 取本轮用的模型；`context {used_tokens, max_tokens}`：`context_usage` 守护进程填，Claude 取 `getContextUsage({detail:"summary"})` 的 `totalTokens` / `maxTokens`，Codex 取最近一次 `thread/tokenUsage/updated` 的 `last.totalTokens` / `modelContextWindow`） |
| `compacting` | `on: bool` | `system/status`、`compact_boundary` / `contextCompaction` 条目、`thread/compacted` |
| `rate_limited` | `retry_at?`, `detail` | `rate_limit_event`, `api_retry` / 错误通知 |
| `subagent` | `on: bool`, `agent_type` | SubagentStart/Stop / — |
| `notification` | `message`, `notification_type` | Notification 钩子 |
| `error` | `code`（`auth_failed|billing|crashed|spawn_failed|protocol`）, `message`, `fatal: bool` | `assistant.error`、进程退出、JSON-RPC 错误 |
| `todos` | `todos[]`（`{content, status: pending|in_progress|completed, active_form?}`，整张清单的最新状态，最多 100 项） | PreToolUse 钩子看到 `TodoWrite` / — |
| `plan` | `plan`（Markdown，截断 32 KB） | PostToolUse 看到 `ExitPlanMode`（`tool_response.plan` 或配置目录下的计划文件） / — |

`summary` 由守护进程生成且不含秘密（Bash 命令截断 200 字符、按正则抹掉 `ghs_…`/`ghp_…`/`sk-…` 形式的串）。

另有不可靠的 `stream.delta {session_id, turn_id, text}`：只在网页有人打开“实时输出”时由控制台发 `session.watch {session_id, on}` 开启，用于逐字显示，不落库。

### 4.9 `session.state`（守护进程 → 控制台，可靠）

`{"session_id", "state": "starting|active|turn_running|parked|queued|closed|crashed", "detail"?}`。崩溃（子进程意外退出）时守护进程自动以 `resume` 重开最多 3 次（间隔 5/15/45 s），仍失败则 `crashed` + `session.event{kind:error, fatal:true}`，由控制台决定（员工 → `broken`）。

### 4.10 上下文管理（`hello.features` 含 `context_usage`、`session_compact`）

设计见 `shared-members.md` §5。百分比都相对岗位的 `context_limit`（不是模型窗口）。

- **任务中自动压缩**：`session.open.context {limit_tokens?, in_task_pct?, window_tokens?, focus?}`，`window_tokens` 是这个员工上一个同运行时会话报的窗口（`turn_ended.context.max_tokens`，没有就不带）。Claude：有 `limit_tokens` 时 `CLAUDE_CODE_AUTO_COMPACT_WINDOW = clamp(limit_tokens, 100K, 1M)`，`CLAUDE_AUTOCOMPACT_PCT_OVERRIDE = limit_tokens × in_task_pct / (窗口 − 20K)`（CLI 的阈值是有效窗口 = 窗口 − 20K 的百分比，SDK 0.3.325 实测；CLI 自己再把窗口夹到模型窗口，并且最晚在有效窗口 − 13K 压缩）；没有 `limit_tokens` 时不设窗口，有 `window_tokens` 就把 PCT 换算成 `window_tokens × in_task_pct` 个 token（`in_task_pct × W / (W − 20K)`），都没有就直接用 `in_task_pct`（这时是 CLI 有效窗口 = 窗口 − 20K 的百分比）。Codex：`thread/start` 配置 `model_auto_compact_token_limit = L × in_task_pct / 100`，`L = min(limit_tokens, window_tokens)`（上限超过模型窗口时永远到不了），没有 `limit_tokens` 时 `L = window_tokens`，两者都没有就不设（Codex 自己的默认）。
- **平台主动压缩** `session.compact {session_id, open_gen, compact_id, instructions?}`（可靠，键 `compact_id`）：守护进程清掉保存的锚点，等当前回合结束后压缩——Claude 往提示队列推 `/compact <instructions>`，以 `compact_boundary` 之后的 `result` 为完成；Codex 调 `thread/compact/start`（没有指令参数），以 `contextCompaction` 条目完成（或 `thread/compacted`）之后的 `turn/completed` 为完成。Codex 只把压缩那个回合（`thread/compact/start` 之后第一个不是我们消息触发的回合）或报过压缩的回合的结束算作完成，期间来的消息自己的回合不算。完成后回 `session.compacted {session_id, open_gen, compact_id, ok, context?, error?}`（可靠，键 `compact_id`）。会话停放时先 resume 再压缩；会话不在本机回 `ok: false`。失败也一定回：等空闲最多 2 分钟，期间一直在跑回合或来了新的 `session.send` 就放弃（`error: "busy: …"`，简报直接发，不压缩）；压缩本身最多 7 分钟（`timed out: …`）；压缩中运行时退出回 `exited: …`；守护进程把进行中的 `compact_id` 记在本机会话记录里，重启后对每个回 `restarted: …`。控制台只认跑这个会话的节点发来的 `session.compacted`。
- **压缩后补锚点**：运行时自己压缩（不是 `session.compact`）后，守护进程把 `session.send.anchor` 存下的锚点补给模型。Claude：SessionStart 钩子（`source: "compact"`）返回 `additionalContext`；自动压缩的 PreCompact 钩子返回 `systemMessage = focus + 锚点`，CLI 把它拼进压缩指令。Codex：压缩完成后如果回合还在跑，用 `turn/steer` 把锚点插进当前回合；回合已经结束（或 steer 被拒）就丢掉，绝不为锚点单开一个回合（下一条任务消息自带简报）。

## 5. 审批

### 5.1 `approval.request`（守护进程 → 控制台，可靠，键：`approval_id`）

```json
{
  "approval_id": "ap_<sha256(session_id + ':' + tool_use_id)[:24]>",
  "session_id": "s_…",
  "source": "claude_can_use_tool|codex_command|codex_file_change|codex_permissions|codex_mcp_elicitation",
  "tool_name": "Bash",
  "input": {"command": "pnpm add -D @tanstack/react-virtual"},
  "title": "…", "description": "…", "decision_reason": "…", "blocked_path": null,   // SDK 提供的现成文案
  "suggestions": [ … ],              // SDK PermissionUpdate[]（原样保存，allow_always 时回传）
  "diff": null,                       // Codex fileChange 的 diff
  "available": ["allow", "allow_always", "deny"]
}
```

### 5.2 `approval.resolve`（控制台 → 守护进程，可靠，键：`approval_id`）

```json
{"approval_id": "ap_…", "decision": "allow|allow_always|deny", "message": "用户拒绝了这个操作", "updated_input": null,
 "decided_by": "user:<id>|policy:thread_auto|policy:rule:<id>|policy:timeout"}
```

守护进程映射：

| decision | Claude `PermissionResult` | Codex 回应 |
| --- | --- | --- |
| allow | `{behavior:'allow', updatedInput: updated_input ?? input}` | `accept`（elicitation：`{action:'accept'}`） |
| allow_always | 上式 + `updatedPermissions: suggestions`（守护进程把每条 `destination` 改写为 `session`） | `acceptForSession` |
| deny | `{behavior:'deny', message}` | `decline`（elicitation：`{action:'decline'}`） |

守护进程处理后回 `approval.ack {approval_id}`（可靠）。若对应的 Promise / JSON-RPC 请求已不存在（已取消），回 `approval.ack {approval_id, stale: true}`。

### 5.3 `approval.cancel`（守护进程 → 控制台，可靠，键：`approval_id`）

`{"approval_id", "reason": "interrupted|session_closed|codex_resolved|process_restarted"}`：Claude `signal` 中止、Codex `serverRequest/resolved`，或守护进程/app-server 重启后发现对应的 Promise / JSON-RPC 请求已不存在（守护进程启动时扫描发件箱里未 ACK 的 `approval.request`，对这些发 cancel 而不是重放）。控制台把卡片标为 cancelled；`process_restarted` 例外：卡片保留（分离），等恢复后的员工重新发起同一请求时接回（architecture.md §9）。

## 6. 平台工具调用

### 6.1 `tool.call`（守护进程 → 控制台，可靠，键：`call_id`）

```json
{"call_id": "tc_01JD…", "session_id": "s_…", "open_gen": 3, "tool": "ask_lead", "args": {"question": "…"}, "tool_use_id": "toolu_01…"}
```

`call_id` 由守护进程在 MCP 处理器被调用时生成（`tc_` + ULID，先写本机日志再发），唯一性只要求在会话内；控制台按 `(session_id, call_id)` 去重。`tool_use_id`（Claude，经 PreToolUse 钩子关联）或 Codex item id 可选，仅供排查。

### 6.2 `tool.result`（控制台 → 守护进程，可靠，键：`call_id`）

```json
{"session_id": "s_…", "call_id": "tc_01JD…", "ok": true, "content": [{"type": "text", "text": "…"}], "structured": null}
```

`ok:false` → 守护进程以 MCP `isError: true` 返回给模型（工具失败从不变成异常，同 v1 约定）。

- 阻塞型工具：控制台在答复前不发 `tool.result`；守护进程不对其计时（Claude 侧 `MCP_TOOL_TIMEOUT=86400000`，Codex `tool_timeout_sec=86400`）。若会话被中断/关闭，守护进程发 `tool.cancel {call_id}`（可靠），控制台取消对应提问（status `cancelled`）。
- 重连后守护进程重发未收到结果、且处理器 Promise 仍在的 `tool.call`（同键）；控制台对已完成的直接回 `tool.result`。处理器已随进程消失的调用不重发，改发 `tool.cancel {call_id, reason:"process_restarted"}`（§3 对账规则）。

### 6.3 `file.chunk`（守护进程 → 控制台，可靠，键：`upload_id + index`）

```json
{"session_id": "s_…", "open_gen": 3, "upload_id": "up_01JD3V0000000000000000", "index": 0, "data": "PGh0bWw+PC9odG1sPg=="}
```

`publish_file` 的文件内容：守护进程读完文件（必须在会话 cwd 内，合计 ≤ 5 MB），把所有文件按 `upload.files` 的顺序拼起来，切成每段 ≤ 1 MiB 的 base64，先按序发 `file.chunk`，再发带 `upload` 的 `tool.call {call_id, session_id, tool, args, upload}`，其中 `upload = {upload_id, chunks, files: [{path, size, sha256, content_type}]}`（`path` 是交付物里的路径：单个文件就是文件名，网站目录则相对目录，入口为 `index.html`）。

可靠消息按序处理，所以控制台处理 `tool.call` 时分段都已到齐；分段数不对、大小或 sha256 不符、扩展名不允许都作为工具失败返回。分段只属于发它的会话；一天没被认领的分段会被清掉；一个会话未认领的分段合计超过 10 MB（两次满额上传）时，新分段直接丢弃，随后的 `tool.call` 按“分段不完整”失败。路径不合法等本机就能判断的错误不发到控制台，守护进程直接回 `isError`。

### 6.4 `file.download`（控制台 → 守护进程，可靠，键：`download_id + index`）

```json
{"session_id": "s_…", "call_id": "tc_01JF…", "download_id": "dn_01jd3v00000000000000000000", "index": 0, "data": "PGh0bWw+PC9odG1sPg=="}
```

`read_deliverable` 的文件内容，方向与 `file.chunk` 相反：控制台把那一版的文件按 `download.files` 的顺序拼起来，切成每段 ≤ 1 MiB 的 base64，先按序发 `file.download`，再发带 `download` 的 `tool.result`（`download = {download_id, deliverable_id, version, chunks, files: [{path, size, sha256}]}`，与 `tool.call.upload` 对称，`examples/messages.json` 里有完整的一对帧）。

守护进程只为仍在等待、且属于同一会话的 `read_deliverable` 调用保存分段（内存里，合计不超过 5 MB 的 base64），其他分段丢弃。收到 `tool.result` 后写到会话 cwd 下的 `.agent-deliverables/<deliverable_id>/v<version>/`，写之前重新校验 id 和版本、每个路径（相对、各段不以点开头、扩展名在白名单内、不重复）、分段是否到齐、合计 ≤ 5 MB、每个文件的大小和 sha256；`.agent-deliverables` 和其下目录必须是真目录，版本目录先整体删掉再写，文件以 `O_CREAT | O_EXCL | O_NOFOLLOW` 创建，顶层写 `.gitignore`（`*`）。成功时在结果文字前加一行本地绝对路径（`structured.abs_dir` 同样），失败（分段不全、校验不符、路径不合法）时把结果改成 `ok:false` 的工具失败。守护进程重启后未完成的调用按 §6.2 取消，分段随之作废；控制台对重复的 `tool.call` 只重发结果、不重发分段，这时结果按“内容不完整”失败，员工重新调用即可。只有 hello 带 `file_download` 特性的守护进程才会收到 `file.download`，否则工具直接失败并提示升级。

## 7. Git 作业

所有 git 命令以会话无关的节点作业执行，凭证同样经 credential helper 获取（`git_credentials` 绑定到 `project_id`）。

### 7.1 `git.prepare`（键：`job_id`）

```json
{"job_id": "g_…", "project_id": "…", "repo": "owner/name", "mode": "worktree|clone",
 "shared": "projects/7a1c…/repos/owner__name", "cwd": "projects/7a1c…/threads/12/dev-2/owner__name",
 "branch": "thread/12/dev-2", "base": "thread/12-employee-comms", "fetch": true}
```

一个作业只管一个仓库：项目有几个仓库，员工开会话前就有几个 `git.prepare`（每个仓库一个共享克隆、员工目录下每个仓库一个 worktree）。`base` 是讨论串分支（已在该仓库创建时）或本讨论串在该仓库的基准分支。

**开工前同步**（`hello.features` 含 `git_sync` 的守护进程）：`git.prepare` 带 `sync: {mode, ref}` 时，worktree 已存在也会更新：先 fetch；`member` —— 有未提交的改动（只看已跟踪文件）回 `dirty` 不动；成员分支没有 `origin/<ref>` 以外的提交就 `reset --hard origin/<ref>`（`reset`），有就 `merge origin/<ref>`（`merged`；冲突则 `merge --abort` 回 `conflict` 和冲突文件）；已经包含则 `current`。`review` —— 只读检出 `origin/<ref>`（detached，`checkout`）；worktree 还不存在时先按 `branch`/`base` 新建再检出（审查员在这个讨论串的第一次审查）。`review` 带 `expect_sha`（被审提交的完整 sha，守护进程 feature `git_review_sha`；旧守护进程的 schema 不认这个字段，控制台只发给声明了它的节点）时：共享克隆里还没有这个提交、或 `origin/<ref>` 不指向它，就重新 fetch（有限次数、逐次加长等待，不超过 `deadline`）；拿到后 `checkout --detach <expect_sha>` 并核对 `HEAD`；一直拿不到或核对不上，作业失败，`error_code: sha_mismatch`，`head_sha` 是实际的 HEAD。分支在任务完成后又有新提交时照样检出 `expect_sha`。`origin/<ref>` 还不存在时回 `current`。结果在 `git.result.sync {action, head_sha, conflicts?}`。merge 用平台身份（Agent Teams）提交。

步骤见 `architecture.md` §8.1。结果 `git.result {job_id, ok, head_sha, created: bool, log_tail, error_code?}`，`error_code`：`clone_failed|fetch_failed|not_a_repo|dirty_conflict|path_outside_root|locked_timeout|sha_mismatch`（review 同步没能检出 `expect_sha`）。

### 7.2 `git.status`（键：`job_id`）

`{"job_id", "cwd"}` → `git.result {…, status: {branch, head_sha, ahead, behind, dirty_files: n, unpushed_commits: n}}`。用于清理前检查、节点页展示。

### 7.3 `git.push`（键：`job_id`）

```json
{"job_id": "g_…", "project_id": "…", "repo": "owner/name", "cwd": "projects/7a1c…/threads/12/dev-2/owner__name",
 "refspec": "HEAD:refs/heads/thread/12/dev-2", "employee": "dev-2", "thread_no": 12,
 "unchanged_against": ["thread/12-employee-comms", "main"]}
```

每个仓库一个作业。带 `unchanged_against` 时守护进程先同步镜像，HEAD 在这些分支（不存在的忽略）之外没有提交就不推送，回 `git.result {job_id, ok: true, unchanged: true, head_sha}`：员工没改这个仓库。

平台替员工推送成员分支走这个作业（`push_branch`、`complete_task`，architecture.md §8.1；员工也可以用自己的令牌直接 `git push` 非保护分支）。守护进程校验 `refspec` 的目标必须是 `refs/heads/thread/<thread_no>/<employee>`、不带 `+`（不强推，除非 body 里 `force_with_lease: "<sha>"`，只用于冲突修复后的成员分支）；用 `cred.request {job_id, purpose:"push"}` 取只对这一次作业有效的写令牌，经 `GIT_ASKPASS` 只传给这一个 `git push --no-verify` 子进程。结果 `git.result {job_id, ok, head_sha, error_code?}`，`error_code`：`rejected_non_ff|workflow_permission|auth_failed|refspec_not_allowed`。

### 7.4 `git.cleanup`（键：`job_id`）

```json
{"job_id": "g_…", "shared": "projects/7a1c…/repo", "paths": ["projects/7a1c…/threads/12/dev-2", "…"],
 "keep_unpushed": true, "remove_shared_if_empty": false, "delete_local_branches": true}
```

逐个：有未推送提交或未提交改动且 `keep_unpushed` → 跳过并在结果里列出；否则 `git worktree remove --force` + `git branch -D`，最后 `git worktree prune`。结果 `git.result {…, removed: [...], kept: [{path, reason}]}`。

### 7.5 项目删除

`git.purge {job_id, path: "projects/<pid>"}`：在确认无活跃会话后删除整个项目目录（v1 删除项目会把检出留在节点上，v2 修正）。

删除讨论串用同一条命令删讨论串目录：`git.purge {job_id, path: "projects/<pid>/threads/12"}`（`12` 是讨论串编号），前面先发 `session.close`，再发 `git.cleanup {keep_unpushed: false}`（未推送的改动一并删除）。git 命令不排在会话队列里，所以守护进程收到 `git.purge` 时若目录下还有正在停止的会话，最多等 60 秒，仍未停止才回 `dirty_conflict`。路径只接受这两种形式，其他一律 `path_outside_root`。

## 8. 凭证

### `cred.request`（守护进程 → 控制台，不可靠但带 `req_id`，超时 10 s 重试）

`{"req_id", "session_id"|"job_id", "open_gen"?, "host": "github.com", "path": "owner/name.git", "purpose": "read|push"}`

控制台先做 §2.3 的归属校验，再按调用方决定令牌范围（architecture.md §10.4）：组长和成员的会话只给本项目仓库、按仓库缩小的员工令牌（`contents: write, pull_requests: write, issues: write, checks: read`，项目允许时加 `workflows: write`；PAT 连接直接给 PAT），`purpose` 对会话不起作用（git 的 credential helper 分不清 fetch 和 push），受保护分支由平台 pre-push 钩子拒绝；`git.prepare`/`git.status` 作业给只读令牌；`git.push` 作业给 `contents: write`（项目允许时加 `workflows: write`）；作业的令牌只发给作业自己的仓库（`body.repo`），用该仓库绑定的 Git 连接铸造；统筹会话一律拒绝。

### `cred.response`（控制台 → 守护进程，不可靠）

`{"req_id", "ok": true, "username": "x-access-token", "password": "ghs_…", "expires_at": "…"}` 或 `{"req_id", "ok": false, "error": "repo_not_in_project"}`。

此类消息**不进发件箱、不写日志**（守护进程和控制台都对 `cred.*` 关闭消息体日志）。

## 9. 节点管理

| 消息 | 方向 | 字段 | 说明 |
| --- | --- | --- | --- |
| `node.config` | 控制台 → | `max_active_sessions`, `park_after_s`, `log_level`, `isolated` | 节点页修改后即时下发 |
| `node.upgrade` | 控制台 → | `version`, `sha256`, `signature` | 下载地址**不由消息指定**：固定为本链路所连控制台的 `<console>/node/dist/agent-teams-node-<version>.tgz`（`version` 须匹配 `^\d+\.\d+\.\d+$`）。守护进程校验 sha256，再用安装时固化在 `~/.config/agent-teams/release.pub` 的发布公钥（Ed25519，`install-node.sh` 写入，换钥需重新运行安装脚本）验证 tgz 的签名；都通过才解包到 `node-<ver>/`、原子切换 `current` 软链、`exit 75` 由 systemd 重启。失败回 `node.upgrade_result {ok:false, error}`。被攻破的控制台无法让节点运行未经发布密钥签名的代码 |
| `node.drain` | 控制台 → | `deadline_s` | 不再接受新会话；空闲会话停放；用于节点维护 |
| `node.stats` | 守护进程 → | `active_sessions`, `parked_sessions`, `queued`, `mem_mb`, `load1`, `disk_free_mb` | 每 30 s，不可靠；机房机柜灯数据 |
| `node.usage` | 守护进程 → | `runtimes` | 不可靠；hello 后和之后定期上报 Claude Code / Codex 的订阅余量：`runtimes.claude`/`runtimes.codex` 各为 `{auth, status, plan?, windows?: [{key, label?, used_percent, window_minutes?, resets_at?}], error?, fetched_at?}`（`key`：`five_hour`/`weekly`/`weekly_opus`/`weekly_sonnet`/`weekly_model`，Codex 为 `primary`/`secondary`；`label` 是服务方给的显示名，如 `weekly_model` 窗口的模型名，最多保留 40 字符）。不带凭证；控制台只保存每个节点最近一次上报（内存），校验后通过 `NodeView.usage` 返回；收到 `node.usage_refresh` 后立即采集并回一条 |
| `node.usage_refresh` | 控制台 → | `runtimes?` | 不可靠；立即采集一次订阅余量并回 `node.usage`（`runtimes` 省略 = 全部）。只在 `hello.features` 含 `usage` 时发；由 `POST /api/nodes/{id}/usage/refresh` 触发（api.md） |
| `log` | 守护进程 → | `level`, `msg`, `fields` | 不可靠；只发 warn 及以上 |
| `error` | 双向 | `ref_id`, `code`, `message` | 对某条消息的协议级错误：`bad_schema`、`unsupported`、`unknown_session`、`busy` |

### 9.1 订阅余量（`hello.features` 含 `usage`）

守护进程上报本机 Claude Code、Codex 登录的订阅余量，节点页据此显示每个节点还剩多少额度。数据来源、可靠程度和版本兼容风险见 [usage-probe.md](usage-probe.md)。

- **`node.usage`**（守护进程 → 控制台，不可靠，不进发件箱）：收到 `welcome` 后立即上报一次，之后每 5 分钟一次（节点环境变量 `AGENT_TEAMS_USAGE_INTERVAL_S` 可以调整，设为 `0` 关闭定时上报）；每个运行时的读取总共最多 12 秒（Codex 的两次调用共用这个时间），保证在控制台等待 `node.usage_refresh` 回复的 15 秒内答复。读取失败只会让该运行时报 `status:"error"`，不影响守护进程的其他功能。
  - `runtimes.claude` / `runtimes.codex`：`auth`（Claude 取 `subscription|api_key|none`，Codex 取 `chatgpt|api_key|none`）、`status`（`ok|error|unsupported`）、`plan`（可选，例如 `max`、`pro`、`plus`）、`windows`、`error`（可选，简短原因，不含凭证）、`fetched_at`（读取时间；Codex 的数据如果来自最近一次推送事件，就填事件时间）。
  - `windows[]`：`key`、`used_percent`（0–100）、`window_minutes`（可选）、`resets_at`（ISO 时间或 null），按模型划分的窗口还会带 `label`。`key` 的取值：Claude 有 `five_hour`、`weekly`、`weekly_opus`、`weekly_sonnet`，以及 `weekly_model`（按模型划分的每周窗口，`label` 是服务端给的显示名）；Codex 有 `primary`（通常 5 小时）和 `secondary`（通常每周）。
  - `auth` 为 `api_key` 或 `none` 时，`status` 填 `unsupported`，`windows` 为空，不发任何请求。Codex 的 `auth.json` 里如果是 API Key 登录（app-server `account/read` 返回 `apiKey`），也按 `api_key` 处理。
  - 常见的 `error`：`timeout`；`unsupported_sdk`（SDK / CLI 没有实验性的 usage 接口，或者调用报错）；`rate_limits_unavailable`（CLI 表示该登录没有订阅余量，例如 token 缺少 profile 权限）；`fetch_failed`（CLI 没取到数据，常见原因是 token 过期而且刷新失败）；`not_logged_in`（Codex 已退出登录）。
  - 消息里永远不带 token、邮箱、组织或账号 id。
- **`node.usage_refresh`**（控制台 → 守护进程，不可靠，body 可以是空对象，也可以带 `runtimes`，例如 `["claude","codex"]`）：鼠标悬停在节点卡片上时，控制台发这条命令要求现查一次。守护进程回一条 `node.usage`，总是包含两个运行时：请求的运行时如果最近 60 秒内有一次成功（`status:"ok"`）的读取，就直接回缓存的结果，`fetched_at` 保持原值，不再请求 Anthropic 或 OpenAI；否则立即读取。失败的结果不缓存。同一时间只跑一次读取，正在跑的就等它跑完再用它的结果。定时上报不受缓存影响，每次都重新读取。

## 10. 关闭码

| 码 | 含义 | 守护进程行为 |
| --- | --- | --- |
| 1000 | 正常（控制台停机） | 重连 |
| 4401 | 令牌无效 / 所有者停用 | 停止重连，每 10 分钟重试一次并记日志 |
| 4409 | 被同节点的新链路替换 | 若本进程才是新的则忽略；否则退出（防双开） |
| 4426 | 需要升级 | 按 `welcome.min_daemon` 提示；若支持 `upgrade` 等待 `node.upgrade` |
| 4429 | 握手过于频繁 | 退避到 60 s |
| 1008 | 第一帧不是合法的 hello | 重连 |

## 11. 协议 1（已移除）

v1 的协议 1（扁平的 `start`/`output`/`exit`/`cancel` 帧）和迁移期的兼容设计已随 v1 一起删除，节点只说协议 2。

## 12. 时序示例

### 12.1 领任务并干活

```
控制台                                         守护进程(cvp)
 scheduler 领取 t8 → dev-3
 ├─ git.prepare g1 (seq 980) ──────────────────►│ worktree add -B thread/12/dev-3
 │◄─────────────────── git.result g1 ok (seq 1850)┤
 ├─ session.open s9 gen1 prepare=g1 (seq 981) ─►│ query({resume:null,…})
 │◄──────────── session.opened s9 (seq 1851) ────┤
 ├─ session.release s9 gen1 ─────────────────────►│ （工具核对通过）
 ├─ session.send m77 "t8 …" (seq 982) ─────────►│ prompt.push
 │◄──────────── session.delivered m77 ───────────┤
 │◄──── session.event turn_started / tool_started(Read) / … ─┤ → 办公室：claiming → returning → reading
 │◄──── tool.call complete_task(t8) ─────────────┤
 ├─ git.push g2 HEAD:refs/heads/thread/12/dev-3 ►│ （complete_task 前平台自动推送）
 │◄─────────────────── git.result g2 ok ──────────┤
 ├─ tool.result ok ──────────────────────────────►│
 │◄──── session.event turn_ended ────────────────┤ → 任务进入 review / 合并队列
```

### 12.2 断线期间的审批

```
守护进程: canUseTool → approval.request ap1 (seq 2001) ──X 链路断开
          （发件箱里保留 seq 2001）
重连: hello{last_seq:2003} → welcome{ack_seq:2000} → 守护进程重放 2001..2003
控制台: 写 at_questions(ap1)（ON CONFLICT DO NOTHING）→ 卡片
用户点放行 → approval.resolve ap1 (cmd seq 990) → 守护进程 resolve Promise → approval.ack
```
