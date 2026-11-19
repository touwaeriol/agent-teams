# v2 网页接口（REST + SSE）

由 Go 控制台（Gin + huma，Casbin 做路由授权）提供，OpenAPI 3.1 文档在 `GET /api/openapi.json`（网页类型由它生成）。本文按设计稿画板分组；表结构见 `data-model.md`。

## 0. 通用约定

- **前缀**：`/api`。沿用 v1 时期的路径（语义不变的保持原样）；GitHub 回调路径 `/api/git/github-app/created|installed|install` 必须保持不变（GitHub 的 App 设置指向它们）。
- **认证**：cookie `agent_teams_session`（HttpOnly、SameSite=Lax、https 时 Secure）或 `Authorization: Bearer <session token>`。失败 401 `{"detail":"请先登录"}`。
- **CSRF**：头 `X-CSRF-Token`，值等于 cookie `agent_teams_csrf`（登录时下发，非 HttpOnly）；所有 cookie 认证的非 GET/HEAD 请求都要求该头。Bearer 认证的请求免 CSRF。见 `architecture.md` §10.1。
- **错误**：保持 v1 形状 `{"detail": "<中文消息>"}`，另加 `code`（机器可读，如 `thread_not_found`、`node_quota`）和可选 `fields`（校验错误 `[{path, message}]`，替代 v1 前端里拍平 pydantic 数组的逻辑）。状态码：400 参数、401、403、404（别人的资源也返回 404）、409 冲突/状态不允许、422 校验、429 限流、503 依赖不可用。
- **分页**：列表默认 50 条，游标分页 `?cursor=<opaque>&limit=`，响应 `{items: [...], next_cursor: string|null}`。v1 兼容路径仍返回裸数组。
- **时间**：ISO 8601 UTC 字符串；界面按浏览器时区显示。
- **幂等**：创建类 POST 接受 `Idempotency-Key` 头（24 h），重复请求返回第一次的结果（发消息、开讨论串、答复卡片都用它，防网络重试导致重复）。
- **限流**：登录/注册 10 次/分钟/IP，节点创建 10 次/小时/用户，发消息 60 次/分钟/用户（Valkey 令牌桶），超出 429 + `Retry-After`。

## 1. 实时事件

### 1.1 项目事件流 `GET /api/projects/{pid}/events`

- SSE，`Content-Type: text/event-stream`，`Cache-Control: no-cache`，`X-Accel-Buffering: no`，15 s 一次 `: ping`。
- 每帧带 `id:` 和 `event:` 名。`id` = **项目事件序号 `seq`**：`at_events` 和 `at_status_events` 共用 `at_projects.event_seq` 分配的同一个序号，按 seq 顺序提交（`architecture.md` §11.1），所以一个数字就能同时标记两类事件的位置。断线重连浏览器自动带 `Last-Event-ID`，服务端从两张表补发 `seq > Last-Event-ID` 的事件（按 seq 合并，再按 `topics`/`thread` 过滤；最多补 24 h / 5000 条，超出则发 `event: reset`，前端整体重新拉取）。过滤掉的事件不发帧，但游标照样前进（服务端在下一帧或每次 ping 前发一条只有 `id:` 的空注释帧 `id: <seq>\n: skip`，保证浏览器记住的位置不落后）。
- 过滤：`?topics=message,thread,task,question,status…`（默认全部）、`?thread=<no>`（只要某讨论串）。办公室页用 `topics=status,thread,task,merge,ci,node,layout`。

| `event:` | `data` | 发生于 |
| --- | --- | --- |
| `message` | `{conversation_id, thread_no?, message: MessageView}` | 用户、统筹、组长、平台说话 |
| `thread` | `{thread: ThreadView}`，删除时 `{deleted: {id, no}}` | 讨论串新建 / 状态 / PR / 计数变化 / 删除 |
| `main_task` | `{thread_no, main_tasks: MainTaskView[]}` | 计划变化、自动打勾 |
| `task` | `{thread_no, task: TaskView, counts: ThreadCounts}` | 小任务新建、领取、状态变化；`counts` 是变化后讨论串的计数（含 `tasks_open`），网页据此更新列表里的讨论串，不用等 `thread` 事件 |
| `question` | `{thread_no?, question: QuestionView}` | 提问 / 审批新建、答复、取消 |
| `mail` | `{thread_no, mail: MailView}` | 员工间留言 |
| `activity` | `{thread_no?, employee, text, at}` | 员工活动摘要（工具开始/结束、进度；**落库**，可回看） |
| `status` | `{employee: EmployeeRef, status: EmployeeStatusView}` | 员工状态变化（办公室、讨论串“进度”页；来自 `at_status_events`，**不在 `at_events` 里**，与其他事件同一序号） |
| `deliverable` | `{deliverable: DeliverableView}` | 交付物登记 / 更新 |
| `merge` | `{thread_no, entry: MergeEntryView}` | 合并队列变化 |
| `ci` | `{thread_no, sha, checks: CheckView[]}` | CI 结果变化 |
| `memory` | `{op: "write"|"delete", name}` | 记忆文件变了（网页重新拉列表和打开的文件） |
| `node` | `{node: NodeBrief}` | 该项目用到的节点上下线、槽位变化 |
| `layout` | `{layout_id, version}` | 办公室布局更新（前端重新拉布局并重画） |
| `reset` | `{}` | 补发窗口不够，前端需整体重新拉取 |

### 1.2 用户事件流 `GET /api/events`

侧栏和设置页用：`project`（项目列表变化、等你处理数 `{project_id, waiting}`）、`node`（节点上下线）、`notice`（系统通知）。同样支持 `Last-Event-ID`（序号来自 `at_users.event_seq`，只补 1 h，之后 `reset`）。

### 1.3 只在需要时的实时输出

`GET /api/sessions/{sid}/stream`：SSE，逐字输出（`delta`），用于讨论串里“看实时输出”。打开时控制台向节点发 `session.watch on`，所有观看者断开 30 s 后 `off`。不落库、不补发。

## 2. 视图对象

```ts
UserView        = {id, email, display_name, role: "admin"|"user", active, max_nodes: number|null, must_change_password, created_at}
ProjectView     = {id, name, description, context, repos: RepoRef[],   // context = 项目指令（≤16,000 字，每个会话都读）；绑定的仓库有顺序；基准分支等约定写在 context 里
RepoRef         = {repo: "owner/name", git_connection_id: string|null}
                   /* ProjectView 其余字段： */
                   pinned, archived, work_mode: "worktree"|"clone", auto_approve,
                   merge_policy,            // pr（默认）：PR 的必需 check 通过即完成，PR 留给人合并；auto：check 通过后平台按 merge_method 自动合并；manual：用户验收后平台合并（architecture.md §8.4）
                   delivery: DeliverySettings, office_layout: string, waiting: number, threads_active: number,
                   icon: string,            // '' = 名字首字母配稳定底色；一个表情；或 "color:#RRGGBB" = 首字母配该颜色
                   coordinator_model: string, thread_model: string, thread_codex_model: string, thread_effort: string,  // 项目级覆盖，按类型：统筹 / Claude 组长和成员 / Codex 组长和成员；'' = 跟随岗位（下次开会话生效）
                   auto_resume: boolean,    // 使用限额重置后自动继续讨论串（data-model.md §2.10）
                   created_at, updated_at}
DeliverySettings = {gates: {review, decision, merge_queue}, ci: {mode, required, appear_timeout_s, timeout_s, on_missing},
                   merge_method: "squash"|"merge"|"rebase", approval_timeout_s: number|null,
                   notify_on_ask: boolean,          // “成员提问时通知我”：员工 ask_user / 审批卡片出现时发浏览器通知 + 用户事件流 notice（默认 true）
                   allow_workflow_edits: boolean,   // “允许员工修改 workflow”，默认 false（architecture.md §8.1）
                   approval_routing: "lead_first"|"user",  // 审批方式：组长先判断，拿不准再问我（默认）/ 全部问我（architecture.md §9.2）
                   lead_review_timeout_s: number}   // 组长判断时限，10–3600，默认 120；到期转给用户
ThreadView      = {id, project_id, no, title, brief, status, queue_position: number|null, pause_reason: string, branch, base_branch /*旧字段*/, pr_number, pr_url, pr_state, summary,
                   merge_error: string|null,  // 验收后没能合并的原因（检查未通过/还在跑、冲突、没有权限…，中文可读）；accept、reject 和合并成功时清空
                   merge_policy: 'pr'|'auto'|'manual',  // 项目的合并策略（architecture.md §8.4）；review 只在 manual 或有 merge_error 时算“等你处理”（counts.waiting）
                   repos: ThreadRepoView[],   // 每个仓库：{repo, base_branch, branch_created, pr_number, pr_url, pr_state}；pr_number/pr_url = 第一个 PR，pr_state = 汇总
                   auto_approve, opened_by, main_tasks: MainTaskView[],
                   members: EmployeeBrief[],  // 讨论串参与者（shared-members.md §3）：组长、讨论串自己的旧成员、在这里有任务或开过会话的项目成员；busy_elsewhere 只在这里有意义
                   counts: {waiting, questions_open, approvals_open, tasks_total, tasks_done, main_total, main_done},
                   lead_last: {text, at} | null, cost_usd_est: number|null, created_at, updated_at,
                   last_activity_at,                          // 状态变化、讨论串里的消息、它发布的交付物中最新的时间
                   dispatch: {opened_by, at /*讨论串创建时间*/,  // 派发给组长的交接信息（brief 本身看 thread.brief）；brief 为空或是转发的原话（副本已在对话里）时整个为 null
                              source: {message_id: number, content, attachments: AttachmentRef[] /*同 meta.attachments*/, created_at} | null} | null,
                              // source = 触发 open_thread 的用户原话（见 data-model source_message_id），没有关联或已删除时为 null
                   latest_deliverable: DeliverableView | null} // 讨论串发布的最新一个交付物
MainTaskView    = {id, pos, title, status: "todo"|"doing"|"done", auto_done, tasks_total, tasks_done}
TaskView        = {id, thread_no, main_task_id, assignee, role, claimed_by, kind, instruction, status, depends_on: string[],
                   parent, priority, attempt, summary, branch, head_sha, created_at, claimed_at, finished_at}
QuestionView    = {id, thread_no, asker, task_id, audience: "lead"|"user", kind: "question"|"approval", text, options: string[],
                   detail: {tool_name?, input?, title?, description?, diff?, source?,
                            questions?: [{question, header, multi_select?, options: [{label, description?, recommended?}]}],  // 结构化提问
                            answers?: [{question, header?, selected: string[], other?}], via?: "AskUserQuestion"},
                   blocking, answer, status, decided_by,
                   created_at, answered_at}
MessageView     = {id, role: "user"|"leader"|"employee"|"system"|"error", author,
                   kind: "chat"|"note"|"interrupt"|"todos"|"plan"|"artifact"|"image"|"resumed",   // 后五种是对话卡片（architecture.md §6.6）
                   content, meta: {thread_no?, task_id?, opens_thread?: number, employee?, todos?: [{content, status, active_form?}],
                   deliverable?: {id, kind, title, url, hosted, entry, render, version},
                   attachments?: [{id, name, content_type, size}], title?}, created_at}   // image 卡片：员工用 publish_file 发的截图（不是交付物）
                   // todos 卡片原地更新：同一 id 的 message 事件再来一次，网页按 id 替换
MailView        = {id, sender, recipient, body, created_at}
EmployeeBrief   = {id, name, tier, role, title, runtime, node: {id, name, online} | null, look: Look,
                   status: EmployeeStatusView | null,
                   thread_no: number|null,    // 组长 / 旧成员 = 自己的讨论串；项目成员 = 当前 claimed / running 任务的讨论串，否则最近干活的讨论串；统筹和闲着的成员为 null
                   sort: number,              // 岗位在岗位模板里的顺序（办公室工位按岗位分排）
                   instance: number,          // 同岗位里的实例编号（dev-2 → 2）
                   busy_elsewhere: boolean}   // 讨论串成员列表里：这个项目成员当前的任务在别的讨论串（thread_no 是哪个），网页显示“在 #12 忙”；其他地方恒为 false
EmployeeStatusView = {state, substate, bubble, bubble_kind, task_id, task_kind, tier, role, since, seq}
Look            = {color, outfit: string, accessory: string, hair: string|null}
                  // outfit/accessory 是字符串，不是枚举：取值 = 当前全局布局 looks.outfits / looks.accessories 的键
                  // （内置 tee|hoodie|suit|coat、none|glasses|headset|cap 必有），服务端保存时按布局校验，422 带 fields
DeliverableView = {id, project_id, thread_no, kind: string, title, url, detail, published_by, created_at, updated_at}
                  // kind 已知集合：doc | design | slides | dashboard | board | data；其他字符串按“链接”卡片显示。pr 不是交付物（PR 记在讨论串上），发布时拒绝
MergeEntryView  = {id, thread_no, task_id, employee, repo, head_branch, status, attempts, error, created_at, updated_at}   // 每个仓库一个条目
CheckView       = {name, status, conclusion, url, summary}
NodeView        = {id, name, online, since, last_seen_at, version, protocol, isolation: "agent_user"|"sandbox"|"none", runtimes: {...}, capacity: {...},
                   active_sessions, parked_sessions, queued, isolated, labels, max_active_sessions, park_after_s, created_at}
RoleView        = {id, tier, role, title, runtime, count, node_ids, description, instructions, model, effort,
                   permission_mode: "bypass"|"ask",  // 省略 = bypass（直接执行，任何节点）；旧值 auto 读作 bypass；非成员岗位总是 bypass（architecture.md §9.1）
                   can_delegate, read_only, allowed_tools, disallowed_tools, sandbox, max_turns, gate, task_kinds, look, sort,
                   context_limit, compact_between_pct, compact_in_task_pct, clear_after_tasks, compact_focus}
                  // 会话管理（shared-members.md §5.1）：百分比相对 context_limit（token，null = 模型窗口）；新岗位四项都没给时按类型填默认值
                  // （写代码的成员 60/80/8、审查 / 决策 null/80/1、组长和统筹 null/70/null），组长和统筹的 compact_between_pct、clear_after_tasks 总是 null
                  // read_only：只读岗位（组长同款策略，Codex = sandbox read-only）；gate 为 review|decision 时默认 true。
                  // Team 页规则行显示“只读：git diff/log/show、gh pr view，不改代码”
GitConnectionView = {id, name, kind: "token"|"app", login, installation_id, created_at, permissions, needs_permission_approval,
                   installation_settings_url: string|null}   // App 连接：https://github.com/settings/installations/<id>（组织安装为 /organizations/<org>/settings/installations/<id>）
RepoItem        = {full_name, private, default_branch, used_by_projects: [{id, name}]}   // 仓库下拉，“已被项目使用”标签
```

## 3. 按画板分组

### 3.1 Login（登录 / 注册）

| 方法 | 路径 | 请求 → 响应 |
| --- | --- | --- |
| POST | `/auth/register` | `{email, password}` → 201 `UserView` + 设 cookie（规则同 v1：首个用户或 `AGENT_TEAMS_ADMIN_EMAILS` 为 admin；`registration_open=false` 时 403；重复 409） |
| POST | `/auth/login` | `{email, password}` → `UserView` + cookie（401 `邮箱或密码不对`，停用 403） |
| POST | `/auth/logout` | → 204 |
| GET | `/api/me` | → `UserView` |
| POST | `/api/me/password` | `{current, new}` → 204（新：v1 没有改密码），同时注销其他会话 |
| GET | `/api/me/sessions` / DELETE `/api/me/sessions/{id}` | 查看 / 注销自己的登录会话（新） |

`/auth/*` 路径与 v1 相同（v1 网页兼容）。

### 3.2 Main（项目工作区：统筹对话 + 讨论串面板）与 Sidebar

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/projects` | → `ProjectView[]`（置顶优先，再按 `updated_at`；带 `waiting` 用于侧栏数字）。`?q=` 搜索名字/描述 |
| PATCH | `/api/projects/{pid}` | 侧栏菜单：`{pinned?, archived?, name?}`（完整字段见 3.6） |
| DELETE | `/api/projects/{pid}` | → 204；关闭全部会话，向用过的节点发 `git.purge` |
| GET | `/api/projects/{pid}/coordinator` | → `{conversation_id, employee: EmployeeBrief, session_state}`；不存在则创建统筹（员工 + 对话），**不立即开会话**（第一条消息时才开） |
| GET | `/api/conversations/{cid}/messages?before=&limit=` | → `{items: MessageView[], next_cursor}`（v1 路径；v2 增加分页参数，不带参数时返回全部以兼容 v1） |
| POST | `/api/conversations/{cid}/messages` | `{content, interrupt?: bool, attachments?: string[], refs?: MessageRef[]}` → 202 `MessageView`。`refs`（最多 10 个）是输入框里用 @ 引用的 `{kind: file\|thread\|deliverable\|memory, repo+path \| no \| id \| name}`：控制台逐个按本项目查（别人的或不存在 → 422），解析成一段“用户用 @ 引用的上下文”接在投递给员工的文字后面（文件：仓库默认分支上的内容，经 GitHub contents API，最多 30000 字节；讨论串：标题、状态、任务说明、总结、组长最新消息；交付物：标题、id、类型、地址、摘要，托管的再加最新一版的文件列表和入口文件内容（html、md、svg、json、csv 等文本类，最多 30000 字节；图片、PDF 只说明类型）及 `read_deliverable` 取回提示；记忆文件：内容），存下的消息只有用户自己的话，`meta.refs` 记下 chip（`{kind, label, …}`）。附件里的文本文件内容同样接在投递文字后面（每个最多 60000 字节），图片和 PDF 作为文件投递。以 `/名字` 开头的消息原样投递，Claude 会话把它当斜杠命令执行（守护进程把岗位技能的短名补成 `agent-teams-skills:名字`，见 `protocol.md` §4.3）。投递到对应员工会话（统筹或组长）；投给统筹的文字末尾带 `[message_id: N]`（消息的 id，统筹据此调用 `forward_to_thread`），存下的消息不带。`interrupt: true` = “插话并打断当前操作”。`attachments` = 本项目已上传图片的 id（最多 8 张，按顺序），有图片时 `content` 可以为空；消息的 `meta.attachments` 记下 `{id, name, content_type, size}`，员工收到的是图片内容块（Claude）或本地图片（Codex），见 `protocol.md` §4.3。讨论串已结束（done / failed）时先自动重新打开（同 `POST /api/threads/{tid}/reopen`，备注写“用户发来新消息，讨论串重新打开。”）再投递，组长按旧会话恢复；已归档 → 409 `thread_archived`（先取消归档） |
| POST | `/api/conversations/{cid}/forward` | 把自己在项目对话里发的消息原样转发到讨论串：`{message_ids: int[]（1–20 条，必须是调用者在这个项目统筹对话里的 user 消息，否则 422）, thread_no?: int, new_thread?: {title?}, note?: string}`，`thread_no` 和 `new_thread` 二选一 → 200 `ForwardView {thread_no, thread_id, title, count, created, duplicate, messages: MessageView[]}`。每条消息（文字、`meta.attachments`、`meta.refs`）复制成讨论串对话里的用户消息，`meta.forwarded_from = {conversation_id, message_id, message_ids, count, index, by: user\|coordinator, note?}`（note 只在最后一条）；组长收到一条投递：开头说明这是用户本人的原话、附言，再逐条列出（文本附件内容和 @ 引用按发送时的规则重新展开，图片和 PDF 作为文件，合计最多 8 个附件，超过 → 422）。讨论串已结束先重新打开再投递（同普通消息），parked / closed 的组长被唤醒；已归档 → 409 `thread_archived`。`new_thread`：开一个新讨论串（标题默认取第一条的第一行），任务说明就是这段转发文字，图片和 PDF 随任务说明一起发给组长。项目对话里留一条备注“已转发 N 条消息到 #12 · 标题”（`meta.forwarded_to = {no, count, created, by, message_ids, key}`，网页点它打开讨论串）。30 秒内把同一组消息再转发到同一处（双击）不重复投递，返回第一次的讨论串和 `duplicate: true`；也支持 `Idempotency-Key`。统筹用平台工具 `forward_to_thread` 做同一件事（`architecture.md` §6.3） |
| POST | `/api/projects/{pid}/attachments` | `{name, data}`（base64）→ 201 `{id, name, content_type, size, sha256, created_at, url}`。收 PNG、JPEG、GIF、WebP 图片和 PDF（按文件头判断，不信扩展名，每个最多 5 MB，Claude 的上限），以及 UTF-8 文本文件（md、txt、json、csv、代码等，按扩展名，最多 1 MB，`content_type: text/plain`），否则 422。输入框粘贴、拖入或从“+ → 添加文件或照片”选择时立即上传，发送时带上 id |
| GET | `/api/conversations/{cid}/commands` | → `{employee, tier, runtime, from: session\|role, commands: [{name, description, argument_hint, source: skill\|plugin}]}`：输入框 “/” 弹出列表里的技能部分。优先用员工最近一次会话 `session.opened.init.commands` 报上来的（岗位技能去掉 `agent-teams-skills:` 前缀），还没有会话时按岗位配置的技能和插件列；统筹没有技能，Codex 员工为空。平台命令（/model、/effort、/status、/context、/skills、/usage、/memory、/config）由前端自己执行，不经过员工；`hidden` 是岗位不显示的平台命令，`custom: [{name, description, mode: prompt\|passthrough, prompt, source: role\|platform}]` 是岗位的自定义命令加平台内置命令（岗位同名优先；透传命令不给 Codex 和成员）。弹出列表：平台命令 → 自定义 → 技能，输入时名字完全相同或前缀匹配的排在最前（“最佳匹配”） |
| GET | `/api/conversations/{cid}/model` | → `{scope: coordinator\|thread, employee, runtime, model, override, inherited, session, live, members: [{name, tier, runtime, model, override}]}`：/model 面板。`model` 是员工现在用（或下次打开会话用）的模型：员工自己的（/model 设的）→ 项目的统筹 / 讨论串模型（只对 Claude）→ 岗位模型 → 项目所有者模型列表里该类型的默认 → 内置兜底（Claude：`claude-opus-5-5`；Codex：空，交给 Codex CLI 自己的默认）；`session` 是会话上一轮实际用的模型（`at_usage.model`） |
| PUT | `/api/conversations/{cid}/model` | `{model, scope?: lead\|thread}` → `{applied: [{employee, model, live}], skipped: [名字]}`。统筹对话写项目的 `coordinator_model`；讨论串对话写组长（`scope: thread` 时是讨论串里和组长同类型的全部员工，其他类型的列在 `skipped`）的 `at_employees.model`。`model` 为空 = 回到设置；格式同模型列表。在节点上有会话的员工马上收到 `session.set_mode {model}`（Claude `Query.setModel`，Codex 下一轮 `turn/start` 带 `model`），从下一轮起生效（`live: true`）；否则下次打开会话时生效。审计 `model_change` |
| GET | `/api/conversations/{cid}/context` | → `{employee, runtime, instructions_chars, memory_index_chars, memory_files, messages, message_chars, session: {state, model, turns, total, last_turn, cost_usd} \| null}`：/context 面板。token 数来自 `at_usage`（每个回合的 SDK result usage），只算最近一个会话 |
| GET | `/api/projects/{pid}/references?q=&kind=` | → `{items: [{kind, label, detail, repo?, path?, no?, id?, name?}], files_note}`：输入框 “@” 弹出列表。文件来自各仓库默认分支的 git trees API（每仓库缓存 5 分钟，最多 20000 个路径），模糊匹配取前 30 个；讨论串、交付物、记忆文件各取前 8 个。读不到仓库时 `files_note` 说明原因，其余照常返回 |
| GET | `/api/voice/config` | 任何登录用户 → `{enabled, provider?, transport?: "relay" \| "webrtc", languages: [{code, name, zh}]}`。管理员没开语音输入、没存 Key 或控制台解不开 Key 时 `enabled: false`，输入框不显示麦克风。不含 Key 和接口地址 |
| GET | `/api/voice/stream` | WebSocket（默认的"经服务器中转"）。子协议 `["agent-teams-voice", "csrf.<agent_teams_csrf cookie>"]`（浏览器不能给 WebSocket 加请求头，CSRF 令牌放在子协议里；Origin 必须与 Host 一致），`?language=auto\|<code>`。没登录 401、CSRF 不对或跨站 403、语言不在列表 422（都在升级前）。连上后服务器用密封的 Key 连 `wss://<base>/realtime?intent=transcription` 并发 `session.update`，OpenAI 接受后发 `{"type":"ready","model","client_commits","max_seconds"}`。浏览器发二进制帧（24 kHz 单声道 PCM16 小端，约 100 ms 一帧，单条 ≤ 64 KiB）和 `{"type":"commit"}` / `{"type":"stop"}`；服务器只回 `input_audio_buffer.committed`、`conversation.item.input_audio_transcription.delta/completed/failed`、`error`（只留 `type/item_id/delta/transcript/error{code,message}`，Key 被替换成 `***`）。结束前发 `{"type":"error","fatal":true,"error":{code,message}}`：`voice_disabled`、`rate_limited`（每人每分钟 20 路）、`voice_too_many`（每人同时 2 路，Valkey 计数，跨副本）、`voice_unreachable` / `voice_key_rejected` / `voice_busy` / `voice_upstream` / `voice_timeout`（10 秒内连不上 OpenAI 或会话没被接受）、`voice_idle`（30 秒没收到消息）、`voice_max_duration`（5 分钟）、`voice_too_fast`、`voice_bad_frame`、`voice_upstream_closed`、`voice_protocol`。审计 `voice_stream_start` / `voice_stream_stop`（模型、语言、时长、音频秒数、commit 次数、结束原因；没有声音和文字）。详见 `voice.md` |
| POST | `/api/voice/session` | 仅"浏览器直连"模式使用。`{language?: ISO-639-1 \| "auto"}` → `{provider, transport: "webrtc", connect_url, client_secret, expires_at, model, language, client_commits}`：控制台用平台的 OpenAI Key 调 `POST /v1/realtime/client_secrets` 签一个 120 秒的转写会话临时密钥（`ek_…`），浏览器拿它直接和 OpenAI 建 WebRTC（声音不经过平台）。每人每分钟 20 次（429 + `Retry-After`）；语言不在列表里 → 422；没开 → 404 `voice_disabled`；OpenAI 拒绝 Key → 502 `voice_key_rejected`。审计 `voice_session`（只记模型、语言、会话 id）。详见 `voice.md` |
| GET | `/api/attachments/{id}` | 附件本身（文本文件为 `text/plain; charset=utf-8`）（只给项目所有者；`X-Content-Type-Options: nosniff`、`Content-Security-Policy: default-src 'none'; sandbox`）。聊天记录里的缩略图用它 |
| POST | `/api/conversations/{cid}/interrupt` | → 202。中止该对话员工的当前回合（停止按钮） |
| GET | `/api/projects/{pid}/threads?status=&group=waiting\|active\|done` | → `ThreadView[]`（右侧讨论串列表：等你处理 / 进行中 / 已完成，归档的折叠在底部“已归档 N”里） |
| GET | `/api/projects/{pid}/waiting` | → `{questions: QuestionView[], reviews: ThreadView[]}`（跨讨论串的“等你处理”） |
| POST | `/api/conversations/{cid}/questions/{qid}/answer` | `{answer?, answers?, reason?}`；审批用 `answer` = `allow`、`deny`、`allow_always`，可附 `reason`（拒绝理由，发给员工）；提问用 `answer`（文字）或 `answers: [{selected: string[], other?}]`（结构化提问，按问题顺序，对不上卡片 → 422 `validation`）→ `QuestionView`（v1 路径，语义扩展） |
| GET | `/api/conversations/{cid}/questions?status=open\|all` | → `QuestionView[]`（v1 路径） |
| GET | `/api/projects/{pid}/search?q=` | 搜索消息、讨论串标题、任务、交付物（PG `tsvector` + `pg_trgm`，中文用 trigram）→ `{threads, messages, tasks, deliverables}` |

讨论串详情面板（右侧展开）用 3.3 的接口。面板顶部是面包屑“讨论串 > #12 标题”（点“讨论串”回列表），下面四个页签：**进度**（为什么在等、每个员工此刻在做什么——工具和命令来自 `status` 事件的 bubble，等审批的员工下面直接是放行 / 拒绝卡片——以及实时“动态”，默认只显示最后几条，可展开）、**任务**、**交付物**（本讨论串的设计稿、文档、文件卡片，点开进查看器）、**成员**。面板宽度可拖动（340–760 px，且不超过窗口 60%），按用户记在浏览器 localStorage，双击拖动条恢复默认。中间统筹对话里讨论串卡片下面显示一行实时状态（谁在跑什么 / 等你审批几条）。

### 3.3 Thread（讨论串详情：两级任务与任务池）与 MobileThread

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/projects/{pid}/threads` | `{title, brief, roles?: [{role, count}]}` → 201 `ThreadView`（用户直接开串，跳过统筹）。**`roles` 的含义变了**：以前是按岗位覆盖这个讨论串的人数，现在成员按项目编制共用，`roles` 只取岗位名，**限定**组长只能往这些岗位派任务（`only_roles`；平台建的审查 / 决策 / 修复任务不受限），`count` 忽略；不传 = 所有岗位。传了模板里没有的岗位 → 422 |
| GET | `/api/threads/{tid}` | → `ThreadView` |
| PATCH | `/api/threads/{tid}` | `{title?, status?: "paused"|"running"|"archived", auto_approve?}` → `ThreadView`。`auto_approve` = “本讨论串内全部放行”（只影响 `ask` 岗位，任何节点都能开；打开时一并放行当前所有待审批，并审计） |
| POST | `/api/threads/{tid}/archive` | → `ThreadView`。归档：讨论串结束（会话关闭、节点上的检出按 `git.cleanup keep_unpushed` 清理），从主列表移到“已归档”，记下归档前的状态（`at_threads.archived_from`）。已结束的讨论串（done / failed）常用它在“解决后归档” |
| POST | `/api/threads/{tid}/complete` | body `{summary?}` → `ThreadView`。用户把讨论串标记为完成（组长做完了但没自己收尾）：状态改成 `done`（和 finish 同一条路径：会话关闭、未答卡片取消、检出清理），协调员收到 `[Thread #n · 标题 · done] 用户在页面上把讨论串标记为完成。`。已是 done → 原样返回；failed / archived → 409 `thread_not_active`；PR 等待接受/拒绝（review）或正在合并 → 409 `pr_awaiting`；合并队列还有条目 → 409 `merge_in_progress` |
| POST | `/api/threads/{tid}/reopen` | → `ThreadView`。重新打开 done / failed 的讨论串：状态回到 `running`（运行中的讨论串已达上限时为 `queued`），`finished_at` 清空，旧总结写进讨论串里一条 `note`（`meta.reopened = true`、`meta.previous_summary`）后清空，停滞检查重新计数，协调员收到 `[Thread #n · 标题 · running] 用户重新打开了讨论串 #n。`。不立即开会话：下一条消息唤醒组长（按 SDK 会话 id 恢复旧对话，节点丢了记录时用回顾），组长给成员发消息时成员同样恢复；恢复后的第一条消息带“[平台] 讨论串已重新打开，接着之前的对话继续。”。已合并 / 已关闭的 PR 成为历史：对应仓库行和讨论串的 `pr_number`/`pr_url`/`pr_state` 清空，`open_pr` 可以为新改动再开 PR、`finish` 重新按“有无改动”判断；PR 已合并的仓库同时忘掉讨论串分支（`branch_created = false`），提交后在 GitHub 上删除旧分支，新的工作从更新后的基准分支重新切出；已关闭未合并的 PR 保留分支和提交。还在进行中 → 原样返回；archived → 409 `thread_archived`（先 `unarchive`，done/failed 归档的会回到原状态，再重新打开） |
| POST | `/api/threads/{tid}/unarchive` | → `ThreadView`。done / failed 归档的回到原状态；进行中归档的回到 `paused`（`pause_reason = "user"`），点“继续”接着做。没归档 → 409 `not_archived` |
| DELETE | `/api/threads/{tid}` | → 204。关闭全部会话，向用过的节点发 `git.cleanup`（`keep_unpushed: false`，未推送的改动也删）和 `git.purge projects/<pid>/threads/<no>`，删除成员分支；讨论串、员工、任务、对话、审批记录一并删除（级联），交付物留在资料库（`thread_no` 变成 null）。SSE 发 `thread {deleted}`。网页上先弹确认框 |
| GET | `/api/threads/{tid}/main-tasks` | → `MainTaskView[]` |
| PATCH | `/api/threads/{tid}/main-tasks/{mid}` | `{status}`（用户手动打勾/取消） |
| GET | `/api/threads/{tid}/tasks?status=&main=` | → `TaskView[]`（任务池看板：可领 / 进行中 / 待审查 / 待决策 / 合并中 / 完成） |
| POST | `/api/threads/{tid}/tasks/{task}/cancel` | 取消小任务 |
| POST | `/api/threads/{tid}/tasks/{task}/retry` | 失败任务重新入池（failed / blocked / cancelled；依赖里有 failed / cancelled 的任务时 409 `dependency_ended`） |
| GET | `/api/threads/{tid}/members` | → `EmployeeBrief[]`（含实时状态） |
| POST | `/api/threads/{tid}/members/{name}/interrupt` | 打断某个成员的当前回合 |
| POST | `/api/threads/{tid}/members/{name}/message` | `{content}` → 直接给成员发话（以“用户”身份，进该成员会话，同时记到讨论串对话里）。讨论串已结束（done / failed）时先自动重新打开；已归档 → 409 `thread_archived` |
| GET | `/api/threads/{tid}/mail?before=` | → 分页 `MailView` |
| GET | `/api/threads/{tid}/activity?before=` | → 分页 activity（来自 `at_events`，v1 不落库）。“进度”页的动态列表先拉这里，之后由 SSE `activity` 追加 |
| GET | `/api/threads/{tid}/merge-queue` | → `MergeEntryView[]` |
| GET | `/api/threads/{tid}/checks` | → 最近一次合并的 `CheckView[]` |
| POST | `/api/threads/{tid}/open-pr` | `{title?, body?, force?: bool}` → `ThreadView`（Thread 页“开 PR”按钮；每个已有讨论串分支、还没有 PR 的仓库各开一个，base 为本讨论串在该仓库的基准分支，409 `nothing_to_pr` = 还没有合并进任何仓库的改动；前提同组长 `open_pr`：无 `merging` 条目、主任务全部 done（`force` 可跳过这一条）；标题/正文缺省时平台由讨论串标题、主任务和小任务总结生成；409 `pr_exists`、409 `merge_in_progress`） |
| POST | `/api/threads/{tid}/accept` | → 200 `ThreadView`，立即返回，不在请求里调 GitHub：讨论串必须是自己的、在 `review`、有未合并的 PR（没有 PR → 409 `no_pr`；不在 review 或 PR 已合并/关闭 → 409 `not_in_review`），然后 `pr_state` 置为 `accepting`、清空 `merge_error`（推 `thread` 事件）。已经是 `accepting` 时原样返回，不会重复合并（同一进程内去重；跨副本靠讨论串行锁，合并前在锁里再确认一次 `pr_state`，已被别处合并的 PR 算成功）。`merge_policy = manual`：合并循环等 PR 必需 check 通过后按 `merge_method` 合并 → `pr_state = merged`、讨论串 done；GitHub 4xx（无权限、405 不可合并、409 冲突等）→ `pr_state` 回到 `open` 并写 `merge_error`；限流和 5xx / 网络错误下一轮自动重试。`pr` / `auto`：accept = “不等了，现在合并”，平台在后台（最长 3 分钟）检查并合并：check 还在跑或没通过、GitHub 4xx、5xx 或网络错误、超时 → `pr_state` 回到 `open`、原因写进 `merge_error`，由用户再点 accept 重试；GitHub 限流（429，或正文含 rate limit 的 403）不算失败，讨论串保持 `accepting`，交给合并循环下一轮接着重试。合并前若用户已 reject，合并停止，失败也不会覆盖 reject 的结果。check 没通过时组长也会收到通知、讨论串回到 running（原有逻辑） |
| POST | `/api/threads/{tid}/reject` | `{reason}` → `ThreadView`（原因发给组长，回到 running，清空 `merge_error`；不在 review → 409 `not_in_review`；`pr` / `auto` 下也可用，在完成或自动合并前拦下） |
| GET | `/api/threads/{tid}/usage` | → `{cost_usd_est, tokens: {...}, by_employee: [...]}` |

讨论串里的员工碰到 Claude **使用限额**（`session.event` 的 `rate_limited` 带 `retry_at`，且离现在至少 2 分钟；几秒的 API 重试不算）时，平台记下等待（`at_rate_limit_waits`），把讨论串暂停成 `status = "paused"`、`pause_reason = "rate_limit"`，等待期间该员工回合结束不再催（不发“任务还没完成”的提醒）。到 `retry_at` 后：项目开了 `auto_resume` 就恢复讨论串，并给碰到限额的员工发一条系统消息让它从中断处继续；没开则只清掉等待，讨论串保持暂停，由用户手动继续。统筹碰到限额同理（没有讨论串可暂停，到点只发消息）。

和组长的对话就是该讨论串的 `conversation_id`（= `tid`），复用 3.2 的消息接口。

### 3.4 Overview（项目首页：讨论串一览）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/projects/{pid}` | → `ProjectView` |
| GET | `/api/projects/{pid}/overview` | → `{project: ProjectView, threads: ThreadView[], waiting: {questions, reviews}, recent_deliverables: DeliverableView[], memory: MemoryBrief[] (MEMORY.md 在前，最多 6 个), usage_7d: {cost_usd_est, by_day: [...]}, nodes: NodeBrief[]}`（一个请求出首屏） |

### 3.5 Deliverables（交付物一览）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/projects/{pid}/deliverables?kind=&thread=&q=&cursor=&no_images=` | → `{items: DeliverableView[], next_cursor, storage_bytes}`；`q` 搜索标题和 `detail.summary`（`pg_trgm`）；`no_images=true` 不返回截图不再算交付物之前发布的托管图片（`detail.hosted` 且 `render = image`），网页的交付物区、资料库和讨论串交付物视图都带上它，按 id 查看的查看器不带；`storage_bytes` = 项目托管文件（每个版本都算）占用的字节数，资料库底部“已使用” |
| POST | `/api/projects/{pid}/deliverables` | `{kind, title, url, thread_no?}` → 201（用户手动登记链接）；`kind = pr` → 400 `pr_not_deliverable`（PR 由平台在讨论串上自动跟踪，不是交付物） |
| POST | `/api/projects/{pid}/deliverables/request` | `{kind, brief}` → 202 `MessageView`（“新做一个”磁贴） |
| PATCH / DELETE | `/api/deliverables/{id}` | 改标题 / 删除 |
| POST | `/api/projects/{pid}/library` | 资料库“添加”：`{name, data: base64, title?}` → 201 `DeliverableView`。单个文件，只收 html、md、pdf、txt、json、csv（扩展名白名单，≤ 5 MB；截图、图片和 SVG 不是交付物，一律 422 `validation`），存成托管交付物第 1 版：`published_by = "user"`，`detail = {by: "user", hosted: true, version: 1, entry, content_type, render, files: 1, size}`，`kind` 按渲染方式（html → design，Markdown/PDF/文本 → doc，json/csv → data），之后和员工发布的文件一样经签名 URL 沙箱访问 |
| GET | `/api/deliverables/{id}/files?version=` | 托管交付物（`detail.hosted`）的一版（默认最新）→ `{deliverable_id, version, versions: [{version, files, size, published_by, publisher_thread_no?, created_at}]（新到旧；`publisher_thread_no` = 发布这一版的员工所在讨论串）, entry, render: html\|markdown\|image\|pdf\|text, files: [{path, content_type, size, sha256}], base_url, entry_url, expires_at, isolated}`；`base_url` 是 2 小时有效的签名目录 URL（`architecture.md` §6.5），`isolated` = 配置了 `AGENT_TEAMS_ARTIFACT_ORIGIN` |
| GET | `/api/deliverables/{id}/comments` | → `{items: [{id, version, text, sent_to, created_at}]}` |
| POST | `/api/deliverables/{id}/comments` | `{text, version?}` → 201：保存并作为消息投递给被批注那一版（默认最新版）的发布员工，不在了给他讨论串的组长（消息里带 `read_deliverable` → 修改 → `publish_file` 的改稿方法），`sent_to` 是收到它的员工 |
| GET | `/files/<token>/<path>` | 签名 URL 下的文件（不需要 cookie；CSP sandbox、nosniff，见 `architecture.md` §6.5） |

**“新做一个”磁贴**（设计、文档、幻灯片、看板 dashboard、白板 board）：不是新流程，而是给统筹发一条模板消息。`POST …/deliverables/request` 等价于 `POST /api/conversations/<统筹对话>/messages`，正文 = `做一个<kind 的中文名>：<brief>`（例如“做一个幻灯片：给投资人的 v2 进展介绍”），`meta.request_kind = <kind>`；统筹照常决定是否开讨论串，产出由员工 `publish_deliverable(kind, …)` 登记。前端在弹出框里收 `brief`，提交后跳到 Main 页的统筹对话。

网页里“资料库”就是这个列表：主界面右侧的面板（`/projects/:pid?panel=library`）和全屏页 `/projects/:pid/deliverables`，表格列 名称 / 所有者（`published_by`，`user` 显示为“你”）/ 修改日期 / 大小（`detail.size`）/ 类型（托管文件按扩展名，其余按 kind），可按类型筛选、搜索、切换网格。

PR 不在资料库里：它的状态在 `ThreadView.repos[].pr_*` 上，由平台轮询 / webhook 更新。设计类和托管交付物（员工用 `publish_file` 发布）在查看器 DesignViewer（`/projects/:pid/deliverables/:id`）里打开：HTML 在 sandbox iframe 里，Markdown 安全渲染，图片内联（只有截图不再算交付物之前发布的旧数据），可切换版本，批注发回给员工。

### 3.6 NewProject 与 ProjectSettings

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/projects` | `{name, description?, context?, repos?: RepoRef[], work_mode?, auto_approve?, merge_policy?, delivery?, first_message?}` → 201 `ProjectView`。`first_message` 有值时投递给统筹（v1 是新建对话再发消息） |
| PATCH | `/api/projects/{pid}` | `name, description, context, pinned, archived`，`repos?: RepoRef[]`（整体替换并保持顺序，`[]` 解绑全部；每个仓库要有能访问它的连接，重复 422）+ `work_mode, auto_approve, merge_policy, delivery, office_layout`（`delivery.notify_on_ask` = “成员提问时通知我”；`auto_approve` 只影响 `ask` 岗位，任何节点都能开）+ `icon`（`''`、一个表情或 `color:#RRGGBB`，其他 422）、`coordinator_model`、`thread_model`、`thread_codex_model`（模型 id，`''` = 跟随岗位；非空必须在项目所有者的模型列表里且类型对得上：前两个是 Claude，`thread_codex_model` 是 Codex，否则 422 字段级错误；和已保存值相同的放行）、`thread_effort`（`'' \| low \| medium \| high \| xhigh \| max`，即 SDK 0.3.325 的 `effort`）、`auto_resume`。模型覆盖在开会话时生效：统筹用 `coordinator_model`，Claude 组长和成员用 `thread_model`，Codex 组长和成员用 `thread_codex_model`，组长和成员都用 `thread_effort` |
| GET | `/api/projects/{pid}/usage?days=7` | 项目用量（`at_usage`，每个回合结束时 SDK 报告的 token 和费用）→ `{days, cost_usd_est, tokens: {input, output, cache_read, cache_write}, turns, by_day: [{day, cost_usd, tokens}], by_thread: [{thread_no: number\|null, title, cost_usd, tokens}], by_employee: [{employee, thread_no, cost_usd, tokens}]}`；`thread_no = null` 是项目统筹；`days` 1–90，缺省 7；按天按 UTC 日期分组 |
| GET | `/api/projects/{pid}/schedules` | 定时任务 → `ScheduleView[]` = `{id, project_id, name, cron, timezone, message, enabled, next_run_at, last_run_at, last_error, created_at, updated_at}` |
| POST | `/api/projects/{pid}/schedules` | `{name, cron, timezone?, message, enabled?}` → 201。`cron` 五段（分 时 日 月 周，支持 `*`、数字、`a-b`、`/n`、逗号；周 0 和 7 都是周日；日和周都限定时任一满足即可），`timezone` 是 IANA 名（缺省 UTC，网页传浏览器时区）；表达式或时区不对 422（`fields[0].path` 指出字段）；每个项目最多 20 个 |
| PATCH / DELETE | `/api/schedules/{id}` | 同上字段的部分更新（保存后从现在重新算 `next_run_at`；停用时为 null）/ 删除 |
| POST | `/api/schedules/{id}/run` | → 202：现在发一次（不改 `next_run_at`） |

定时任务到点时，平台把 `【定时任务：<name>】\n<message>` 作为用户消息发给项目统筹（`meta = {schedule_id, schedule_name}`），统筹照常决定怎么做。调度在控制台进程里：一个进程持有 advisory lock `agent-teams:timers` 每 20 秒检查一次，到期的行 `FOR UPDATE SKIP LOCKED` 认领并在同一事务里推进 `next_run_at`，然后再投递，所以多副本、重启都不会重复触发；停机期间错过的多次只补发一次。投递失败时写入 `last_error`。
| GET | `/api/projects/{pid}/memory` | → `MemoryBrief[]`（`{name, description, size, updated_by, updated_at}`，MEMORY.md 在前，不含内容） |
| GET | `/api/projects/{pid}/memory/{name}` | → `MemoryFile`（`{name, description, content, updated_by, created_at, updated_at}`）；没有 404 |
| PUT | `/api/projects/{pid}/memory/{name}` | `{description?, content}` → `MemoryFile`（新建或替换，`updated_by = user`；名字不带 `.md` 会补上；主题文件必须有描述，平台同步 MEMORY.md 的索引行；名字不合法、太长、超过 200 个文件 422） |
| DELETE | `/api/projects/{pid}/memory/{name}` | 204，同时去掉 MEMORY.md 里的索引行；MEMORY.md 本身 422 |
| GET | `/api/projects/{pid}/ci/detect?repo=` | 一个仓库（缺省第一个）在其默认分支上的检测 → `{repo, branch, workflows: [{path, triggers}], covers_thread_branches: bool, covers_mq_branches: bool, app_has_checks_permission: bool, ruleset: {present: bool, recommended: {...}}}`（设置页 CI 部分的检测结果；`ruleset.recommended` 是建议的 GitHub 规则集：保护基准分支和 `thread/*`，绕过者为平台 App，见 `architecture.md` §8.1） |
| POST | `/api/projects/{pid}/ci/setup-pr?repo=` | → `{pr_url}`：平台在该仓库（缺省第一个）开一个 PR，给 workflows 加上 `thread/**`、`mq/**` 的 push 触发 |
| GET | `/api/git/connections/{id}/repos?q=&cursor=` | 仓库下拉 → 分页 `RepoItem`（带 `used_by_projects`；v2 支持分页和搜索，v1 只取前 100） |
| GET | `/api/git/connections/{id}/branches?repo=&q=` | 分支下拉 |

### 3.7 Team（团队岗位模板 + 员工形象）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/roles` | → `{roles: RoleView[], warnings: [{role, message}]}`（例如“候选节点已删除”“Codex 岗位没有 agent_user 或 isolated 节点”） |
| PUT | `/api/roles` | `{roles: RoleView[]}` 整体保存（按 `id` 匹配，缺失的删除）→ 同 GET。校验：恰好一个 coordinator 和一个 lead（两者都只能是 Claude）；`model` 为空 = 跟随默认，非空时须是自己模型列表里 `runtime` 与岗位相同的条目（与该岗位已保存的值相同且 runtime 未变时不再检查，删了模型也能照常保存其他改动）；`role` 键唯一且符合 `^[a-z][a-z0-9-]{0,23}$`；`count` 1–8；节点属于本人；`look.outfit`/`look.accessory` 是当前全局布局里存在的键；`permission_mode` 是 `bypass`/`ask`（`auto` 和空值按 `bypass` 保存），任何节点都生效，只读岗位的模式不生效；Codex 岗位的候选节点不能是 `sandbox`/`none` 级别（`architecture.md` §10.3）；`read_only` 省略时按 `gate` 取默认值；`skills` 每项须是内置或自己上传的技能，最多 16 个，统筹不能有技能；`plugins` 每项是 `name` 或 `name@N`，须是自己的或内置的插件（钉住的版本须存在），最多 8 个、不重复，统筹不能有插件；`hidden_commands` 每项须是平台命令名；`context_limit` ≥ 100000，`compact_between_pct` / `compact_in_task_pct` 1–100，`clear_after_tasks` ≥ 0，`compact_focus` 最多 2000 字（YAML 视图里是岗位的 `context: {limit, compact_between_pct, compact_in_task_pct, clear_after_tasks, focus}`，没有这一段按岗位类型取默认值；导出时没有设置的岗位写 `context: {}`，导回去仍然没有设置）；`commands` 最多 30 个，名字 `^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$`、岗位内不重复、不能是平台命令名，`mode` 是 `prompt`（`prompt` 必填，最多 8000 字，不能以 `/` 开头；`{{args}}` 换成命令后面的文字，原样一次替换，没有 `{{args}}` 时附在后面）或 `passthrough`（原样发给 Claude Code；Codex 岗位和成员不能用）。命令按员工的岗位模板现值生效（改了不用重开讨论串）；成员的命令在组长 `send_message` 和用户直接发给成员的消息以 `/名字` 开头时展开 |
| GET | `/api/models` | → `{models: [{id, runtime: claude\|codex, model, label, default, mine}]}`：自己的模型列表（按顺序；没有平台列表，`mine` 恒为 `true`，只为兼容保留）。新用户第一次读时写入内置的 `claude-opus-5-5`（Opus 5.5，Claude 默认）。/model 和岗位模型字段从这里选，也可以直接输入模型 ID（`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`） |
| PUT | `/api/models/mine` | `{models}` 整体替换自己的模型列表，返回保存后的列表（顺序即显示顺序；至少 1 个、最多 50 个；同类型内 ID 不重复；每种类型最多一个 `default`：员工、项目、岗位都没指定模型时，该用户项目里的会话用它）。审计 `models_change` |
| GET | `/api/commands` | → `{commands: CustomCommand[], platform: [平台命令名]}`：平台内置的自定义命令 |
| GET | `/api/roles/presets` | → `{roles: RoleView[]}`：Team 页“从预设添加”的现成岗位（“设计师” = `design` + `docs` 技能） |
| GET | `/api/skills` | → `{skills: [{id, name, description, builtin, size}]}`：随平台发布的技能（`id` 为空）+ 管理员发布的内置技能（`builtin`，有 `id`；自己有同名技能时不列出）+ 自己上传的 |
| POST | `/api/skills` | `{zip?: base64, files?: [{path, content}]}` → 201 `Skill`（`SKILL.md` front matter 给出 name/description；只收文本，≤ 64 个文件、≤ 512 KB） |
| DELETE | `/api/skills/{id}` | 删除自己上传的技能，并从岗位的 `skills` 里去掉 |
| GET | `/api/plugins` | → `{plugins: Plugin[]}`：自己的插件（含停用的）+ 已启用的内置插件（自己有同名插件时不列出）。`Plugin = {id, name, builtin, enabled, latest, versions: [{version, manifest_version, description, size, sha256, contents: {skills, commands, agents, hooks, mcp_servers: [名字]}, created_at}]}`（新版本在前） |
| POST | `/api/plugins` | `{zip: base64}` → 201 `Plugin`：新插件，或同名插件的新版本。校验见 architecture.md §6.5（≤ 2 MB zip、≤ 200 个文件、单个 ≤ 512 KB、合计 ≤ 1 MB、无符号链接和越界路径、合法清单）；不能与内置插件同名 → 422 |
| PATCH | `/api/plugins/{id}` | `{enabled}` → `Plugin`；停用的插件留在岗位上但不加载 |
| DELETE | `/api/plugins/{id}` | 删除自己的插件（所有版本），并从岗位的 `plugins` 里去掉 |
| POST | `/api/roles/look/random` | `{role}` → `Look`（“随机一个”，从布局的调色板和服装里选） |
| GET | `/api/roles/yaml` / PUT `/api/roles/yaml` | YAML 视图（与 v1 的 YAML 标签页对应；格式为 threads.md 的新格式，旧格式自动转换）；保存走与 PUT `/api/roles` 相同的校验 |

形象预览由前端用布局 JSON 里的 `sprites`/`looks` 本地渲染（与办公室同一套渲染代码），不需要接口。

### 3.8 Nodes（节点）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/nodes` | → `NodeView[]` |
| POST | `/api/nodes` | `{name}` → 201 `{node, token, install}`（规则同 v1：名字正则、配额、409；`install` 指向 v2 `install-node.sh`） |
| PATCH | `/api/nodes/{id}` | `{max_active_sessions?, park_after_s?, isolated?, labels?}` → `NodeView`（即时下发 `node.config`） |
| POST | `/api/nodes/{id}/token` | → `{token, install}`；轮换并立即断开 |
| POST | `/api/nodes/{id}/upgrade` | → 202；发 `node.upgrade`（版本取 `at_settings.node_release`） |
| POST | `/api/nodes/{id}/drain` | `{deadline_s}` → 202 |
| DELETE | `/api/nodes/{id}` | → 204；断开链路；该节点上的员工改派：在新节点开新会话并附交接摘要（`architecture.md` §6.2） |
| GET | `/api/nodes/{id}/sessions` | → `[{session_id, employee, project, thread_no, state, last_active_at}]` |
| GET | `/api/nodes/{id}/logs?level=&before=` | → 节点回传的 warn/error 日志 |
| GET | `/install-node.sh` | 公开；v2 安装/升级脚本 |
| GET | `/node/dist/{file}` | 公开；守护进程发布包（tgz + `.sha256`） |

### 3.9 GitHub（连接）

与 v1 相同（路径、行为、重定向目标），只列变化：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/git/connections` | v1；`GitConnectionView` 加 `permissions`、`needs_permission_approval: bool`（安装上缺 `checks:read`）、`installation_settings_url`（GitHub 页“在 GitHub 上调整仓库”链接） |
| POST | `/api/git/connections` | v1（粘贴 PAT）；v2 网页提供表单 |
| DELETE | `/api/git/connections/{id}` | v1 |
| GET | `/api/git/github-app` | v1；加 `permissions`、`webhook_active` |
| GET | `/api/git/github-app/install` | v1（重定向） |
| GET | `/api/git/github-app/created` | v1（manifest 回调，签名 state，路径不可变） |
| GET | `/api/git/github-app/installed` | v1（安装回调，路径不可变） |
| POST | `/api/git/github-app/webhook` | 新；`X-Hub-Signature-256` 校验；处理 `check_run`、`check_suite`、`pull_request`、`installation`（权限变化时刷新 `permissions`） |
| POST | `/api/git/connections/{id}/refresh` | 新；重新读取安装权限 |

### 3.10 Admin（管理）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/admin/users` | 加 `nodes`、`running_tasks`（进行中的任务数） |
| PATCH | `/api/admin/users/{id}` | `active`、`max_nodes`、`role`、`display_name` + `max_running_threads: number|null`（覆盖全局默认） |
| DELETE | `/api/admin/users/{id}` | 删除用户（关闭其节点链路） |
| POST | `/api/admin/users/{id}/password-reset` | 新；→ `{temp_password}`（一次性显示，用户登录后必须修改） |
| PUT | `/api/admin/commands` | `{commands}` 整体替换平台内置命令（`at_settings.composer_commands`，校验同岗位命令，两种方式都可以）。审计 `builtin_commands_change` |
| GET / PUT | `/api/admin/settings` | v1 的 `{registration_open, default_max_nodes}` + `max_running_threads`（“每人最多同时运行的讨论串”，默认 3）、`delivery_default`、`office_default`、`node_release` |
| POST | `/api/admin/github-app/manifest` | v1；manifest 增加 `checks: read, statuses: read`，webhook active |
| DELETE | `/api/admin/github-app` | 删除平台 App 配置并清掉安装令牌缓存 |
| GET | `/api/admin/audit?user=&action=&cursor=` | 新；审计日志 |
| GET | `/api/admin/plugins` | → `{plugins: Plugin[]}`：所有内置插件（含停用的） |
| POST | `/api/admin/plugins` | `{zip}` → 201 `Plugin`：内置插件或它的新版本，所有用户都能勾选 |
| PATCH | `/api/admin/plugins/{id}` | `{enabled}`；停用后用户看不到，已勾选的岗位不再加载 |
| DELETE | `/api/admin/plugins/{id}` | 删除内置插件，并从所有（没有同名自有插件的）用户的岗位上去掉 |
| POST | `/api/admin/skills` | 同 `POST /api/skills` → 201：管理员发布的内置技能，所有用户都能勾选；不能与随平台发布的技能重名 |
| DELETE | `/api/admin/skills/{id}` | 删除管理员发布的内置技能，并从所有（没有同名自有技能的）用户的岗位上去掉 |
| GET / PUT | `/api/admin/voice` | 语音输入设置 `{enabled, model, base_url, noise_reduction, delay, transport: "relay"(默认，经服务器中转) \| "webrtc"(浏览器直连 OpenAI), api_key?, clear_key?}`；返回 `{…, key_set, key_last4, usable, default_model}`，从不返回 Key。`api_key` 只写：用 SecretBox 密封存 `at_settings.voice`，留空保留原 Key；没有 Key 不能启用（422）；已存 Key 时改 `base_url` 必须同时重填 Key（422），保存的 Key 只发往保存时的地址。审计 `voice_settings_change`（不含 Key） |
| POST | `/api/admin/voice/test` | `{api_key?, model?, base_url?}` → `{ok, model, expires_at}`：用填的或已存的 Key 签一个 10 秒的临时密钥试试（用已存 Key 时 `base_url` 必须与保存的一致）；每个管理员每分钟 10 次；审计 `voice_test` |
| GET | `/api/admin/office/layouts` | 新；→ `[{id, source: "file"|"db", version, updated_at, valid, errors?}]` |
| PUT | `/api/admin/office/layouts/{id}` | 新；`{doc}` → 校验（Schema + 语义检查）后存 `at_office_layouts` 并广播 `layout` 事件；422 带 `fields` |
| DELETE | `/api/admin/office/layouts/{id}` | 新；删除数据库布局（文件布局只能改文件） |

### 3.11 Office（项目办公室）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/office/layouts/{id}` | → 布局 JSON（`ETag` = 内容 hash；`If-None-Match` 304）。`id` 省略规则：项目设置 → 全局默认 → `default` |
| GET | `/api/projects/{pid}/office` | → 首屏快照 `{layout_id, layout_version, threads: ThreadView[] (活跃的), employees: [EmployeeBrief + {thread_no, status}], board: {claimable, by_thread: {...}, queue: [{thread_no, id, role, assignee, locked, reserved_for}]}, nodes: [NodeView 简版 + slots], merge_queue: MergeEntryView[], ci: {thread_no: CheckView[]}, last_event_id}`。`last_event_id` = 快照读取时（同一个 `REPEATABLE READ` 事务内）的 `at_projects.event_seq`，快照里的一切都反映到这个序号为止。之后用 `GET /api/projects/{pid}/events?topics=status,thread,task,merge,ci,node,layout` 并带 `Last-Event-ID: <last_event_id>` 增量更新。`board.claimable` 和 `board.queue` 只算成员现在能领的：运行中 / 等待中讨论串里 `pending` 的任务（排队、待验收、暂停的讨论串只在 `by_thread` 里计数）。`board.queue` 按讨论串号、再按领取顺序（`priority` 小的先，同优先级下游多的先：`downstream`，再等得久的先：`ready_at`，没有时用创建时间）排，每个讨论串最多 12 个、总共最多 120 个，后面的讨论串不会被前面的长队列挤掉：`locked` = 还不能领（`ready_at` 为空，即还有依赖没 `done`，和领取用同一个判断），`reserved_for` = 返工预留给谁（预留过期后为空），`assignee` = 点名的在职成员（岗位池、或点名的实例已撤编时为空）；网页在 `task` 事件后重新取快照（同一项目节流约 750 ms） |
| GET | `/api/projects/{pid}/office/replay?from=&to=&thread=` | → `{snapshot: (同上，取 from 时刻), events: [{id, at, type, data}]}`；单次最多 6 h，超出 422 |
| GET | `/api/employees/{id}` | → `EmployeeBrief + {current_task: TaskView|null, recent_activity: [...], session: {state, node}}`（点人物弹出的卡片） |

### 3.12 Mobile

不单独设接口：手机版用同一套接口，讨论串面板变成独立页（`/projects/:pid/threads/:no`）。为减少请求，`GET /api/threads/{tid}?include=messages,questions` 可一次带回最近 30 条消息和 open 卡片。

### 3.13 旧对话（已删除）

v1 的旧对话只读页和它的接口（`GET /api/conversations`、`…/{cid}/tasks|mail|plan|events`、`POST /api/conversations`）已随迁移 `00120_drop_v1` 删除，v1 的对话数据也一并删除；`/api/conversations/{cid}/…` 下只剩统筹和讨论串对话用的消息、提问、插话接口（§3.2）。

## 4. 网页路由（v2）

| 路由 | 画板 |
| --- | --- |
| `/login` | Login |
| `/projects` → 最近项目 | — |
| `/projects/new` | NewProject |
| `/projects/:pid` | Main（统筹对话 + 讨论串面板；`?panel=library` 右侧换成资料库，`?panel=none` 收起右侧；`?msg=<id>` 滚动到统筹对话里的这条消息并短暂高亮，讨论串里转发消息下的“已从项目对话转发 N 条消息”就链到这里）。统筹对话里自己的消息悬停出现“转发到讨论串”，点了进入多选（再点别的消息加选，输入框换成“已选 N 条 · 转发到讨论串 · 取消”，Esc 取消），“转发到讨论串”打开选择框（搜索、按讨论串列表分组、已归档的不能选、最上面“新建讨论串”带标题）。顶栏：项目图标和名字（切换项目）、资料库、讨论串面板开关、办公室、“…”菜单（项目首页 / 定时任务 / 设置） |
| `/projects/:pid/overview` | Overview |
| `/projects/:pid/threads/:no` · `/projects/:pid/threads/:no/:tab` | 宽屏（≥1024 px）：Main，右侧面板打开这个讨论串，`tab` = `progress`（默认，可省）· `tasks` · `deliverables` · `members`；窄屏：Thread / MobileThread。旧链接 `/projects/:pid?thread=N` 跳到这里 |
| `/projects/:pid/threads/:no/full` | Thread（全屏，主要任务 / 任务池 / 和组长的对话 / 交付） |
| `/projects/:pid/deliverables` | Deliverables |
| `/projects/:pid/office` | Office |
| `/projects/:pid/settings` · `/projects/:pid/settings/:section` | ProjectSettings：Main 上打开的设置对话框，分区 `general`（通用：名称图标、简介、模型、自动继续、归档删除）· `memory`（记忆：项目指令 = `context`，自动记忆 = MEMORY.md 和记忆文件）· `environment`（环境：每个讨论串都包含的仓库，工作树开关 = `work_mode`：开启 `worktree` / 关闭 `clone` 独立克隆；没有云环境或远程环境，节点在 设置 → 节点）· `usage`（使用情况）· `advanced`（高级：审批与交付、合并方式、分支命名与清理说明），关闭回到 `/projects/:pid` |
| `/settings/team` · `/settings/nodes` · `/settings/git` · `/settings/appearance` | Team · Nodes · GitHub · 外观（用户端设置，侧栏里没有管理入口） |
| `/admin` → `/admin/users` | 管理后台（只给管理员；独立侧栏，顶部“管理后台”，“返回用户端”回 `/projects`）。非管理员访问任何 `/admin/*` 看到 404 页，地址不变，不加载管理后台也不请求管理接口 |
| `/admin/users` · `/admin/platform` · `/admin/library` · `/admin/feedback` · `/admin/github-app` · `/admin/audit` | 用户 · 平台设置（注册、限额、语音输入）· 内置命令和插件（内置命令、插件、技能）· 反馈 · GitHub App（创建和状态；服务端的 manifest 回调直接跳到这里，带 `?github_app=<slug>` 或 `?error=…`）· 审计日志 |
| `/settings/admin` · `/setup/github` | 旧地址，分别重定向到 `/admin` 和 `/admin/github-app`，保留 query |
| 其他不存在的地址 | NotFound（404 页，“返回首页”到 `/projects`）。未登录访问任何非公开地址跳 `/login?from=<原地址含 query>`，登录后回去；`from` 只接受站内路径（以 `/` 开头，不能是 `//` 或 `/\`） |
