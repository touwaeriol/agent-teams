# v2 网页接口（REST + SSE）

由 Go 控制台（chi + huma）提供，OpenAPI 3.1 文档在 `GET /api/openapi.json`（网页类型由它生成）。本文按设计稿画板分组；表结构见 `data-model.md`。

## 0. 通用约定

- **前缀**：`/api`。v1 已有、语义不变的路径保持原样（迁移期 v1 网页和 v2 网页都能用）；新功能用新路径。GitHub 回调路径 `/api/git/github-app/created|installed|install` 必须与 v1 完全一致。
- **认证**：cookie `agent_teams_session`（HttpOnly、SameSite=Lax、https 时 Secure）或 `Authorization: Bearer <session token>`。失败 401 `{"detail":"请先登录"}`。
- **CSRF**：头 `X-CSRF-Token`，值等于 cookie `agent_teams_csrf`（登录时下发，非 HttpOnly）；v2 网页的所有非 GET/HEAD 请求都带。**迁移期（P1–P5）**：cookie 认证的写请求没带该头时，按同源检查放行（`Sec-Fetch-Site: same-origin`，或 `Origin` 等于 `AGENT_TEAMS_PUBLIC_URL` 的 origin），因为 v1 网页不发这个头；v2 新增的写接口和 `platform_version = 2` 用户的会话始终要求该头。**P6 起**所有 cookie 认证的写请求都要求该头。Bearer 认证的请求免 CSRF。见 `architecture.md` §10.1。
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
| `thread` | `{thread: ThreadView}` | 讨论串新建 / 状态 / PR / 计数变化 |
| `main_task` | `{thread_no, main_tasks: MainTaskView[]}` | 计划变化、自动打勾 |
| `task` | `{thread_no, task: TaskView}` | 小任务新建、领取、状态变化 |
| `question` | `{thread_no?, question: QuestionView}` | 提问 / 审批新建、答复、取消 |
| `mail` | `{thread_no, mail: MailView}` | 员工间留言 |
| `activity` | `{thread_no?, employee, text, at}` | 员工活动摘要（工具开始/结束、进度；**落库**，可回看） |
| `status` | `{employee: EmployeeRef, status: EmployeeStatusView}` | 员工状态变化（办公室；来自 `at_status_events`，与其他事件同一序号） |
| `deliverable` | `{deliverable: DeliverableView}` | 交付物登记 / 更新 |
| `merge` | `{thread_no, entry: MergeEntryView}` | 合并队列变化 |
| `ci` | `{thread_no, sha, checks: CheckView[]}` | CI 结果变化 |
| `memory` | `{item: MemoryView, op: "add"|"delete"}` | 项目记忆 |
| `node` | `{node: NodeBrief}` | 该项目用到的节点上下线、槽位变化 |
| `layout` | `{layout_id, version}` | 办公室布局更新（前端重新拉布局并重画） |
| `reset` | `{}` | 补发窗口不够，前端需整体重新拉取 |

### 1.2 用户事件流 `GET /api/events`

侧栏和设置页用：`project`（项目列表变化、等你处理数 `{project_id, waiting}`）、`node`（节点上下线）、`notice`（系统通知）。同样支持 `Last-Event-ID`（序号来自 `at_users.event_seq`，只补 1 h，之后 `reset`）。

### 1.3 只在需要时的实时输出

`GET /api/sessions/{sid}/stream`：SSE，逐字输出（`delta`），用于讨论串里“看实时输出”。打开时控制台向节点发 `session.watch on`，所有观看者断开 30 s 后 `off`。不落库、不补发。

## 2. 视图对象

```ts
UserView        = {id, email, display_name, role: "admin"|"user", active, max_nodes: number|null, platform_version: 1|2, created_at}
ProjectView     = {id, name, description, context, repo: string|null, branch, git_connection_id: string|null,
                   pinned, archived, work_mode: "worktree"|"clone", auto_approve,
                   require_acceptance,      // true：用户验收后平台合并；false：PR 的必需 check 通过后平台按 merge_method 自动合并（architecture.md §8.4）
                   delivery: DeliverySettings, office_layout: string, waiting: number, threads_active: number,
                   created_at, updated_at}
DeliverySettings = {gates: {review, decision, merge_queue}, ci: {mode, required, appear_timeout_s, timeout_s, on_missing},
                   merge_method: "squash"|"merge"|"rebase", approval_timeout_s: number|null,
                   notify_on_ask: boolean,          // “成员提问时通知我”：员工 ask_user / 审批卡片出现时发浏览器通知 + 用户事件流 notice（默认 true）
                   allow_workflow_edits: boolean}   // “允许员工修改 workflow”，默认 false（architecture.md §8.1）
ThreadView      = {id, project_id, no, title, brief, status, queue_position: number|null, pause_reason: string, branch, base_branch, pr_number, pr_url, pr_state, summary,
                   auto_approve, opened_by, members: EmployeeBrief[], main_tasks: MainTaskView[],
                   counts: {waiting, questions_open, approvals_open, tasks_total, tasks_done, main_total, main_done},
                   lead_last: {text, at} | null, cost_usd_est: number|null, created_at, updated_at}
MainTaskView    = {id, pos, title, status: "todo"|"doing"|"done", auto_done, tasks_total, tasks_done}
TaskView        = {id, thread_no, main_task_id, assignee, role, claimed_by, kind, instruction, status, depends_on: string[],
                   parent, priority, attempt, summary, branch, head_sha, created_at, claimed_at, finished_at}
QuestionView    = {id, thread_no, asker, task_id, audience: "lead"|"user", kind: "question"|"approval", text, options: string[],
                   detail: {tool_name?, input?, title?, description?, diff?, source?}, blocking, answer, status, decided_by,
                   created_at, answered_at}
MessageView     = {id, role: "user"|"leader"|"employee"|"system"|"error", author, kind: "chat"|"note"|"interrupt",
                   content, meta: {thread_no?, task_id?, opens_thread?: number}, created_at}
MailView        = {id, sender, recipient, body, created_at}
EmployeeBrief   = {id, name, tier, role, title, runtime, node: {id, name, online} | null, look: Look,
                   status: EmployeeStatusView | null}
EmployeeStatusView = {state, substate, bubble, bubble_kind, task_id, task_kind, tier, role, since, seq}
Look            = {color, outfit: string, accessory: string, hair: string|null}
                  // outfit/accessory 是字符串，不是枚举：取值 = 当前全局布局 looks.outfits / looks.accessories 的键
                  // （内置 tee|hoodie|suit|coat、none|glasses|headset|cap 必有），服务端保存时按布局校验，422 带 fields
DeliverableView = {id, project_id, thread_no, kind: string, title, url, detail, published_by, created_at, updated_at}
                  // kind 已知集合：pr | doc | design | slides | dashboard | board | data；其他字符串按“链接”卡片显示
MergeEntryView  = {id, thread_no, task_id, employee, head_branch, status, attempts, error, created_at, updated_at}
CheckView       = {name, status, conclusion, url, summary}
NodeView        = {id, name, online, since, last_seen_at, version, protocol, isolation: "agent_user"|"sandbox"|"none", runtimes: {...}, capacity: {...},
                   active_sessions, parked_sessions, queued, isolated, labels, max_active_sessions, park_after_s, created_at}
RoleView        = {id, tier, role, title, runtime, count, node_ids, description, instructions, model, effort,
                   skip_permissions, can_delegate, read_only, allowed_tools, disallowed_tools, sandbox, max_turns, gate, task_kinds, look, sort}
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
| POST | `/api/conversations/{cid}/messages` | `{content, interrupt?: bool}` → 202 `MessageView`。投递到对应员工会话（统筹或组长）。`interrupt: true` = “插话并打断当前操作” |
| POST | `/api/conversations/{cid}/interrupt` | → 202。中止该对话员工的当前回合（停止按钮） |
| GET | `/api/projects/{pid}/threads?status=&group=waiting\|active\|done` | → `ThreadView[]`（右侧讨论串列表：等你处理 / 进行中 / 已完成） |
| GET | `/api/projects/{pid}/waiting` | → `{questions: QuestionView[], reviews: ThreadView[]}`（跨讨论串的“等你处理”） |
| POST | `/api/conversations/{cid}/questions/{qid}/answer` | `{answer}`；审批用 `allow`、`deny`、`allow_always`，可附 `reason`（拒绝理由，发给员工）→ `QuestionView`（v1 路径，语义扩展） |
| GET | `/api/conversations/{cid}/questions?status=open\|all` | → `QuestionView[]`（v1 路径） |
| GET | `/api/projects/{pid}/search?q=` | 搜索消息、讨论串标题、任务、交付物（PG `tsvector` + `pg_trgm`，中文用 trigram）→ `{threads, messages, tasks, deliverables}` |

讨论串详情面板（右侧展开）用 3.3 的 `GET /api/threads/{tid}`。

### 3.3 Thread（讨论串详情：两级任务与任务池）与 MobileThread

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/projects/{pid}/threads` | `{title, brief, roles?: [{role, count}]}` → 201 `ThreadView`（用户直接开串，跳过统筹；`roles` 可临时调整本串岗位数量） |
| GET | `/api/threads/{tid}` | → `ThreadView` |
| PATCH | `/api/threads/{tid}` | `{title?, status?: "paused"|"running"|"archived", auto_approve?}` → `ThreadView`。`auto_approve` = “本讨论串内全部放行”（打开时一并放行当前所有待审批，并审计） |
| GET | `/api/threads/{tid}/main-tasks` | → `MainTaskView[]` |
| PATCH | `/api/threads/{tid}/main-tasks/{mid}` | `{status}`（用户手动打勾/取消） |
| GET | `/api/threads/{tid}/tasks?status=&main=` | → `TaskView[]`（任务池看板：可领 / 进行中 / 待审查 / 待决策 / 合并中 / 完成） |
| POST | `/api/threads/{tid}/tasks/{task}/cancel` | 取消小任务 |
| POST | `/api/threads/{tid}/tasks/{task}/retry` | 失败任务重新入池 |
| GET | `/api/threads/{tid}/members` | → `EmployeeBrief[]`（含实时状态） |
| POST | `/api/threads/{tid}/members/{name}/interrupt` | 打断某个成员的当前回合 |
| POST | `/api/threads/{tid}/members/{name}/message` | `{content}` → 直接给成员发话（以“用户”身份，进该成员会话，同时记到讨论串对话里） |
| GET | `/api/threads/{tid}/mail?before=` | → 分页 `MailView` |
| GET | `/api/threads/{tid}/activity?before=` | → 分页 activity（来自 `at_events`，v1 不落库） |
| GET | `/api/threads/{tid}/merge-queue` | → `MergeEntryView[]` |
| GET | `/api/threads/{tid}/checks` | → 最近一次合并的 `CheckView[]` |
| POST | `/api/threads/{tid}/open-pr` | `{title?, body?, force?: bool}` → `ThreadView`（Thread 页“开 PR”按钮；前提同组长 `open_pr`：无 `merging` 条目、主任务全部 done（`force` 可跳过这一条）；标题/正文缺省时平台由讨论串标题、主任务和小任务总结生成；409 `pr_exists`、409 `merge_in_progress`） |
| POST | `/api/threads/{tid}/accept` | → `ThreadView`。`require_acceptance` 开：等 PR 必需 check 通过后按 `merge_method` 合并 → done。关：PR 本来就会在 check 通过后自动合并，accept = “不等了，现在合并”（check 未通过时 409 `checks_pending`） |
| POST | `/api/threads/{tid}/reject` | `{reason}` → `ThreadView`（原因发给组长，回到 running；`require_acceptance` 关时也可用，在自动合并前拦下） |
| GET | `/api/threads/{tid}/usage` | → `{cost_usd_est, tokens: {...}, by_employee: [...]}` |

和组长的对话就是该讨论串的 `conversation_id`（= `tid`），复用 3.2 的消息接口。

### 3.4 Overview（项目首页：讨论串一览）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/projects/{pid}` | → `ProjectView` |
| GET | `/api/projects/{pid}/overview` | → `{project: ProjectView, threads: ThreadView[], waiting: {questions, reviews}, recent_deliverables: DeliverableView[], memory: MemoryView[] (最近 5 条), usage_7d: {cost_usd_est, by_day: [...]}, nodes: NodeBrief[]}`（一个请求出首屏） |

### 3.5 Deliverables（交付物一览）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/projects/{pid}/deliverables?kind=&thread=&q=&cursor=` | → 分页 `DeliverableView`；`q` 搜索标题和 `detail.summary`（`pg_trgm`） |
| POST | `/api/projects/{pid}/deliverables` | `{kind, title, url, thread_no?}` → 201（用户手动登记链接） |
| POST | `/api/projects/{pid}/deliverables/request` | `{kind, brief}` → 202 `MessageView`（“新做一个”磁贴） |
| PATCH / DELETE | `/api/deliverables/{id}` | 改标题 / 删除 |
| GET | `/api/deliverables/{id}/files?version=` | 托管交付物（`detail.hosted`）的一版（默认最新）→ `{deliverable_id, version, versions: [{version, files, size, published_by, created_at}], entry, render: html\|markdown\|image\|pdf\|text, files: [{path, content_type, size, sha256}], base_url, entry_url, expires_at, isolated}`；`base_url` 是 2 小时有效的签名目录 URL（`architecture.md` §6.5），`isolated` = 配置了 `AGENT_TEAMS_ARTIFACT_ORIGIN` |
| GET | `/api/deliverables/{id}/comments` | → `{items: [{id, version, text, sent_to, created_at}]}` |
| POST | `/api/deliverables/{id}/comments` | `{text, version?}` → 201：保存并作为消息投递给发布者（不在了给组长），`sent_to` 是收到它的员工 |
| GET | `/files/<token>/<path>` | 签名 URL 下的文件（不需要 cookie；CSP sandbox、nosniff，见 `architecture.md` §6.5） |

**“新做一个”磁贴**（设计、文档、幻灯片、看板 dashboard、白板 board）：不是新流程，而是给统筹发一条模板消息。`POST …/deliverables/request` 等价于 `POST /api/conversations/<统筹对话>/messages`，正文 = `做一个<kind 的中文名>：<brief>`（例如“做一个幻灯片：给投资人的 v2 进展介绍”），`meta.request_kind = <kind>`；统筹照常决定是否开讨论串，产出由员工 `publish_deliverable(kind, …)` 登记。前端在弹出框里收 `brief`，提交后跳到 Main 页的统筹对话。

PR 类交付物的 `detail.state` 由平台轮询 / webhook 更新。设计类和托管交付物（员工用 `publish_file` 发布）在查看器 DesignViewer（`/projects/:pid/deliverables/:id`）里打开：HTML 在 sandbox iframe 里，Markdown 安全渲染，图片内联，可切换版本，批注发回给员工。

### 3.6 NewProject 与 ProjectSettings

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/projects` | `{name, description?, context?, repo?, branch?, git_connection_id?, work_mode?, auto_approve?, require_acceptance?, delivery?, first_message?}` → 201 `ProjectView`。`first_message` 有值时投递给统筹（v1 是新建对话再发消息） |
| PATCH | `/api/projects/{pid}` | v1 字段（`name, description, context, repo, branch, git_connection_id, pinned, archived, clear_repo`）+ `work_mode, auto_approve, require_acceptance, delivery, office_layout`（`delivery.notify_on_ask` = “成员提问时通知我”；`auto_approve` 要求节点隔离级别满足 `architecture.md` §10.3，否则 409 `isolation_required`） |
| GET | `/api/projects/{pid}/memory` | → `MemoryView[]`（`{id, text, source, pinned, created_at}`） |
| POST | `/api/projects/{pid}/memory` | `{text}` → 201（source = user） |
| PATCH / DELETE | `/api/projects/{pid}/memory/{id}` | `{text?, pinned?}` / 删除 |
| GET | `/api/projects/{pid}/ci/detect` | → `{workflows: [{path, triggers}], covers_thread_branches: bool, covers_mq_branches: bool, app_has_checks_permission: bool, ruleset: {present: bool, recommended: {...}}}`（设置页 CI 部分的检测结果；`ruleset.recommended` 是建议的 GitHub 规则集：保护基准分支和 `thread/*`，绕过者为平台 App，见 `architecture.md` §8.1） |
| POST | `/api/projects/{pid}/ci/setup-pr` | → `{pr_url}`：平台开一个 PR，给 workflows 加上 `thread/**`、`mq/**` 的 push 触发 |
| GET | `/api/git/connections/{id}/repos?q=&cursor=` | 仓库下拉 → 分页 `RepoItem`（带 `used_by_projects`；v2 支持分页和搜索，v1 只取前 100） |
| GET | `/api/git/connections/{id}/branches?repo=&q=` | 分支下拉 |

### 3.7 Team（团队岗位模板 + 员工形象）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/roles` | → `{roles: RoleView[], warnings: [{role, message}]}`（例如“跳过权限确认的岗位没有 isolated 节点”“候选节点已删除”） |
| PUT | `/api/roles` | `{roles: RoleView[]}` 整体保存（按 `id` 匹配，缺失的删除）→ 同 GET。校验：恰好一个 coordinator（Claude）和一个 lead；`role` 键唯一且符合 `^[a-z][a-z0-9-]{0,23}$`；`count` 1–8；节点属于本人；`look.outfit`/`look.accessory` 是当前全局布局里存在的键；`skip_permissions` 时候选节点须为 `agent_user` 或 `isolated`，Codex 岗位的候选节点不能是 `sandbox`/`none` 级别（`architecture.md` §10.3）；`read_only` 省略时按 `gate` 取默认值；`skills` 每项须是内置或自己上传的技能，最多 16 个，统筹不能有技能 |
| GET | `/api/roles/presets` | → `{roles: RoleView[]}`：Team 页“从预设添加”的现成岗位（“设计师” = `design` + `docs` 技能） |
| GET | `/api/skills` | → `{skills: [{id, name, description, builtin, size}]}`：内置技能（`id` 为空）+ 自己上传的 |
| POST | `/api/skills` | `{zip?: base64, files?: [{path, content}]}` → 201 `Skill`（`SKILL.md` front matter 给出 name/description；只收文本，≤ 64 个文件、≤ 512 KB） |
| DELETE | `/api/skills/{id}` | 删除上传的技能，并从岗位的 `skills` 里去掉 |
| POST | `/api/roles/look/random` | `{role}` → `Look`（“随机一个”，从布局的调色板和服装里选） |
| GET | `/api/roles/yaml` / PUT `/api/roles/yaml` | YAML 视图（与 v1 的 YAML 标签页对应；格式为 threads.md 的新格式，旧格式自动转换） |
| GET | `/api/team` / PUT `/api/team` | v1 兼容路径（v1 用户在迁移期使用；v2 用户调用时 GET 返回由 `at_roles` 生成的 YAML，PUT 转发到 `/api/roles/yaml`） |

形象预览由前端用布局 JSON 里的 `sprites`/`looks` 本地渲染（与办公室同一套渲染代码），不需要接口。

### 3.8 Nodes（节点）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/nodes` | → `NodeView[]`（v1 路径；v2 字段是 v1 的超集。迁移期协议 1 的链路，`online`/`since`/`active` 从 Python 内部接口合并而来） |
| POST | `/api/nodes` | `{name}` → 201 `{node, token, install}`（规则同 v1：名字正则、配额、409；`install` 指向 v2 `install-node.sh`） |
| PATCH | `/api/nodes/{id}` | `{max_active_sessions?, park_after_s?, isolated?, labels?}` → `NodeView`（即时下发 `node.config`） |
| POST | `/api/nodes/{id}/token` | → `{token, install}`；轮换并立即断开 |
| POST | `/api/nodes/{id}/upgrade` | → 202；发 `node.upgrade`（版本取 `at_settings.node_release`） |
| POST | `/api/nodes/{id}/drain` | `{deadline_s}` → 202 |
| DELETE | `/api/nodes/{id}` | → 204；断开链路（协议 1 链路经 Python 内部接口断开，§14.5）；该节点上的员工改派：在新节点开新会话并附交接摘要（`architecture.md` §6.2） |
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
| GET | `/api/admin/users` | v1；加 `platform_version`、`running_tasks`（v1 中进行中的任务数，切换前检查） |
| PATCH | `/api/admin/users/{id}` | v1 字段 + `platform_version: 1|2`（迁移期切换；切到 2 时执行团队转换，有 running 任务时 409，`force: true` 可强制；切回 1 时按 `architecture.md` §14.4 暂停该用户的 v2 讨论串；任何切换都关闭该用户的节点链路让其重新协商）+ `max_running_threads: number|null`（覆盖全局默认）。停用用户时同步调用 Python 的切断接口（§14.5） |
| DELETE | `/api/admin/users/{id}` | v1；删除前调用 Python 的切断接口 |
| POST | `/api/admin/users/{id}/password-reset` | 新；→ `{temp_password}`（一次性显示，用户登录后必须修改） |
| GET / PUT | `/api/admin/settings` | v1 的 `{registration_open, default_max_nodes}` + `max_running_threads`（“每人最多同时运行的讨论串”，默认 3）、`delivery_default`、`office_default`、`node_release` |
| POST | `/api/admin/github-app/manifest` | v1；manifest 增加 `checks: read, statuses: read`，webhook active |
| DELETE | `/api/admin/github-app` | v1；同时让 Python 清掉安装令牌缓存（迁移期，`architecture.md` §14.5） |
| GET | `/api/admin/audit?user=&action=&cursor=` | 新；审计日志 |
| GET | `/api/admin/office/layouts` | 新；→ `[{id, source: "file"|"db", version, updated_at, valid, errors?}]` |
| PUT | `/api/admin/office/layouts/{id}` | 新；`{doc}` → 校验（Schema + 语义检查）后存 `at_office_layouts` 并广播 `layout` 事件；422 带 `fields` |
| DELETE | `/api/admin/office/layouts/{id}` | 新；删除数据库布局（文件布局只能改文件） |

### 3.11 Office（项目办公室）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/office/layouts/{id}` | → 布局 JSON（`ETag` = 内容 hash；`If-None-Match` 304）。`id` 省略规则：项目设置 → 全局默认 → `default` |
| GET | `/api/projects/{pid}/office` | → 首屏快照 `{layout_id, layout_version, threads: ThreadView[] (活跃的), employees: [EmployeeBrief + {thread_no, status}], board: {claimable, by_thread: {...}}, nodes: [NodeView 简版 + slots], merge_queue: MergeEntryView[], ci: {thread_no: CheckView[]}, last_event_id}`。`last_event_id` = 快照读取时（同一个 `REPEATABLE READ` 事务内）的 `at_projects.event_seq`，快照里的一切都反映到这个序号为止。之后用 `GET /api/projects/{pid}/events?topics=status,thread,task,merge,ci,node,layout` 并带 `Last-Event-ID: <last_event_id>` 增量更新 |
| GET | `/api/projects/{pid}/office/replay?from=&to=&thread=` | → `{snapshot: (同上，取 from 时刻), events: [{id, at, type, data}]}`；单次最多 6 h，超出 422 |
| GET | `/api/employees/{id}` | → `EmployeeBrief + {current_task: TaskView|null, recent_activity: [...], session: {state, node}}`（点人物弹出的卡片） |

### 3.12 Mobile

不单独设接口：手机版用同一套接口，讨论串面板变成独立页（`/projects/:pid/threads/:no`）。为减少请求，`GET /api/threads/{tid}?include=messages,questions` 可一次带回最近 30 条消息和 open 卡片。

### 3.13 旧对话（v1 conversations，只读）

v2 用户在项目侧栏的“旧对话”分组和 `/projects/:pid/legacy/:cid` 页查看自己在 v1 里的对话。迁移期这些接口由 Go 直接从 v1 表读取（不再代理给 Python，因为 v2 用户的请求不进 Python），P6 后保留：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/conversations?project_id=&kind=chat` | → v1 形状的对话列表（`kind` 缺省为 `chat`；v2 用户只能拿到 `kind='chat'` 的行，统筹和讨论串对话不出现在这里） |
| GET | `/api/conversations/{cid}/messages` | 同 3.2（只读） |
| GET | `/api/conversations/{cid}/events` | v1 的对话 SSE：对旧对话只发一次当前状态后保持空闲（旧对话不会再有新事件） |
| GET | `/api/conversations/{cid}/tasks` | v1 形状的任务列表（`TaskView` 的 `status` 为字符串，不限枚举） |
| GET | `/api/conversations/{cid}/mail` | v1 形状的留言 |
| GET | `/api/conversations/{cid}/plan` | v1 的 `plan` JSONB |
| GET | `/api/conversations/{cid}/questions?status=` | 同 3.2（只读；v1 遗留的 open 提问在切换时已标 `cancelled`） |

- v2 用户对 `kind='chat'` 对话的任何写请求（`POST …/messages`、`…/answer`、`…/interrupt` 等）以及 `POST /api/conversations`（新建 v1 对话）→ **409** `{"code":"legacy_read_only","detail":"旧对话只读"}`。
- 重命名和删除旧对话：v1 本来就不支持，v2 **明确不提供**（旧对话随项目删除而删除；P6 后如有需要再加）。
- v1 用户（`platform_version = 1`）的 `/api/conversations*` 照旧代理给 Python。

## 4. 网页路由（v2）

| 路由 | 画板 |
| --- | --- |
| `/login` | Login |
| `/projects` → 最近项目 | — |
| `/projects/new` | NewProject |
| `/projects/:pid` | Main（统筹对话 + 讨论串面板） |
| `/projects/:pid/overview` | Overview |
| `/projects/:pid/threads/:no` | Thread（全屏） / MobileThread |
| `/projects/:pid/deliverables` | Deliverables |
| `/projects/:pid/office` | Office |
| `/projects/:pid/settings` | ProjectSettings |
| `/projects/:pid/legacy/:cid` | 旧对话（只读） |
| `/settings/team` · `/settings/nodes` · `/settings/git` · `/settings/appearance` · `/settings/admin` · `/setup/github` | Team · Nodes · GitHub · 外观 · Admin · GitHub App 创建 |
