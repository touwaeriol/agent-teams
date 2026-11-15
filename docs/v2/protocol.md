# 控制台 ↔ 节点守护进程协议 v2

适用：Go 控制台（`/server/internal/nodegw`）与 TS 守护进程（`/node/src/link`）。消息 Schema 的唯一来源是 `/protocol/schemas/node/*.json`，本文是其说明。v1（协议 1）的语义见 `architecture.md` §14 与 v1 代码 `remote.py` / `daemon.py`；v2 守护进程同时实现协议 1 以便迁移。

## 1. 连接

- URL：`wss://<console>/nodes/connect`（与 v1 同一路径，由 Go 前门接入并按 hello 协商，见 §11）；`/nodes/v2/connect` 是只接受协议 2 的别名，P6 之后守护进程默认用它。
- 认证：`Authorization: Bearer <node token>`。控制台 `sha256(token)` 查 `at_nodes.token_hash`，所有者必须存在且 `active`，否则以 **4401** 关闭（v1 用 1008，v2 守护进程两者都视为“令牌无效，停止重连并在日志里提示”）。
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
| 控制台 → 守护进程 | 所有命令（`session.*`、`approval.resolve`、`tool.result`、`git.*`、`cred.response`、`node.*`、`run.*`） | `ack`、`pong` 类 |
| 守护进程 → 控制台 | 会话事件、命令结果、审批请求/取消、工具调用、git 结果 | `ack`、`stream.delta`（逐字输出，可选）、`log` |

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
  "features": ["sessions", "codex_app_server", "git_worktree", "cred_helper", "legacy_runs", "upgrade", "permission_auto"],
  "isolated": false,
  "isolation": "agent_user|sandbox|none"     // architecture.md §10.3，守护进程启动时探测
}
```

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
  "employee": {"name": "dev-2", "role": "dev", "tier": "member", "project_id": "…", "thread_no": 12},
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
  "codex": null,                                   // runtime=codex 时：{"model","sandbox","approval_policy","developer_instructions","config":{…}}；
                                                   // 成员 config 含 sandbox_workspace_write.writable_roots=[<shared>/.git]、network_access=true；
                                                   // mcp_servers.platform 由守护进程每次 start/resume 自己填（URL 路径秘密，architecture.md §6.3）
  "platform_tools": [ {"name": "complete_task", "description": "…", "input_schema": {…}, "blocking": false}, … ],
  "env": {"LANG": "C.UTF-8"},                      // 额外的非秘密环境；守护进程自己加 PATH/HOME/MCP_TOOL_TIMEOUT/git 凭证变量
  "git_credentials": true,                         // 是否装 credential helper 与 gh 包装
  "skills": [{"name": "design", "files": [{"path": "SKILL.md", "content": "…"}]}],  // 可选：岗位的技能（architecture.md §6.5）
  "plugins": [{"name": "tinyplug", "version": 2, "files": [{"path": ".claude-plugin/plugin.json", "content": "…"}], "skills": ["tinyplug:greet"], "mcp_servers": {}}],  // 可选：岗位的插件，确切版本（architecture.md §6.5）
  "first_message": {"msg_id": "m_…", "text": "…"}  // 可选：工具核对通过、收到 session.release 后才发送
}
```

守护进程行为：

1. 校验 `cwd` 在 root 内；`prepare_jobs`（没有时 `prepare`）指定的 git 操作逐个等待成功（每个最多 10 分钟），任何一个失败就打开失败。
2. Claude：构造 `options`（`env` 逐项构造、`permissionMode` 显式、`settingSources: []`、`allowedTools`（含控制台由“总是允许”规则展开的项）、`settings`（沙箱与拒绝规则）、`canUseTool`、`hooks`、`mcpServers.platform = createSdkMcpServer(...)`、`includeHookEvents: true`、`resume`），用一个异步队列作为 `prompt` 调 `query()`，随即 `await q.initializationResult()` 拿工具列表和 MCP 状态（不等第一个回合的 `system/init`）。Codex：确保本机 app-server 在跑，`thread/start` 或 `thread/resume`（带新的 `mcp_servers.platform` 配置），工具列表取自 `mcpServerStatus/list` + 沙箱配置。
3. 回 `session.opened`。`first_message` 先扣住，收到 `session.release {session_id, open_gen}` 才推送。
4. `skills`（只有 Claude 会话会收到）：写成本地插件 `<root>/agent-skills/<project>/<thread_no>/<employee>/`（每次打开重建，不在 worktree 里），`options.plugins = [{type:'local', path, skipMcpDiscovery:true}]`、`options.skills = ['agent-teams-skills:<name>', …]`；`Skill` 工具由控制台放进 `tools`/`allowed_tools`。
4a. `plugins`：每个写到 `<root>/agent-plugins/<project>/<thread_no>/<employee>/<name>@<version>/`（每次打开整目录重建，不在 worktree 里；统筹忽略）。Claude：`options.plugins` 追加一个 local 插件项（skipMcpDiscovery 为 true），`options.skills` 追加 bundle 的 `skills`，`mcp_servers` 里的 `${CLAUDE_PLUGIN_ROOT}` 换成插件目录后以 `plugin_<plugin>_<server>` 放进 `options.mcpServers`（`strictMcpConfig` 不变）。Codex：只把 `mcp_servers` 的 stdio/http 服务器并进 thread 配置的 `mcp_servers`。
5. `resume` 失败（转录不存在、thread 找不到）：不自动开新会话，回 `session.opened {resume_failed: true, error}`，由控制台走交接流程（architecture.md §6.2）。

### 4.2 `session.opened`（守护进程 → 控制台，可靠，键：`session_id + open_gen`）

```json
{"session_id": "s_…", "open_gen": 3, "sdk_session_id": "8d2f…", "codex_thread_id": null,
 "init": {"model": "claude-opus-5-5", "tools": ["Read", "…", "mcp__platform__complete_task"], "permission_mode": "default",
          "claude_code_version": "2.1.325", "mcp_servers": [{"name": "platform", "status": "connected"}]}}
```

`init` 来自 `initializationResult()`（Codex 见上）。控制台用 `init.tools` 做角色工具核对（`architecture.md` §10.2）：通过 → 发 `session.release`（可靠，键 `session_id + open_gen`）；不符 → `session.close {reason:"policy_violation"}`。之后每个回合的 `system/init`（`turn_started` 事件带 `tools`）再核对一次，不符同样关闭。

### 4.3 `session.send`（控制台 → 守护进程，键：`msg_id`）

```json
{"session_id": "s_…", "msg_id": "m_01JD…", "text": "…", "priority": "now|next|later", "kind": "user|task|mail|answer|system",
 "interrupt_first": false, "images": [{"id": "img_…", "media_type": "image/png", "name": "截图.png", "size": 48213}]}
```

- `images`（可选，最多 8 张；`first_message` 也可以带）：用户附的图片。帧里只有引用（帧上限 4 MiB，图片单张可到 5 MB）；守护进程投递前用自己的节点令牌 `GET <控制台>/nodes/attachments/<id>`（`Authorization: Bearer <节点令牌>`，只能取同一用户项目的图片）下载到 `<state>/attachments/<session_id>/`，会话关闭时删掉。Claude：用户消息的 `content` 变成 `[{type:"image", source:{type:"base64", media_type, data}}…, {type:"text", text}]`（Agent SDK 0.3.325 的 `SDKUserMessage.message` 就是 Messages API 的 `MessageParam`，支持图片块）。Codex：在 `turn/start` / `turn/steer` 的 `input` 里追加 `{type:"localImage", path}`。下载失败时照样投递文字，末尾加一行说明图片没取到。

- 会话处于 `parked`：守护进程先用 `resume` 重开进程（不需要控制台再发 open），再投递。
- 会话还没收到本 `open_gen` 的 `session.release`：消息在守护进程排队，release 后按顺序投递（park 后自动 resume 的同一 gen 不需要再 release，但 init 工具若与上次不同则当作违规上报 `session.event{kind:error, code:'protocol'}`）。
- Claude：向 prompt 队列推 `SDKUserMessage {priority}`。Codex：空闲 `turn/start`，回合中 `turn/steer {expectedTurnId}`（`later` 则排到 `turn/completed` 之后）。
- `interrupt_first: true`：先 `interrupt()` / `turn/interrupt`，再投递。
- 回 `session.delivered {session_id, msg_id, turn_id?}`（可靠）。

### 4.4 `session.interrupt`（键：`id`）

`{"session_id": "s_…", "reason": "user_stop|thread_paused|superseded"}` → Claude `q.interrupt()`，Codex `turn/interrupt`。回 `session.interrupted {session_id, queued_msg_ids?: […]}`（SDK 的 `interrupt_receipt_v1` 能力返回仍在排队的消息 uuid，映射回 msg_id）。

### 4.5 `session.set_mode`（键：`id`）

`{"session_id": "s_…", "permission_mode": "default|acceptEdits|bypassPermissions|auto", "model": null}` → Claude `q.setPermissionMode()` / `q.setModel()`；Codex 在下一次 `turn/start` 带 `approvalPolicy`。`bypassPermissions` 只有会话以 `allowDangerouslySkipPermissions` 打开时才允许（守护进程否则回错误）。`auto`（会话打开时的 `permission_mode` 也一样）只发给 hello `features` 含 `permission_auto` 的守护进程；`auto` 会话里写到工作目录外的文件工具调用由 PreToolUse 钩子改成 `ask`，一定走 `approval.request`。

### 4.6 `session.close`（键：`session_id + open_gen`）

`{"session_id": "s_…", "reason": "thread_done|employee_removed|policy_violation|node_drain", "forget": false}` → Claude `q.close()`；Codex 不再路由该 thread（app-server 无需关闭）。`forget: true` 同时删除本机会话记录（SDK 转录文件保留，用于排查，按 7 天清理）。回 `session.closed`。

### 4.7 停放（守护进程自主）

空闲超过 `welcome.config.park_after_s` 且无运行中回合、无待审批、无待工具调用 → `q.close()`，发 `session.state {session_id, state:"parked"}`。下一次 `session.send` 自动 resume。活跃会话数达到 `max_active_sessions` 时，新 `session.send` 进入守护进程的等待队列，并发 `session.state {state:"queued"}`（办公室显示“排队等工位”）；需要时优先停放最久空闲的会话腾出位置。

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
| `turn_ended` | `result_subtype`, `is_error`, `num_turns`, `duration_ms`, `cost_usd?`, `usage{}` | `result` / `turn/completed` |
| `compacting` | `on: bool` | `system/status` / — |
| `rate_limited` | `retry_at?`, `detail` | `rate_limit_event`, `api_retry` / 错误通知 |
| `subagent` | `on: bool`, `agent_type` | SubagentStart/Stop / — |
| `notification` | `message`, `notification_type` | Notification 钩子 |
| `error` | `code`（`auth_failed|billing|crashed|spawn_failed|protocol`）, `message`, `fatal: bool` | `assistant.error`、进程退出、JSON-RPC 错误 |

`summary` 由守护进程生成且不含秘密（Bash 命令截断 200 字符、按正则抹掉 `ghs_…`/`ghp_…`/`sk-…` 形式的串）。

另有不可靠的 `stream.delta {session_id, turn_id, text}`：只在网页有人打开“实时输出”时由控制台发 `session.watch {session_id, on}` 开启，用于逐字显示，不落库。

### 4.9 `session.state`（守护进程 → 控制台，可靠）

`{"session_id", "state": "starting|active|turn_running|parked|queued|closed|crashed", "detail"?}`。崩溃（子进程意外退出）时守护进程自动以 `resume` 重开最多 3 次（间隔 5/15/45 s），仍失败则 `crashed` + `session.event{kind:error, fatal:true}`，由控制台决定（员工 → `broken`）。

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

`{"approval_id", "reason": "interrupted|session_closed|codex_resolved|process_restarted"}`：Claude `signal` 中止、Codex `serverRequest/resolved`，或守护进程/app-server 重启后发现对应的 Promise / JSON-RPC 请求已不存在（守护进程启动时扫描发件箱里未 ACK 的 `approval.request`，对这些发 cancel 而不是重放）。控制台把卡片标为 cancelled。

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

## 7. Git 作业

所有 git 命令以会话无关的节点作业执行，凭证同样经 credential helper 获取（`git_credentials` 绑定到 `project_id`）。

### 7.1 `git.prepare`（键：`job_id`）

```json
{"job_id": "g_…", "project_id": "…", "repo": "owner/name", "mode": "worktree|clone",
 "shared": "projects/7a1c…/repos/owner__name", "cwd": "projects/7a1c…/threads/12/dev-2/owner__name",
 "branch": "thread/12/dev-2", "base": "thread/12-employee-comms", "fetch": true}
```

一个作业只管一个仓库：项目有几个仓库，员工开会话前就有几个 `git.prepare`（每个仓库一个共享克隆、员工目录下每个仓库一个 worktree）。`base` 是讨论串分支（已在该仓库创建时）或本讨论串在该仓库的基准分支。

步骤见 `architecture.md` §8.1。结果 `git.result {job_id, ok, head_sha, created: bool, log_tail, error_code?}`，`error_code`：`clone_failed|fetch_failed|not_a_repo|dirty_conflict|path_outside_root|locked_timeout`。

### 7.2 `git.status`（键：`job_id`）

`{"job_id", "cwd"}` → `git.result {…, status: {branch, head_sha, ahead, behind, dirty_files: n, unpushed_commits: n}}`。用于清理前检查、节点页展示。

### 7.3 `git.push`（键：`job_id`）

```json
{"job_id": "g_…", "project_id": "…", "repo": "owner/name", "cwd": "projects/7a1c…/threads/12/dev-2/owner__name",
 "refspec": "HEAD:refs/heads/thread/12/dev-2", "employee": "dev-2", "thread_no": 12,
 "unchanged_against": ["thread/12-employee-comms", "main"]}
```

每个仓库一个作业。带 `unchanged_against` 时守护进程先同步镜像，HEAD 在这些分支（不存在的忽略）之外没有提交就不推送，回 `git.result {job_id, ok: true, unchanged: true, head_sha}`：员工没改这个仓库。

员工的推送只走这个作业（architecture.md §8.1）。守护进程校验 `refspec` 的目标必须是 `refs/heads/thread/<thread_no>/<employee>`、不带 `+`（不强推，除非 body 里 `force_with_lease: "<sha>"`，只用于冲突修复后的成员分支）；用 `cred.request {job_id, purpose:"push"}` 取只对这一次作业有效的写令牌，经 `GIT_ASKPASS` 只传给这一个 `git push --no-verify` 子进程。结果 `git.result {job_id, ok, head_sha, error_code?}`，`error_code`：`rejected_non_ff|workflow_permission|auth_failed|refspec_not_allowed`。

### 7.4 `git.cleanup`（键：`job_id`）

```json
{"job_id": "g_…", "shared": "projects/7a1c…/repo", "paths": ["projects/7a1c…/threads/12/dev-2", "…"],
 "keep_unpushed": true, "remove_shared_if_empty": false, "delete_local_branches": true}
```

逐个：有未推送提交或未提交改动且 `keep_unpushed` → 跳过并在结果里列出；否则 `git worktree remove --force` + `git branch -D`，最后 `git worktree prune`。结果 `git.result {…, removed: [...], kept: [{path, reason}]}`。

### 7.5 项目删除

`git.purge {job_id, path: "projects/<pid>"}`：在确认无活跃会话后删除整个项目目录（v1 删除项目会把检出留在节点上，v2 修正）。

删除讨论串用同一条命令删讨论串目录：`git.purge {job_id, path: "projects/<pid>/threads/<no>"}`，前面先发 `session.close`，再发 `git.cleanup {keep_unpushed: false}`（未推送的改动一并删除）。git 命令不排在会话队列里，所以守护进程收到 `git.purge` 时若目录下还有正在停止的会话，最多等 60 秒，仍未停止才回 `dirty_conflict`。路径只接受这两种形式，其他一律 `path_outside_root`。

## 8. 凭证

### `cred.request`（守护进程 → 控制台，不可靠但带 `req_id`，超时 10 s 重试）

`{"req_id", "session_id"|"job_id", "open_gen"?, "host": "github.com", "path": "owner/name.git", "purpose": "read|push"}`

控制台先做 §2.3 的归属校验，再按调用方决定令牌范围（architecture.md §10.4）：员工会话只给本项目仓库的只读令牌（`purpose: push` 来自会话一律拒绝）；`git.prepare`/`git.status` 作业给只读令牌；`git.push` 作业给 `contents: write`（项目允许时加 `workflows: write`）；作业的令牌只发给作业自己的仓库（`body.repo`），用该仓库绑定的 Git 连接铸造；统筹会话一律拒绝。

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
| `log` | 守护进程 → | `level`, `msg`, `fields` | 不可靠；只发 warn 及以上 |
| `error` | 双向 | `ref_id`, `code`, `message` | 对某条消息的协议级错误：`bad_schema`、`unsupported`、`unknown_session`、`busy` |

## 10. 关闭码

| 码 | 含义 | 守护进程行为 |
| --- | --- | --- |
| 1000 | 正常（控制台停机） | 重连 |
| 4401 | 令牌无效 / 所有者停用 | 停止重连，每 10 分钟重试一次并记日志 |
| 4409 | 被同节点的新链路替换 | 若本进程才是新的则忽略；否则退出（防双开） |
| 4426 | 需要升级 | 按 `welcome.min_daemon` 提示；若支持 `upgrade` 等待 `node.upgrade` |
| 4429 | 握手过于频繁 | 退避到 60 s |
| 1008 | v1 控制台的“令牌无效” | 同 4401 |

## 11. 协议 1 兼容（迁移期）

- 守护进程连 `/nodes/connect`，发出 hello 后：收到 `welcome` → 协议 2；5 s 内没收到 `welcome`、或直接收到 v1 的 `start`（Python 控制台忽略 hello 里不认识的字段，不回 welcome）→ 切入协议 1 模式。Go 前门的分流：Go 读 `hello.protocols`，节点属于 v1 用户 → 透明代理给 Python（hello 原样转发，Python 只看它认识的字段）；属于 v2 用户 → 回 `welcome {protocol:2}`。
- 为了让 Python 看到合规的 hello，v2 守护进程的 hello 帧**在顶层**同时带 v1 字段（v1 帧是扁平的 `{type:"hello", protocol:1, …}`，Python 不会去 `body` 里找）：

  ```json
  {"type": "hello", "protocol": 1, "allow": ["claude", "codex"], "max_runs": 4, "active": [], "claude": "2.1.325", "codex": "0.169.0",
   "v": 2, "id": "n_…", "body": {"protocols": [2, 1], "daemon": {…}, "epoch": "…", …}}
  ```

  `type`、`protocol`、`allow`、`max_runs`、`active`、`claude`、`codex` 在顶层，与信封的 `v`、`id`、`body` 并列；v2 字段只在 `body` 里。Go 读 `body.protocols`，没有 `body` 的就是旧 Python 守护进程。
- 管理员改变某用户的 `platform_version` 时，控制台关闭该用户所有节点链路（代理到 Python 的和 v2 的都关，关闭码 4426），守护进程立即重连并按新归属重新协商。
- 协议 1 模式下守护进程实现 v1 全部语义：`start {run_id, command[], cwd, after, env, repo}` 按 run_id 幂等、`state/runs/<sha256(run_id)[:32]>.jsonl` 日志重放、`output {run_id, seq, line}`、`exit {run_id, code, stderr}`、`cancel`（SIGTERM 进程组，5 s 后 SIGKILL）、退出码 126/127/128/130/137/143、`--allow` 白名单、`max_runs` 信号量、1 h 内存保留、7 天日志清理、空目录时 `git clone`。用 v1 的测试用例（fake claude）做兼容测试。

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
