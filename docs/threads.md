# 项目统筹、讨论串与两级任务（实现约定）

界面设计稿：https://claude.ai/artifact/ETVxwwc77PFUpjsai9mmoi （17 个画板；本约定里的页面名都指那里的画板）。
员工通信、审批、Codex 的既有约定见 `docs/agent-comms.md`，本文在其上扩展，不推翻。

## 角色与层级

| 层级 | 谁 | 运行位置 | 能做 | 不能做（平台强制） |
| --- | --- | --- | --- | --- |
| 项目统筹 | 每个项目 1 个 Claude Code 会话 | 节点 `projects/<pid>/coordinator`（空目录，不克隆仓库） | 只有统筹工具（下表） | 不读写文件、不执行命令：`--disallowedTools Bash,Edit,Write,MultiEdit,NotebookEdit,Read,Glob,Grep` + `--permission-mode plan`，`--allowedTools` 只列统筹 MCP 工具 |
| 组长 | 每个讨论串 1 个 Claude Code 会话（就是现在的 Leader） | 节点上讨论串的 worktree（只读） | 现有负责人工具 + 新增工具；可 Read/Glob/Grep 看代码 | 不改文件：`--disallowedTools Edit,Write,MultiEdit,NotebookEdit` 且 Bash 只放行 `git log/diff/show/status` |
| 成员 | 每个讨论串按岗位模板实例化（dev-1、dev-2、qa-1…） | 各自 worktree / 克隆 | 现有员工工具 | 按岗位配置 |

## 团队配置 = 岗位模板（替换现在的 leader + employees）

`PUT /api/team` 的结构化格式（YAML 同构）：

```yaml
coordinator: {node: rc-erio, model: "", instructions: ""}          # 可选，默认取第一个在线节点
lead:        {node: rc-erio, model: "", instructions: "", type: claude_code}
members:
  - {role: dev, title: 开发, count: 2, nodes: [rc-erio, cvp], type: claude_code,
     description: "...", instructions: "...", permission_mode: auto, can_delegate: false,
     allowed_tools: [], model: ""}
  - {role: qa, title: 测试, count: 1, nodes: [cvp], type: codex, sandbox: workspace-write, permission_mode: ask}
```

- 旧格式（`leader` + `employees`）仍能读：自动转成 lead = leader 那个员工，其余每人一个 `count: 1` 的岗位（role = 原名字）。保存时写新格式。
- 实例化：讨论串开工时，每个岗位按 `count` 生成 `<role>-<n>`，节点在 `nodes` 里轮流分配。
- `permission_mode`：`auto`（默认，Claude Code 的判断模型自动放行或拦下，拿不准的走审批；Codex `on-request`）、`ask`（每次都问；Codex `untrusted`）、`bypass`（Claude Code `bypassPermissions`，Codex `danger-full-access`，只在隔离节点生效，否则按 `auto`）。旧的 `skip_permissions: true` 读作 `bypass`。详见 v2/architecture.md §9.1。

## 数据

- `at_projects` 加：`work_mode TEXT DEFAULT 'worktree'`（`worktree` | `clone`）、`auto_approve BOOLEAN DEFAULT FALSE`（新讨论串默认全部放行）、`require_acceptance BOOLEAN DEFAULT TRUE`、`memory JSONB DEFAULT '[]'`（`[{text, source}]`，source 为 `user` 或 `#<讨论串号>`）、`thread_seq INTEGER DEFAULT 0`。
- `at_conversations` 加：`kind TEXT DEFAULT 'chat'`（`coordinator` | `thread` | `chat`，旧数据都是 chat，界面不再展示 chat）。每个项目最多一个 `coordinator`（部分唯一索引）。
- 新表 `at_threads`：`id`（= 它的 conversation id）、`project_id`、`user_id`、`no INT`（项目内序号）、`title`、`brief`（统筹给组长的说明）、`status`（`queued` | `running` | `waiting` | `review` | `done` | `failed` | `paused` | `archived`）、`branch`（`thread/<no>-<slug>`）、`pr_number INT`、`pr_url`、`summary`（组长的结束总结）、`auto_approve BOOLEAN`、`members JSONB`（实例列表 `[{name, role, title, node, type}]`）、`created_at`、`updated_at`。唯一 `(project_id, no)`。
- `at_tasks` 加：`main TEXT DEFAULT ''`（所属主要任务标题）、`role TEXT DEFAULT ''`（按岗位领取时的岗位）、`claimed_by TEXT DEFAULT ''`。
- 交付物 `at_deliverables`：`id`、`project_id`、`thread_id`、`kind`（`pr` | `doc` | `design` | `data`）、`title`、`url`、`detail JSONB`、`created_at`、`updated_at`。PR 由平台开 PR 时自动登记；成员/组长可用 `publish_deliverable` 登记链接类交付物。

## 主要任务与小任务

- 主要任务 = 讨论串的计划（已有 `update_plan`，步骤 `{title, status: todo|doing|done}`）。组长开工第一件事是 `update_plan`。
- 小任务 = 任务板上的任务，`assign_task` 增加参数 `main`（属于哪个主要任务，标题）、`assignee` 可以写**岗位名**（如 `dev`）或实例名（如 `dev-2`）。写岗位时任务进“任务池”（状态 `pending`、`claimed_by` 空），任务板调度：该岗位有空闲实例（没有 running 任务）就领走（写 `claimed_by`，转 running）；有依赖未完成的先不领。写实例名就是点名指派。
- 一个主要任务下的小任务全部 done 时，平台把该计划步骤自动标成 done（组长也可以自己改）。

## 分支、worktree 与交付

守护进程 `start` 的 `repo` 扩展为：

```json
{"url": "...", "branch": "<要检出的分支>", "base": "<从哪个分支创建，若 branch 不存在>",
 "mode": "worktree" | "clone", "shared": "projects/<pid>/repo"}
```

- `clone`：沿用现在的行为（目录为空就 `git clone --branch base`，再 `git checkout -B branch`）。
- `worktree`：`shared` 目录不存在就 `git clone` 一份主仓库；之后 `git -C shared fetch origin`；目标目录不是 worktree 时 `git -C shared worktree add -B <branch> <cwd> origin/<base>`（远端已有 branch 时用它）。
- 目录：统筹 `projects/<pid>/coordinator`（无仓库）；组长 `projects/<pid>/threads/<no>/lead`，分支 = 讨论串分支；成员 `projects/<pid>/threads/<no>/<name>`，分支 `thread/<no>/<name>`，base = 讨论串分支。
- 讨论串开工时平台确保讨论串分支存在：GitHub API 从基准分支创建 `refs/heads/thread/<no>-<slug>`（已存在就跳过）。
- 合并：组长工具 `merge_member(member)` → 平台用 GitHub API `POST /repos/{o}/{r}/merges`（base = 讨论串分支，head = 成员分支），冲突时返回错误，组长让该成员 rebase。
- 开 PR：组长工具 `open_pr(title, body)` → GitHub API 建 PR（head 讨论串分支 → base 基准分支），写入 `at_threads.pr_*` 和交付物，讨论串状态转 `review`。
- 验收：`POST /api/threads/{id}/accept` → 若 `require_acceptance`，合并 PR（GitHub merge API），状态 `done`；`POST /api/threads/{id}/reject {reason}` → 原因发给组长，状态回 `running`。
- 清理：讨论串 done / archived 后，向用到的节点发 `cleanup` 消息删除 worktree；有未推送改动则保留并在状态里提示（守护进程回报）。

## 工具

**统筹**（新 toolkit，MCP 发布方式同负责人）：

| 工具 | 参数 | 行为 |
| --- | --- | --- |
| `open_thread` | `title`, `brief` | 新建讨论串（编号、分支、成员实例），把 brief 作为组长的第一条消息，立即返回 `#no` |
| `list_threads` | `status?` | 讨论串一行一个：编号、标题、状态、主要任务进度、待你处理数 |
| `thread_status` | `no` | 计划、最近动态、组长最近一条回复、PR |
| `message_thread` | `no`, `text` | 以“项目统筹”身份给组长发消息 |
| `ask_user` | `question`, `options?` | 同 agent-comms：不阻塞，答案回到统筹对话 |
| `remember` | `text` | 写入项目记忆（source = 统筹） |

讨论串状态变化（running→waiting/review/done/failed）时，平台向统筹对话 post 一条 `"[Thread #12 · 员工通信与审批 · review] <组长最近的结论一句话>"`，统筹据此告诉用户（它被要求只在需要用户知道时才说话）。

**组长**新增：`merge_member(member)`、`open_pr(title, body)`、`finish(summary)`（写总结，进 review 前或完成后，也写入项目记忆）、`publish_deliverable(kind, title, url)`。`assign_task` 增加 `main`、岗位名领取。

**成员**新增：`publish_deliverable(kind, title, url)`。

## 接口

- `GET /api/projects/{pid}/coordinator` → 统筹对话（conversation，不存在就创建），之后消息、SSE 复用现有 `/api/conversations/{id}/...`。
- `GET /api/projects/{pid}/threads?status=` → `ThreadView[]`；`GET /api/threads/{id}` → `ThreadView`（含 `plan`、`members`、`counts{questions_open, tasks_total, tasks_done}`）。
- `POST /api/projects/{pid}/threads {title, brief}`（用户也能直接开讨论串，跳过统筹）。
- `PATCH /api/threads/{id} {title?, status?: paused|running|archived, auto_approve?}`；`POST /api/threads/{id}/accept`、`/reject`。
- `GET /api/projects/{pid}/deliverables?kind=` → `DeliverableView[]`。
- `GET /api/projects/{pid}/waiting` → 等你处理：open 的提问/审批（跨讨论串）+ `review` 状态的讨论串。
- `PATCH /api/projects/{pid}` 增加 `work_mode`、`auto_approve`、`require_acceptance`、`memory`。
- 统筹/讨论串对话的 SSE 新事件：`{"type":"thread","thread":ThreadView}`（项目级频道 `project:<pid>` 也发，供侧栏和项目首页实时更新：`GET /api/projects/{pid}/events`）。
- `ThreadView` = `{id, project_id, no, title, brief, status, branch, pr_number, pr_url, summary, auto_approve, members, plan, counts, created_at, updated_at}`。

## 前端（按设计稿）

- 项目工作区（`Main`）：左侧 `Sidebar` 只放 新建项目 / 搜索 / 交付物 和项目列表（已置顶、项目、已归档；项目旁的数字是等你处理数），不展开讨论串；中间是和项目 leader（项目统筹）的对话，leader 开出的讨论串在消息里显示为可点的一行；右侧默认是讨论串列表（等你处理 / 进行中 / 已完成分组），点一个在右侧展开详情（主要任务清单、等你处理卡片、成员、组长最近说、和组长说话、全屏打开），左上角“← 所有讨论串”返回列表。
- 项目首页（`Overview`）、讨论串详情（`Thread`：主要任务紧凑清单 / 任务池看板 / 和组长的对话 / 交付，右侧审批、成员、动态、和组长说话；“本讨论串内全部放行”开关）、交付物一览（`Deliverables`）、新建项目（`NewProject`，含工作方式）、项目设置（`ProjectSettings`）。
- 设置（`SettingsNav`）：团队岗位（`Team`，三级 + 跳过权限确认）、节点（`Nodes`）、GitHub 连接（`GitHub`）、外观、管理（`Admin`）。登录（`Login`）。手机：侧栏收起，讨论串面板变成独立页。
- 主要任务清单的样式：完成 = 灰色实心勾 + 划线；进行中 = 转圈；未开始 = 虚线圈；超过 5 项折叠“显示另外 N 项”。
- 设计稿查看器（`DesignViewer`）本期不做：交付物里的设计类先显示为链接卡片。

## 约束

同 `docs/agent-comms.md`：ruff / mypy / pytest 全过（at-dev 容器），每个行为改动配测试；`cd web && npx tsc -b && npx vite build` 必须过；不要 commit / push / 部署。

## 后续：交付流程与项目办公室（讨论串改造之后做）

- 交付流程（项目设置里可开关的固定关卡）：拆 → 领 → 做 → 审查（审查岗）→ 决策（可选决策岗，只读只判断，拿不准升级给组长 / 用户）→ 合并队列（平台按序把成员分支合进讨论串分支）→ CI（读 GitHub Checks，失败自动生成修复小任务回池）→ 主要任务打勾 → 开 PR → 用户验收。小任务状态加 `review`（待审查）、`decision`（待决策）、`merging`（待合并）。
- 项目办公室（设计稿 `Office`）：默认所有讨论串一起，每个讨论串一个工作组区域（门牌 + 进度），共享决策室 / 等候区 / 休息区 / 机房（节点槽位灯、合并队列、CI）；可聚焦单个讨论串（其他变灰）。人物位置按状态映射，头顶气泡来自实时事件翻译；事件需落库以支持回放。
- 员工形象（设计稿 `Team`、`Avatar`）：岗位模板加 `look: {color, outfit: tee|hoodie|suit|coat, accessory: none|glasses|headset|cap}`，同岗位成员衣着一致、发色和编号自动区分，提供“随机一个”。

## 决定：守护进程改用 TypeScript + Claude Agent SDK（用户 2026-11-15）

- 节点守护进程用 TypeScript 重写（Node 22+），用 `@anthropic-ai/claude-agent-sdk` 托管员工：每个组长 / 成员一个**常驻会话**（流式输入），随时可以把派活、转达的消息、用户插话推进去；可中断当前操作。
- 权限审批改用 SDK 的 `canUseTool` 回调：回调里向控制台发审批请求、等用户在网页上点（替代 `--permission-prompt-tool`）；“跳过权限确认”的岗位直接 `bypassPermissions`。
- hooks 在 SDK 里接：工具开始 / 结束、会话停止、通知，用来驱动员工状态和办公室动画。
- 与控制台的 WebSocket 协议保持现有语义（hello / start / output / exit / cancel，按 run_id 幂等），新增 `session.*` 消息（open / send / interrupt / close）承载常驻会话。
- 先实测：SDK 能否直接使用节点上 Claude Code 已登录的订阅账号（不用 API key）。不能的话退回 `claude -p --input-format stream-json` 常驻进程，审批继续走 MCP 权限提示工具。
- Codex 员工同理用 `codex app-server`（JSON-RPC 常驻会话）或继续 `codex exec`。

## 员工状态（驱动办公室看板）

两个来源合并：
1. **平台监控（默认，无需员工配合）**：会话事件 / hooks → `typing`（Edit/Write）、`reading`（Read/Grep/Glob）、`running`（Bash，按命令识别“跑测试”“看 CI”“提交”）、`thinking`；任务板事件 → `walking_to_board` / `claiming` / `returning`；审批和提问 → `raising_hand`；没有任务 → `idle`；依赖未满足 → `waiting`；节点离线 → `offline`；出错 → `broken`。
2. **员工自己标注（MCP 工具 `set_status(text, kind?)`）**：员工在提示里被要求在切换工作内容时调用，用于气泡文字（“写迁移脚本”）；平台监控的状态优先决定位置和动作，`set_status` 只决定气泡文案。
状态变化写入事件表（落库），办公室实时订阅、支持回放。
