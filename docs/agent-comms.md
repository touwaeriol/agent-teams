# 员工通信、审批与 Codex 接入（实现约定）

本文是这一轮改动的实现约定，后端、前端、Codex 三部分按这里的名字和格式对接。

## 现状

- 负责人：持续的 Claude Code 会话，经 `/mcp`（`server/mcp_bridge.py` 的 `ToolBridge`）拿到 `assign_task` / `list_tasks` / `cancel_task`（`tasks.py`）。
- 员工：每个任务一次 `claude -p`，没有任何工具，完成后结果以 `[Task t1 · dev · done]` 回到负责人对话（`conversation.py` 的 listener）。
- 页面实时事件走 SSE（`hub.py` `_hooks` → `broker.publish`）：`message`、`task`、`activity`。

## 目标

1. 员工干活中途能：汇报进度、问负责人、问用户、给其他员工发消息、读消息、看团队、看任务、请人审查、（有权限的）往下派子任务。
2. 员工做未被允许的操作时，权限请求变成网页上的审批卡片（用 Claude Code 内置的 `--permission-prompt-tool`，不用 hooks）。
3. 负责人新增：回答员工提问、广播、更新计划、问用户。
4. Codex 作为员工可用（`type: codex`），同样拿到员工工具。

## 核心：`Comms`（新文件 `src/agent_teams/comms.py`，与 web 无关）

每个对话一个 `Comms`，由 `Leader.open(...)` 创建并交给 `TaskBoard`。它保存：

- **信箱** `Mail(id: int, sender: str, recipient: str, body: str, created_at: float, read: bool)`；`recipient` 是员工名或 `"lead"` 或 `"*"`（广播）。
- **问题** `Question(id: str "q1"…, asker: str, task_id: str | None, audience: "lead" | "user", kind: "question" | "approval", text: str, options: list[str], answer: str | None, status: "open" | "answered" | "cancelled", created_at, answered_at, detail: dict)`。
  - `approval` 的 `detail` = `{"tool_name": ..., "input": {...}}`，`options` 固定 `["allow", "deny"]`，答案 `allow` / `deny`（可带 `allow_always`：本对话里该员工对同一 tool_name 以后自动放行）。
- **计划** `Plan = list[{"title": str, "status": "todo" | "doing" | "done"}]`。

`Comms` 对外（都是 async）：

```
send(sender, recipient, body) -> Mail          # recipient "lead" 时同时 post 到负责人对话："[Message from dev-a]\n..."
unread(name) -> list[Mail]; mark_read(name)
ask(asker, task_id, audience, text, options=[], kind="question", detail={}) -> Question   # 创建
wait(question_id) -> str                       # 阻塞到有答案（asyncio.Future）
answer(question_id, answer) -> Question        # 用户或负责人回答；audience=lead 的问题由负责人的 reply_to 回答
set_plan(steps) / plan
```

持久化与推送通过注入的 `CommsSink` 协议（server 实现、测试用内存实现）：`on_mail(mail)`、`on_question(question)`（新建和回答都调）、`on_plan(plan)`。重启恢复：open 状态的问题在重启后标为 `cancelled`（员工那边 MCP 调用会失败，员工会看到错误，负责人会收到任务报告）。

## 员工工具（每个员工任务一个 MCP 端点，token 绑定 conversation + task + employee）

| 工具 | 参数 | 行为 / 返回 |
| --- | --- | --- |
| `report_progress` | `text` | 发 `WorkerProgress` 事件（进 activity），返回 "ok" |
| `ask_lead` | `question` | 建 audience=lead 的问题，向负责人对话 post `"[Question q3 from dev-a · t2]\n<question>\n(answer with reply_to)"`，**阻塞**到负责人 `reply_to`，返回答案 |
| `ask_user` | `question`, `options?: list[str]` | 建 audience=user 的问题（页面弹卡片），**阻塞**到用户回答，返回答案 |
| `send_message` | `to`, `text` | `to` = 员工名或 `lead`；返回 "sent" |
| `read_messages` | — | 返回未读消息（含广播）并标已读 |
| `list_team` | — | 每个成员：名字、职责、类型、节点、当前在做的任务 |
| `get_task` | `task_id` | 任务说明、状态、结果 |
| `request_review` | `reviewer`, `summary` | 给 reviewer 建一个审查任务（parent=当前任务），**阻塞**到它结束，返回审查结果；不能请自己 |
| `assign_subtask` | `assignee`, `instruction` | 只有 `can_delegate: true` 的员工有；建子任务（parent=当前任务），立即返回任务号 |
| `wait_task` | `task_id` | 只有 `can_delegate: true` 的员工有；阻塞到该子任务结束，返回结果 |
| `approve`（权限提示工具） | Claude Code 传入 `tool_name`, `input`（及可能的 `tool_use_id`） | 建 approval 问题（audience=user），阻塞到用户点；返回 **JSON 文本** `{"behavior":"allow","updatedInput":<input>}` 或 `{"behavior":"deny","message":"用户拒绝了这个操作"}`；本对话已“总是允许”的直接 allow |

子任务（有 parent 的任务）结束时**不**向负责人对话 post 报告（由发起的员工自己 `wait_task` / `request_review` 拿结果），但照常发 `task` SSE 事件。`Task` 加字段 `parent: str = ""`（`types.py`，`TaskView`、`at_tasks` 同步加列）。

员工开工时，任务提示末尾自动附上：团队名单（名字 / 职责）、该员工的未读消息、"你可以用 team 工具……"的一段说明（让员工知道有这些工具，何时该问、何时该汇报）。

### 接到员工身上

- `ClaudeCodeWorker` 增加 `with_tools(endpoint: ToolEndpoint, approval_tool: str | None) -> ClaudeCodeWorker`：返回副本，`extra_args` 加 `mcp_args(endpoint)`（复用 `claude_session.py`），`allowed_tools` 加 `mcp_tool_names(endpoint)`（除 `approve`），有 `approval_tool` 时加 `--permission-prompt-tool mcp__team__approve`。
- Codex 员工实现同名方法（见下）。
- `TaskBoard._execute`：成员有 `with_tools` 且 board 有 `comms` 和 `tool_server` 时，为该任务 `expose` 员工 toolkit，`member.with_tools(...)` 后运行，结束后 `revoke`。
- MCP 工具调用可能阻塞很久：项目和非项目的 RemoteNode 环境都加 `MCP_TOOL_TIMEOUT=86400000`（Claude Code 读这个环境变量）。

## 负责人新增工具（`Leader` 的 toolkit）

| 工具 | 参数 | 行为 |
| --- | --- | --- |
| `reply_to` | `question_id`, `answer` | 回答 audience=lead 的问题 |
| `broadcast` | `text` | 发给所有员工（`recipient="*"`） |
| `send_message` | `to`, `text` | 给某个员工留言 |
| `update_plan` | `steps: list[{title, status}]` | 覆盖计划，页面右侧显示 |
| `ask_user` | `question`, `options?` | **不阻塞**：建问题卡片，立即返回问题号；用户回答后向负责人对话 post `"[Answer to q5] <answer>"` |

`LEADER_INSTRUCTIONS` 补充这些工具的用法（员工的 `[Question …]` 要尽快用 `reply_to` 回答；计划有变就 `update_plan`）。

## 团队配置

`ClaudeCodeSpec` 加 `can_delegate: bool = False`（`CodexSpec` 同）。前端团队表单加开关“可以给其他员工派子任务”和成员类型选择（Claude Code / Codex）。

## 服务端（`server/`）

- 表：`at_mail(conversation_id, id BIGSERIAL, sender, recipient, body, created_at, read_by JSONB)`、`at_questions(conversation_id, id, asker, task_id, audience, kind, text, options JSONB, detail JSONB, answer, status, created_at, answered_at, PRIMARY KEY(conversation_id, id))`、`at_conversations.plan JSONB`、`at_tasks.parent TEXT DEFAULT ''`。Memory 和 Postgres 两种 store 都要实现。
- `ToolBridge` 不变（一个 `/mcp`，token 选 toolkit）；员工 toolkit 通过同一个 `ToolServer.expose` 发布。
- API：
  - `GET /api/conversations/{id}/questions?status=open|all` → `QuestionView[]`
  - `POST /api/conversations/{id}/questions/{qid}/answer` body `{"answer": str}`（approval 用 `allow` / `deny` / `allow_always`）→ `QuestionView`；只有对话主人能答 audience=user 的问题；已答的返回 409
  - `GET /api/conversations/{id}/mail` → `MailView[]`（最近 200 条）
  - `GET /api/conversations/{id}/plan` → `{"steps": [...]}`
- SSE 新事件（`data:` 里的 JSON）：
  - `{"type":"question","question":QuestionView}`（新建和状态变化都发）
  - `{"type":"mail","mail":MailView}`
  - `{"type":"plan","plan":{"steps":[...]}}`
  - `task` 事件的 task 多 `parent` 字段
- `QuestionView` = `{id, asker, task_id, audience, kind, text, options, detail, answer, status, created_at, answered_at}`（时间用 ISO 字符串）；`MailView` = `{id, sender, recipient, body, created_at}`。

## 前端（`web/`）

- 对话正文里：audience=user 的 open 问题显示为卡片（提问员工、所属任务、问题、选项按钮或输入框）；approval 卡片显示工具名和参数（命令用代码块），按钮“放行”“总是允许”“拒绝”；回答后卡片变为已处理状态。刷新后从 `GET questions` 恢复。
- 右侧上下文面板：新增“计划”分节（步骤 + 状态）放在任务进度上面；“工作动态”里混排员工之间的消息（`mail`，显示 `dev-a → dev-b`）；任务列表里子任务缩进显示在父任务下面。
- 侧栏项目下的对话若有待你处理的问题，显示一个小圆点。
- 团队表单：成员类型选择（Claude Code / Codex）、`can_delegate` 开关。
- 只用 `components/common` 和 `components/ui`，亮色为主、暗色可用；`npx tsc -b` 和 `npx vite build` 必须通过。

## Codex（`src/agent_teams/codex.py`）

- `CodexWorker(Runnable)`：字段与 `ClaudeCodeWorker` 对应（name、node、description、instructions、workdir、model、sandbox、extra_args…），在节点上运行 `codex exec --json …`（非交互），解析 JSONL 事件：进度 → `WorkerProgress`，最后的回复 → 任务结果，失败 → 异常。具体参数和事件格式以**实际安装的 codex CLI** 为准（在 docker 里 `npm i -g @openai/codex` 后看 `codex exec --help`、`codex --version`，并用一个假的 OpenAI key 跑一次看事件形状；拿不到真实输出的部分写清楚假设）。
- 员工工具：`with_tools(endpoint, approval_tool)` 用 `-c` 配置覆盖把 `/mcp` 端点作为 streamable HTTP MCP 服务器传给 codex（bearer token 走环境变量或 header 配置，以 CLI 支持的方式为准）。审批：`codex exec` 非交互，审批用沙箱策略代替（`approval_tool` 忽略，文档里说明）；后续再接 `codex app-server`。
- `config.py` 加 `CodexSpec`（`type: codex`），平台校验（`workspaces.py`）允许员工是 codex（负责人仍须 claude_code）。
- 守护进程默认允许 `codex`（已是），节点 hello 里报告 `codex --version`（像 claude 一样）。
- 测试用一个假的 codex 脚本（像 `tests/fake_claude.py`）。

## 约束

- 代码风格见 `CLAUDE.md`（ruff 全规则、mccabe ≤ 6、mypy strict、函数小而平）。每个行为改动配测试。
- 测试命令（容器 `at-dev` 已装好依赖、源码挂载在 /src）：`docker exec at-dev sh -c 'ruff format . && ruff check . && mypy && pytest -q'`。
- 不要 commit、不要 push、不要部署；不要动 `deploy/`、`docker-compose.yml`。

## Codex：实际核对

2026-11-14 在一次性容器（`node:22` + `npm i -g @openai/codex`）里核对，CLI 版本 **codex-cli 0.169.0**。没有真实 OpenAI key：先用假 key 跑了一次失败的运行，再用本地假的 Responses API（`-c model_provider=mock -c model_providers.mock={base_url=…, wire_api="responses"}`）加一个真实的 streamable HTTP MCP 服务器（mcp 2.x `MCPServer`）跑通了一次成功的运行。

**亲眼看到的（`CodexWorker` 依赖这些）**

- 调用：`codex exec --json --skip-git-repo-check --sandbox <read-only|workspace-write|danger-full-access> [--model M] -c k=v … -- <prompt>`。`-c` 的值按 TOML 解析（解析失败时当原样字符串）。`-C <dir>` 存在，但我们不用：节点已在员工目录里启动进程。stdin 不是终端时 codex 会读 stdin（stderr 打 `Reading additional input from stdin...`），节点和守护进程都给的是 `/dev/null`，没问题。
- 指令：`-c developer_instructions="…"` 会作为 `role: developer` 的消息送给模型（在假 Responses 服务器上看到了）。`--model` 原样进请求的 `model` 字段。
- MCP：`codex mcp add team --url U --bearer-token-env-var V` 写出的配置是 `[mcp_servers.team] url=… bearer_token_env_var=…`；`codex mcp list --json` 显示 `-c mcp_servers.team.url="…"` 和 `-c mcp_servers.team.http_headers={Authorization="Bearer …"}` 被识别为 `streamable_http`。实际运行中 codex 连上了 MCP 服务器、调用了工具。配置项里还有 `tool_timeout_sec`、`startup_timeout_sec`、`enabled_tools`、`disabled_tools`。
- 我们用 `http_headers` 传 token（和 Claude Code 的 `--mcp-config` 一样，token 在命令行里）；`bearer_token_env_var` 也能用，但要给每次运行单独加环境变量，节点接口现在不支持。
- MCP 工具在模型那边是 namespace `mcp__team` 下的 `report_progress` 这种名字；JSONL 里是 `mcp_tool_call` 项，带 `server`、`tool`、`arguments`。
- JSONL 事件（stdout，每行一个；日志和报错走 stderr）：
  - `{"type":"thread.started","thread_id":…}`、`{"type":"turn.started"}`
  - `{"type":"item.started"|"item.completed","item":{"id":"item_N","type":…}}`，看到的 item 类型：
    - `agent_message` `{text}`：最后一条就是任务结果
    - `command_execution` `{command, aggregated_output, exit_code, status: in_progress|completed|failed}`
    - `mcp_tool_call` `{server, tool, arguments, result:{content,structured_content}, error, status}`
    - `error` `{message}`：非致命警告，例如未知模型的 metadata 提示
  - `{"type":"turn.completed","usage":{input_tokens, cached_input_tokens, output_tokens, reasoning_output_tokens,…}}`：成功，退出码 0
  - `{"type":"error","message":"Reconnecting... 2/5 (…401…)"}`：重试中
  - `{"type":"turn.failed","error":{"message":…}}`：失败，退出码 1
- 只设 `OPENAI_API_KEY` 时 `codex exec` 不会用它（401 "Missing bearer"）；设 `CODEX_API_KEY` 才会。节点上要么先 `codex login`，要么在节点环境里设 `CODEX_API_KEY`。
- `--sandbox workspace-write` 在不允许非特权 user namespace 的容器里不能用：命令直接失败（`bwrap: No permissions to create a new namespace`）。这种节点要用 `danger-full-access`，或者在宿主机上开放 user namespace。

**假设的（没有亲眼看到）**

- `reasoning`（`text`）、`file_change`（`changes` 列表）、`todo_list` 等 item 类型：模拟的模型没有产生这些。代码只把它们显示为进度，结果不依赖它们。
- `-- <prompt>`：按 clap 的惯例认为 `--` 后面的内容都是位置参数（防止 `-` 开头的任务被当成选项）。没有单独测试。
- `tool_timeout_sec=86400` 被接受了，但没有测过一个调用真的阻塞几个小时会怎样。
- `disabled_tools=["approve"]` 的键名是从 `codex mcp get --json` 看到的，没有确认模型请求里真的少了这个工具。
- 审批：`codex exec` 没有类似 `--permission-prompt-tool` 的选项。`approval_tool` 只用来隐藏 `approve` 工具，能做什么由 `sandbox` 决定。以后要接审批卡片，得改用 `codex app-server`。
