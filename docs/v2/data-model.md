# v2 数据模型（PostgreSQL 17）

迁移工具：goose（`/server/migrations`，SQL 迁移 + 少量 Go 迁移），版本表 `goose_db_version`；表结构只由迁移定义。查询用 GORM（`internal/store` 的模型对应这些表，不用 AutoMigrate），少数 UNION / CTE / `RETURNING` 语句手写 SQL。

## 0. 约定

- **v1 已退役（2026-11-15），表结构只为 v2 服务**：可以改列、删列。v1 遗留的清理已经做完：`00110_casbin_rule`（`at_users.role` → `casbin_rule`，§2.1）和 `00120_drop_v1`（§3.1）。下文 §2 里标“v1:”的注释只说明列的来历，其中 v1 独有的列已删除。迁移期（v1、v2 共用这个库）的“只加不改”规则和 v1 防护补丁见 `architecture.md` §14（历史）。
- 表名前缀保持 `at_`。
- 新表主键：`TEXT`，值为带类型前缀的 ULID（`th_…`、`em_…`、`s_…`），便于日志里一眼认出、按时间有序。v1 的表保持原来的 id 规则（`uuid4().hex`、对话取前 12 位、`t<n>`、`q<n>`）。
- 时间一律 `TIMESTAMPTZ NOT NULL DEFAULT now()`。
- 事件类大表按月分区（§2.9）。
- 所有引用用户数据的表都有 `user_id`（或能通过 `project_id` 追到），删除用户时级联删除。

## 1. 总览

```
at_users ─┬─ at_sessions
          ├─ at_nodes ─────────── at_node_commands, at_node_logs
          ├─ at_git_connections
          ├─ at_remote_servers（远程服务器，用户设置里配置，§2.14）
          ├─ at_ssh_audit（SSH 网关审计，§2.14）
          ├─ at_roles（岗位模板；替代 at_teams）
          └─ at_projects ─┬─ at_conversations（kind: coordinator | thread）   ─┬─ at_messages
                          │                                                        ├─ at_tasks（小任务）
                          │                                                        ├─ at_questions（提问 + 审批）
                          │                                                        └─ at_mail
                          ├─ at_threads ─┬─ at_main_tasks
                          │              ├─ at_merge_queue ── at_ci_runs
                          │              └─ at_thread_nodes（用过哪些节点，清理用）
                          ├─ at_employees ── at_agent_sessions ── at_tool_calls, at_usage
                          ├─ at_employee_status / at_status_events（办公室）
                          ├─ at_events（项目事件流）
                          ├─ at_deliverables
                          ├─ at_memory_files（MEMORY.md + 主题记忆文件）
                          ├─ at_permission_rules
                          └─ at_office_layouts（项目覆盖布局，可选）
                          ├─ at_project_servers ── at_project_server_grants（项目选用的服务器、按岗位授权，§2.14）
                          ├─ at_ssh_certs（会话证书，§2.14）
                          ├─ at_rate_limit_waits（碰到使用限额的员工，§2.10）
                          └─ at_schedules（定时任务，§2.10）
at_settings（全局键值）  at_audit_log  casbin_rule（用户 → 角色）  goose_db_version
```

## 2. 表结构

### 2.1 账号与设置（v1 表，原地扩展）

```sql
-- v1: id, email, password_hash, role, active, max_nodes, created_at（role 已由 00110 删掉，见下方 casbin_rule）
ALTER TABLE at_users
  ADD COLUMN IF NOT EXISTS platform_version SMALLINT NOT NULL DEFAULT 1,   -- 迁移期 v1/v2 分流；00120 已删除
  ADD COLUMN IF NOT EXISTS max_running_threads INT,                         -- NULL = 用 at_settings.platform.max_running_threads
  ADD COLUMN IF NOT EXISTS event_seq BIGINT NOT NULL DEFAULT 0,             -- 用户事件流序号（/api/events 的 Last-Event-ID）
  ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS password_changed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;

-- v1: token_hash PK, user_id, expires_at
ALTER TABLE at_sessions
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS csrf_secret TEXT NOT NULL DEFAULT '';         -- 空 = 首次请求时生成
CREATE INDEX IF NOT EXISTS at_sessions_expires ON at_sessions (expires_at);   -- 每日清理用

-- 00110：用户的角色（admin | user）只存在 Casbin 自己的表里，每个用户一行 g 规则：
-- (ptype 'g', v0 = 用户 id, v1 = 'admin' | 'user')。迁移时把 at_users.role 一次性搬进来
-- （不是 admin 的一律 user）后删掉该列；没有触发器、没有第二份数据、没有同步代码。
-- 只经 Casbin enforcer 读写（server/README.md “Roles live in casbin_rule”）：注册、管理员改角色、
-- 删用户（先删规则再删账号）、atctl admin set-role。路由策略（p 行）和角色继承仍嵌在
-- authz/policy.csv 里，不进库。没有外键：规则由删除用户的接口一并清掉。
CREATE TABLE casbin_rule (
  id    BIGSERIAL PRIMARY KEY,
  ptype VARCHAR(100) NOT NULL DEFAULT '',
  v0    VARCHAR(100) NOT NULL DEFAULT '', v1 VARCHAR(100) NOT NULL DEFAULT '', v2 VARCHAR(100) NOT NULL DEFAULT '',
  v3    VARCHAR(100) NOT NULL DEFAULT '', v4 VARCHAR(100) NOT NULL DEFAULT '', v5 VARCHAR(100) NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_casbin_rule ON casbin_rule (ptype, v0, v1, v2, v3, v4, v5);

-- v1: key PK, value JSONB。键：platform、github_app。platform 增加字段 max_running_threads（默认 0 = 不限）。v2 新增键：
--   office_default   {"layout": "default"}           默认布局文件名
--   delivery_default {"gates": {...}, "ci": {...}}  新项目的交付流程默认值
--   node_release     {"version": "2.0.3", "sha256": "…", "signature": "…"}   -- 签名用发布私钥离线生成，控制台不持有私钥

CREATE TABLE at_audit_log (
  id          BIGSERIAL PRIMARY KEY,
  user_id     TEXT REFERENCES at_users(id) ON DELETE SET NULL,
  actor       TEXT NOT NULL,                  -- user:<id> | system | node:<id> | employee:<id>
  action      TEXT NOT NULL,                  -- login, role_change, node_token_rotate, approval_answer, thread_accept, skip_permissions_on, …
  target_type TEXT NOT NULL DEFAULT '',
  target_id   TEXT NOT NULL DEFAULT '',
  detail      JSONB NOT NULL DEFAULT '{}',
  ip          INET,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_audit_log_user ON at_audit_log (user_id, created_at DESC);
```

### 2.2 节点

```sql
-- v1: id, user_id, name, token_hash UNIQUE, created_at, UNIQUE(user_id, name)
ALTER TABLE at_nodes
  ADD COLUMN IF NOT EXISTS max_active_sessions INT,              -- NULL = 用守护进程上报值（不报 = 不限）
  ADD COLUMN IF NOT EXISTS park_after_s INT,
  ADD COLUMN IF NOT EXISTS isolated BOOLEAN NOT NULL DEFAULT FALSE,           -- 用户声明：整台机器只给员工用
  ADD COLUMN IF NOT EXISTS isolation TEXT NOT NULL DEFAULT 'none',          -- 守护进程探测：agent_user | sandbox | none（architecture.md §10.3）
  ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS last_hello JSONB NOT NULL DEFAULT '{}', -- 最近一次 hello（版本、能力、runtimes）
  ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS epoch TEXT NOT NULL DEFAULT '',         -- 守护进程发件箱纪元
  ADD COLUMN IF NOT EXISTS ack_seq BIGINT NOT NULL DEFAULT 0,      -- 已持久处理的守护进程消息 seq
  ADD COLUMN IF NOT EXISTS cmd_seq BIGINT NOT NULL DEFAULT 0,      -- 已分配的控制台命令 seq
  ADD COLUMN IF NOT EXISTS public_key TEXT;                        -- P7：Ed25519 认证

CREATE TABLE at_node_commands (
  node_id     TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
  seq         BIGINT NOT NULL,
  type        TEXT NOT NULL,                  -- session.open, git.prepare, approval.resolve, …
  idem_key    TEXT NOT NULL,                  -- 业务幂等键
  body        JSONB NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending',-- pending | sent | acked | done | failed | expired | superseded
  result      JSONB,
  session_id  TEXT,                           -- 会话类命令：用于 open_gen 前进 / 会话关闭时批量标 superseded
  open_gen    INT,
  expires_at  TIMESTAMPTZ NOT NULL,           -- 过期不重发（protocol.md §2.1）
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  acked_at    TIMESTAMPTZ,
  PRIMARY KEY (node_id, seq),
  UNIQUE (node_id, idem_key)
);
CREATE INDEX at_node_commands_pending ON at_node_commands (node_id, seq) WHERE status IN ('pending', 'sent');

CREATE TABLE at_node_logs (
  id BIGSERIAL PRIMARY KEY,
  node_id TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
  level TEXT NOT NULL, msg TEXT NOT NULL, fields JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_node_logs_node ON at_node_logs (node_id, created_at DESC);   -- 保留 7 天

-- 00360：各节点每种运行时 CLI 的最新安装 / 登录状态（protocol.md §9.2 node.runtimes），不含凭证
CREATE TABLE at_node_runtimes (
  node_id         TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
  runtime         TEXT NOT NULL,                  -- 运行时 id：claude、codex、以后新增的
  installed       BOOLEAN NOT NULL DEFAULT false,
  version         TEXT NOT NULL DEFAULT '',
  path            TEXT NOT NULL DEFAULT '',       -- 守护进程找到的可执行文件
  auth            TEXT NOT NULL DEFAULT 'unknown',-- subscription | chatgpt | api_key | none | unknown
  auth_expires_at TIMESTAMPTZ,                    -- 守护进程能判断时才有
  expired         BOOLEAN NOT NULL DEFAULT false,
  error           TEXT NOT NULL DEFAULT '',
  checked_at      TIMESTAMPTZ NOT NULL,           -- 守护进程检测时间
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(), -- 内容最近一次变化（只更新 checked_at 不算）
  PRIMARY KEY (node_id, runtime)
);
```

收到 `node.runtimes` 就整体替换该节点的行（报告里没有的运行时删掉）；不带 `runtime_status` 的守护进程发来的 `node.runtimes` 直接忽略。调度对**在线且 hello.features 含 `runtime_status`** 的节点只看这张表：运行时 `installed`、`auth` 不是 `none`/`unknown`、没有 `expired`、`auth_expires_at` 未过，才算可用（`store.NodeRuntime.Ready`）；这张表里还没有该节点的行（首次上报还没到）时也算不可用，原因是“正在检测节点上 X 的安装和登录状态”，首次上报（`reason: hello`）一到就重新放行。其他节点照旧（`node.usage` 的 auth，没有上报就当可用）。暂存的会话见 §2.7 `at_session_holds`。

### 2.3 GitHub

```sql
-- v1 at_git_connections: id, user_id, name, provider, login, token_secret (Fernet), created_at, kind token|app, installation_id
ALTER TABLE at_git_connections
  ADD COLUMN IF NOT EXISTS permissions JSONB NOT NULL DEFAULT '{}',   -- 安装上实际授予的权限（检测 checks:read 是否已批准）
  ADD COLUMN IF NOT EXISTS checked_at TIMESTAMPTZ;
-- at_settings['github_app'] 增加字段：webhook_secret（sealed）、permissions（manifest 中申请的）
```

### 2.4 岗位模板（替代 `at_teams` YAML）

```sql
CREATE TABLE at_roles (
  id               TEXT PRIMARY KEY,                    -- ro_…
  user_id          TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  project_id       TEXT REFERENCES at_projects(id) ON DELETE CASCADE,  -- NULL = 用户级模板；非 NULL = 项目覆盖（后续）
  tier             TEXT NOT NULL,                       -- coordinator | lead | member
  role             TEXT NOT NULL,                       -- 岗位键：coordinator、lead、dev、qa、review、decision…
  title            TEXT NOT NULL DEFAULT '',            -- 显示名：开发、测试…
  runtime          TEXT NOT NULL DEFAULT 'claude',      -- claude | codex（coordinator 只能 claude）
  count            INT  NOT NULL DEFAULT 1,             -- member 每讨论串实例数；coordinator/lead 固定 1
  node_ids         TEXT[] NOT NULL DEFAULT '{}',        -- 候选节点（存 id，不存名字，节点改名不失效）
  description      TEXT NOT NULL DEFAULT '',
  instructions     TEXT NOT NULL DEFAULT '',
  model            TEXT NOT NULL DEFAULT '',            -- 手填的模型 ID（所有岗位，含统筹和组长），空 = 跟随项目指定，再落到内置默认 claude-opus-5-5，见 §2.12
  effort           TEXT NOT NULL DEFAULT '',
  permission_mode  TEXT NOT NULL DEFAULT 'bypass',      -- bypass | ask（00096 取代 skip_permissions；00097 去掉 auto，auto → bypass，
                                                        -- at_employees.spec 的副本同样改写）；只对可写的 member 生效，只读岗位可以是 bypass（不生效）
  can_delegate     BOOLEAN NOT NULL DEFAULT FALSE,
  read_only        BOOLEAN NOT NULL DEFAULT FALSE,      -- 只读岗位：组长同款策略（disallowed Edit/Write/NotebookEdit + argv Bash 白名单 + 沙箱无可写路径 + init 核对；
                                                        -- Codex = sandbox read-only）。API 保存时 gate IN ('review','decision') 且未显式给值 → true
  allowed_tools    TEXT[] NOT NULL DEFAULT '{}',
  disallowed_tools TEXT[] NOT NULL DEFAULT '{}',
  sandbox          TEXT NOT NULL DEFAULT '',            -- codex: read-only | workspace-write | danger-full-access
  max_turns        INT,
  gate             TEXT NOT NULL DEFAULT '',            -- '' | review | decision：此岗位负责的交付关卡
  task_kinds       TEXT[] NOT NULL DEFAULT '{}',        -- 偏好领取的任务类别（dev、test、fix…），空 = 按岗位名匹配
  look             JSONB NOT NULL DEFAULT '{}',         -- {"color":"#4C7BD0","outfit":"tee","accessory":"none","hair":null}；outfit/accessory 为字符串
  skills           TEXT[] NOT NULL DEFAULT '{}',        -- 技能名（内置 design/docs 或 at_skills.name），统筹必须为空（00060）
  plugins          TEXT[] NOT NULL DEFAULT '{}',        -- 插件：name（最新版本）或 name@N（钉住版本），最多 8 个，统筹必须为空（00080）
  -- 00280（shared-members.md §5.1）：会话管理，百分比都相对 context_limit（不是模型窗口）
  context_limit        INT,                             -- 岗位的上下文上限（token，保存时校验 ≥ 100000）；NULL = 模型窗口
  compact_between_pct  INT,                             -- 成员领下一个任务时已用 ≥ 上限 × 比例就先压缩；NULL = 不压缩（组长、统筹总是 NULL）
  compact_in_task_pct  INT,                             -- 任务中运行时自己压缩的比例；NULL = 运行时默认
  clear_after_tasks    INT,                             -- 同一会话做满几个任务就换新会话；0 = 不按次数，NULL = 不适用（组长、统筹）
  compact_focus        TEXT NOT NULL DEFAULT '',        -- 压缩时要保留什么，拼进压缩指令
                                                        -- 默认值：写代码的成员 60/80/8 + 默认重点，审查 / 决策 —/80/1，组长和统筹 —/70/—；
                                                        -- 迁移同时写进 at_employees.spec（员工按岗位快照用这些值）
  sort             INT NOT NULL DEFAULT 0,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (tier IN ('coordinator','lead','member')),
  CHECK (runtime IN ('claude','codex')),
  CHECK (NOT (tier = 'coordinator' AND runtime <> 'claude')),
  CHECK (permission_mode IN ('bypass','ask'))
);
CREATE UNIQUE INDEX at_roles_key ON at_roles (user_id, COALESCE(project_id, ''), role);
CREATE UNIQUE INDEX at_roles_one_coord ON at_roles (user_id, COALESCE(project_id, '')) WHERE tier = 'coordinator';
CREATE UNIQUE INDEX at_roles_one_lead  ON at_roles (user_id, COALESCE(project_id, '')) WHERE tier = 'lead';
```

`look` 的取值与办公室布局的 `looks` 对应（`office-layout.md` §4.7）：`outfit` 和 `accessory` 是**字符串**，合法值 = 当前全局布局 `looks.outfits` / `looks.accessories` 的键，由 Go 在保存时校验（库里不加 CHECK，OpenAPI 里也是 `string`，这样布局 JSON 新增一种服装，Team 页和接口不用改代码就能用）。Schema 要求布局至少包含内置的 `tee|hoodie|suit|coat` 和 `none|glasses|headset|cap`。`hair` 为空时按实例编号从布局的发色表里取。

### 2.5 项目、对话、讨论串

```sql
-- v1 at_projects: id, user_id, name, description, context（= 项目指令，§6.7）, repo, branch, git_connection_id, created_at, updated_at, pinned, archived
-- 00070 起 repo / branch / git_connection_id 不再读写（留作回滚）：仓库在 at_project_repos，基准分支写在 context 里
ALTER TABLE at_projects
  ADD COLUMN IF NOT EXISTS work_mode TEXT NOT NULL DEFAULT 'worktree',      -- worktree | clone
  ADD COLUMN IF NOT EXISTS auto_approve BOOLEAN NOT NULL DEFAULT FALSE,     -- 新讨论串默认“全部放行”
  ADD COLUMN IF NOT EXISTS require_acceptance BOOLEAN NOT NULL DEFAULT TRUE,  -- 00250 起删除，见 merge_policy
  ADD COLUMN IF NOT EXISTS thread_seq INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS event_seq BIGINT NOT NULL DEFAULT 0,             -- 项目事件序号：at_events 与 at_status_events 共用（architecture.md §11.1）
  ADD COLUMN IF NOT EXISTS delivery JSONB NOT NULL DEFAULT '{}',
    -- {"gates":{"review":true,"decision":false,"merge_queue":true},
    --  "ci":{"mode":"checks","required":[],"appear_timeout_s":300,"timeout_s":3600,"on_missing":"pass"},
    --  "merge_method":"squash","approval_timeout_s":null,
    --  "notify_on_ask":true,            -- “成员提问时通知我”
    --  "allow_workflow_edits":false,    -- 推送作业令牌是否带 workflows: write
    --  "approval_routing":"lead_first", -- 审批方式：lead_first | user（architecture.md §9.2）
    --  "lead_review_timeout_s":120}     -- 组长判断时限
    -- 讨论串开出 PR 后怎么收尾看 merge_policy（00250，architecture.md §8.4）：
    --   pr（默认）PR 的 check 通过即 done，PR 留给人合并；auto check 通过后按 merge_method 自动合并；manual 等用户验收
  ADD COLUMN IF NOT EXISTS office_layout TEXT NOT NULL DEFAULT '';          -- '' = 全局默认；否则布局 id

ALTER TABLE at_projects                                                     -- 00090
  ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT '',                   -- '' = 首字母配稳定底色；表情；或 color:#RRGGBB
  ADD COLUMN IF NOT EXISTS coordinator_model TEXT NOT NULL DEFAULT '',      -- 统筹的项目指定，'' = 统筹岗位的 model（00310 曾清空并停用，00320 起恢复）
  ADD COLUMN IF NOT EXISTS thread_model TEXT NOT NULL DEFAULT '',           -- Claude 组长和成员的项目指定，'' = 各自岗位的 model
  ADD COLUMN IF NOT EXISTS thread_codex_model TEXT NOT NULL DEFAULT '',     -- 00230：Codex 组长和成员的项目指定，'' = 跟随岗位
  ADD COLUMN IF NOT EXISTS thread_effort TEXT NOT NULL DEFAULT '',          -- '' = 岗位的 effort；low | medium | high | xhigh | max
  ADD COLUMN IF NOT EXISTS auto_resume BOOLEAN NOT NULL DEFAULT FALSE;      -- 使用限额重置后自动继续讨论串（§2.10）

CREATE TABLE at_project_repos (     -- 项目绑定的仓库（00070），position 即界面上的顺序
  project_id        TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  repo              TEXT NOT NULL,                 -- owner/name（GitHub 返回的 full_name）
  git_connection_id TEXT REFERENCES at_git_connections(id) ON DELETE SET NULL,  -- 连接被删的仓库不再使用
  position          INT  NOT NULL DEFAULT 0,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, repo)
);
-- 迁移：每个项目原来的 repo/git_connection_id 成为第一行；branch 不是 main/master 时在 context 末尾追加
-- “仓库 <repo> 的基准分支：<branch>”，信息不丢。

-- v1 at_conversations: id, user_id, title, created_at, history JSONB, plan JSONB, project_id
ALTER TABLE at_conversations   -- kind 在 00002（P1）加，v1 防护补丁按它过滤；其余在 00010
  ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'chat',               -- coordinator | thread（00120 删了 chat 行和默认值，加了 CHECK）
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE UNIQUE INDEX IF NOT EXISTS at_conversations_one_coord
  ON at_conversations (project_id) WHERE kind = 'coordinator';

CREATE TABLE at_threads (
  id             TEXT PRIMARY KEY REFERENCES at_conversations(id) ON DELETE CASCADE,  -- = 它的对话 id（12 hex，沿用 v1 规则）
  project_id     TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  user_id        TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  no             INT  NOT NULL,
  title          TEXT NOT NULL,
  brief          TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'queued',  -- queued | running | waiting | review | done | failed | paused | archived
  pause_reason   TEXT NOT NULL DEFAULT '',        -- '' | user | rolled_back_to_v1 | …
  branch         TEXT NOT NULL DEFAULT '',        -- thread/<no>-<slug>
  base_branch    TEXT NOT NULL DEFAULT '',        -- 00070 起不再写：基准分支按仓库记在 at_thread_repos
  pr_number      INT,
  pr_url         TEXT,
  pr_state       TEXT NOT NULL DEFAULT '',        -- '' | open | accepting | merged | closed：全部 PR 的汇总状态；pr_number/pr_url = 第一个 PR
  merge_error    TEXT,                            -- 00200：验收后没能合并的原因（展示给用户）；accept / reject / 合并成功时清空
  summary        TEXT NOT NULL DEFAULT '',
  auto_approve   BOOLEAN NOT NULL DEFAULT FALSE,
  opened_by      TEXT NOT NULL DEFAULT 'coordinator', -- coordinator | user
  source_message_id BIGINT REFERENCES at_messages(id) ON DELETE SET NULL, -- 00220：统筹 open_thread 时对应的用户原话（open_thread 的 message_id，必须是用户本人在该项目对话里的消息；不写则取统筹上一条回复之后送达的用户本人消息里最新的一条，定时任务等平台写入的 role=user 消息（meta.system_post / schedule_id）不算；取不到为 NULL）；手动开、转发开的为 NULL
  forwarded_brief BOOLEAN NOT NULL DEFAULT FALSE, -- 00220：brief 是转发的用户原话（forward 新开的讨论串，按 brief 前缀回填历史数据）；这类讨论串 dispatch 为 null
  roles_snapshot JSONB NOT NULL DEFAULT '[]',     -- 开串时的岗位模板快照（之后模板改动不影响进行中的串）
  waiting_count  INT NOT NULL DEFAULT 0,          -- 冗余：等你处理数（触发器/应用维护），列表排序用
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at     TIMESTAMPTZ,
  finished_at    TIMESTAMPTZ,
  archived_from  TEXT NOT NULL DEFAULT '',        -- 00095：归档前的状态（取消归档时 done/failed 回原状态，其余回 paused）
  stalled_note_at TIMESTAMPTZ,                    -- 00130：上次给组长发“守护进程重启后无人工作”检查的时间；同一次停滞只发一次（之后有用户消息/回答、任务/计划/合并/CI/PR 变化或恢复运行才再发，且 10 分钟内不重发）
  only_roles     JSONB,                           -- 00270：open_thread(roles) 限定的成员岗位（组长只能往这些岗位派活；平台建的审查 / 修复任务不受限）；NULL = 全部
  done_pending_at TIMESTAMPTZ,                    -- 00311：结束条件已满足、在等组长这一回合结束（或等它回应“（平台）可以收尾”提示）的时间；组长 turn_ended 时结束讨论串，超过 10 分钟（delivery.DoneGrace）合并循环直接结束；只读讨论串 finish 时也写它
  finish_nudged_at TIMESTAMPTZ,                   -- 00311：结束条件满足时组长还没调用 finish(summary)，平台给它发“（平台）[Thread #n · 可以收尾]”提示的时间；每个讨论串只发一次
  UNIQUE (project_id, no)
);
CREATE INDEX at_threads_project ON at_threads (project_id, status, updated_at DESC);

CREATE TABLE at_thread_repos (       -- 讨论串在每个仓库的状态（00070）；行在设基准分支、建讨论串分支时出现
  thread_id      TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
  repo           TEXT NOT NULL,
  base_branch    TEXT NOT NULL DEFAULT '',        -- 组长/统筹给的基准分支，或建讨论串分支时用的默认分支；分支建好后固定
  branch_created BOOLEAN NOT NULL DEFAULT FALSE,  -- 讨论串分支已在该仓库创建（第一次有条目合并进来时懒创建）
  pr_number      INT,
  pr_url         TEXT,
  pr_state       TEXT NOT NULL DEFAULT '',        -- '' | open | accepting | merged | closed
  pr_opened_at   TIMESTAMPTZ,                     -- 00190：开 PR 的时间，CI 只看这之后的 check；空时退回 updated_at
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (thread_id, repo)
);
CREATE INDEX at_thread_repos_pr ON at_thread_repos (pr_url) WHERE pr_url IS NOT NULL;  -- webhook 找 PR

CREATE TABLE at_thread_nodes (       -- 讨论串在哪些节点上有工作目录（清理用）
  thread_id TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
  node_id   TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
  paths     TEXT[] NOT NULL DEFAULT '{}',          -- 每个仓库的 worktree（threads/<no>/<name>/<owner>__<repo>），按共享克隆分组清理
  cleaned_at TIMESTAMPTZ,
  PRIMARY KEY (thread_id, node_id)
);
```

### 2.6 消息、任务、提问、留言（v1 表扩展）

```sql
-- v1 at_messages: id BIGSERIAL, conversation_id, role user|leader|error, content, created_at
ALTER TABLE at_messages
  ADD COLUMN IF NOT EXISTS author TEXT NOT NULL DEFAULT '',          -- 员工名（lead-12、coordinator）或 user
  ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'chat',         -- chat | note（平台插入的 [Task …]/[Thread …]）| interrupt
  ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,                  -- 已投递到会话（session.delivered）
  ADD COLUMN IF NOT EXISTS meta JSONB NOT NULL DEFAULT '{}';          -- {thread_no, task_id, msg_id(m_…)}
-- v2 的 role 取值扩展为 user | leader | employee | system | error（leader 保留指统筹/组长回复，兼容 v1 前端）

-- v1 at_tasks: PK(conversation_id, id 't<n>'), assignee, instruction, depends_on JSONB, status, output, updated_at, parent
ALTER TABLE at_tasks
  ADD COLUMN IF NOT EXISTS thread_id TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS main_task_id TEXT,                         -- at_main_tasks.id
  ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT '',             -- 进池时的岗位
  ADD COLUMN IF NOT EXISTS claimed_by TEXT NOT NULL DEFAULT '',       -- 员工名
  ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'work',         -- work | fix | review | decision | test
  ADD COLUMN IF NOT EXISTS priority INT NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS attempt INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '',          -- complete_task 的总结（output 保留给 v1 语义）
  ADD COLUMN IF NOT EXISTS branch TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS head_sha TEXT NOT NULL DEFAULT '',          -- 第一个推送的仓库的 head
  ADD COLUMN IF NOT EXISTS pushed JSONB NOT NULL DEFAULT '{}',          -- 00070：{"owner/repo": sha}，complete_task 推送了哪些仓库（合并队列按它入队）
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ;
-- 00270（shared-members.md §4）：任务进项目级队列，由项目成员领取
ALTER TABLE at_tasks
  ADD COLUMN reserved_for TEXT NOT NULL DEFAULT '',  -- 返工预留给的原成员（审查打回、CI / 冲突修复）
  ADD COLUMN reserved_until TIMESTAMPTZ,             -- 预留到期（10 分钟），过期后同岗位其他成员可领
  ADD COLUMN ready_at TIMESTAMPTZ;                   -- 依赖都满足、可领取的时刻（饿死保护按它算 5 分钟）；00300 起领取查询只看它（pending 且非空 = 可领取），不再逐个查依赖
CREATE INDEX at_tasks_queue ON at_tasks (thread_id, status, role);
-- 00290（shared-members.md §5）：领取时的压缩、任务锚点里的进度、长任务指标
ALTER TABLE at_tasks
  ADD COLUMN compact_id TEXT NOT NULL DEFAULT '',  -- 领取后先压缩：等这个 compact_id 的 session.compacted（10 分钟超时）再同步、发简报
  ADD COLUMN wait_since TIMESTAMPTZ,               -- 当前这段等待（压缩、开工前同步）从什么时候开始，超时按它算
  ADD COLUMN progress TEXT NOT NULL DEFAULT '',    -- 最近一次 report_progress（任务锚点带上）
  ADD COLUMN compactions INT NOT NULL DEFAULT 0,   -- 任务进行中运行时压缩了几次（≥ 2 时给组长发一次“考虑拆小”，split_hinted 记已发）
  ADD COLUMN ctx_start INT, ADD COLUMN ctx_end INT, -- 任务开始（简报送达）和最近一个回合结束时的上下文 token 数
  ADD COLUMN split_hinted BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX at_tasks_compact ON at_tasks (compact_id) WHERE compact_id <> '';
-- 00300（shared-members.md §4.1，阶段 4 调度优化）：关键路径计数，可领取 / 待补 ready_at 的部分索引
ALTER TABLE at_tasks
  ADD COLUMN downstream INT NOT NULL DEFAULT 0;    -- 直接或间接依赖它、还没结束（不是 done / failed / cancelled）的任务数，每个只算一次；
                                                   -- 新建、重排、进出结束状态的事务提交后整讨论串重算（core.RecountDownstream，跳过别人锁着的行），扫描每 10 秒兜底
CREATE INDEX at_tasks_ready ON at_tasks (thread_id, role) WHERE status = 'pending' AND ready_at IS NOT NULL;
CREATE INDEX at_tasks_unready ON at_tasks (updated_at) WHERE status = 'pending' AND ready_at IS NULL;  -- 调度兜底补写 ready_at 用
-- claimed_by 仍是员工名：名字解析先找讨论串自己的员工（组长、旧成员），再找项目成员
-- v2 status 取值：pending | claimed | running | blocked | review | decision | merging | done | failed | cancelled
-- 注意：pending/running 与 v1 重叠，v1 只靠防护补丁（kind='chat' 且 platform_version=1）避开 v2 行；补丁同时把 v1 TaskView.status 改为 str
CREATE INDEX IF NOT EXISTS at_tasks_pool ON at_tasks (thread_id, role, priority, created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS at_tasks_claimed ON at_tasks (thread_id, claimed_by) WHERE status IN ('claimed','running');

CREATE TABLE at_main_tasks (
  id          TEXT PRIMARY KEY,                -- mt_…
  thread_id   TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
  pos         INT NOT NULL,
  title       TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'todo',    -- todo | doing | done
  auto_done   BOOLEAN NOT NULL DEFAULT FALSE,  -- 由“全部小任务完成”自动打勾
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_main_tasks_thread ON at_main_tasks (thread_id, pos);

-- v1 at_questions: PK(conversation_id, id 'q<n>'), asker, task_id, audience, kind, text, options, detail, answer, status, created_at, answered_at
ALTER TABLE at_questions
  ADD COLUMN IF NOT EXISTS thread_id TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS project_id TEXT REFERENCES at_projects(id) ON DELETE CASCADE,  -- 跨讨论串“等你处理”查询
  ADD COLUMN IF NOT EXISTS session_id TEXT,
  ADD COLUMN IF NOT EXISTS approval_id TEXT,                          -- ap_…（幂等键）
  ADD COLUMN IF NOT EXISTS call_id TEXT,                              -- 阻塞型提问对应的 tool.call
  ADD COLUMN IF NOT EXISTS blocking BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS decided_by TEXT NOT NULL DEFAULT '';       -- user:<id> | policy:…
-- audience 取值不变：lead | user（组长、统筹的 ask_user 都直接到用户）
CREATE UNIQUE INDEX IF NOT EXISTS at_questions_approval ON at_questions (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS at_questions_open ON at_questions (project_id, created_at) WHERE status = 'open';

-- v1 at_mail: PK(conversation_id, id), sender, recipient, body, created_at, read_by JSONB —— v2 不改结构，
-- 讨论串内员工留言 conversation_id = 讨论串对话 id；recipient '*' = 本讨论串全体。
```

### 2.7 员工、会话、工具调用

```sql
CREATE TABLE at_employees (
  id          TEXT PRIMARY KEY,                    -- em_…
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,   -- NULL = 项目统筹和项目成员（所有讨论串共用，00270 起）；非空 = 组长和迁移前随讨论串建的旧成员
  current_thread_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL, -- 00270：项目成员当前在哪个讨论串干活（领任务时写，会话所在讨论串；讨论串结束时清空）。粘性和办公室显示用，不用于权限
  name        TEXT NOT NULL,                       -- coordinator | lead-12 | dev-1（项目成员 <岗位>-<n>，项目内唯一）
  tier        TEXT NOT NULL,
  role        TEXT NOT NULL,
  role_id     TEXT REFERENCES at_roles(id) ON DELETE SET NULL,
  runtime     TEXT NOT NULL,
  node_id     TEXT REFERENCES at_nodes(id) ON DELETE SET NULL,
  instance_no INT NOT NULL DEFAULT 1,
  look        JSONB NOT NULL DEFAULT '{}',         -- 解析后的形象（岗位 look + 实例发色）
  spec        JSONB NOT NULL DEFAULT '{}',         -- 开会话用的完整配置快照（model、tools、权限…）
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX at_employees_name ON at_employees (project_id, COALESCE(thread_id, ''), name);
CREATE INDEX at_employees_thread ON at_employees (thread_id) WHERE thread_id IS NOT NULL;  -- 00300：领取查询按讨论串查旧成员

CREATE TABLE at_agent_sessions (
  id              TEXT PRIMARY KEY,                -- s_…
  employee_id     TEXT NOT NULL REFERENCES at_employees(id) ON DELETE CASCADE,
  node_id         TEXT REFERENCES at_nodes(id) ON DELETE SET NULL,
  runtime         TEXT NOT NULL,
  sdk_session_id  TEXT,                            -- Claude
  codex_thread_id TEXT,                            -- Codex
  cwd             TEXT NOT NULL,
  branch          TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL DEFAULT 'opening', -- opening | active | turn_running | parked | queued | crashed | closed
  open_gen        INT NOT NULL DEFAULT 0,
  permission_mode TEXT NOT NULL DEFAULT 'default', -- 会话打开时的 Claude 模式（bypassPermissions / default / dontAsk；00097 之前的会话可能是 auto），set_mode 后更新
  init            JSONB NOT NULL DEFAULT '{}',     -- 最近一次 session.opened.init（工具核对）
  last_event_no   BIGINT NOT NULL DEFAULT 0,
  opened_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_active_at  TIMESTAMPTZ,
  closed_at       TIMESTAMPTZ,
  close_reason    TEXT NOT NULL DEFAULT '',        -- … | thread_done | switch_thread（项目成员换到别的讨论串）| context_clear（做满任务数或上下文太满，下一个任务开新会话）
  thread_id       TEXT REFERENCES at_threads(id) ON DELETE SET NULL, -- 00270：会话开在哪个讨论串的 worktree（讨论串结束按它关会话）
  in_turn         BOOLEAN NOT NULL DEFAULT false,  -- 00270：正在跑回合（turn_started 置位，turn_ended / 离开 active 清掉）；项目成员回合中不领别的讨论串的任务
  context_used    INT,                             -- 00290：最近一个回合结束时的上下文 token 数（turn_ended.context / session.compacted）
  context_max     INT,                             -- 00290：运行时衡量用的窗口（岗位没设上限时按它算比例）
  tasks_done      INT NOT NULL DEFAULT 0           -- 00290：送进这个会话的任务简报数（clear_after_tasks）
);
CREATE UNIQUE INDEX at_agent_sessions_live ON at_agent_sessions (employee_id) WHERE state <> 'closed';  -- 一个员工最多一个活会话
CREATE INDEX at_agent_sessions_node ON at_agent_sessions (node_id) WHERE state <> 'closed';

CREATE TABLE at_tool_calls (
  session_id  TEXT NOT NULL REFERENCES at_agent_sessions(id) ON DELETE CASCADE,
  call_id     TEXT NOT NULL,                       -- 守护进程生成的 tc_<ULID>（会话内唯一即可）
  tool_use_id TEXT,                                -- 可选：Claude tool_use_id（经 PreToolUse 关联）或 Codex item id，仅排查用
  tool        TEXT NOT NULL,
  args        JSONB NOT NULL,
  status      TEXT NOT NULL DEFAULT 'running',     -- running | waiting | done | failed | cancelled
  result      JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  PRIMARY KEY (session_id, call_id)
);
CREATE INDEX at_tool_calls_waiting ON at_tool_calls (session_id) WHERE status IN ('running','waiting');

CREATE TABLE at_usage (
  id          BIGSERIAL PRIMARY KEY,
  session_id  TEXT NOT NULL REFERENCES at_agent_sessions(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL,
  thread_id   TEXT,
  turn_id     TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  cost_usd    NUMERIC(12,6),                       -- 订阅账号下为估算
  input_tokens BIGINT, output_tokens BIGINT, cache_read_tokens BIGINT, cache_write_tokens BIGINT,
  duration_ms INT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (session_id, turn_id)
);
CREATE INDEX at_usage_project ON at_usage (project_id, created_at);

CREATE TABLE at_permission_rules (                 -- “总是允许”记住的规则（v1 只在内存）
  id          TEXT PRIMARY KEY,                    -- pr_…
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,   -- NULL = 全项目
  employee    TEXT NOT NULL DEFAULT '',            -- '' = 本串所有员工
  tool_name   TEXT NOT NULL,
  rule        TEXT NOT NULL DEFAULT '',            -- SDK 规则内容，如 "Bash(pnpm add:*)"；只用于展示和展开成 allowed_tools，控制台不拿它做匹配
  sdk_update  JSONB,                               -- 原样保存的 SDK PermissionUpdate（destination 已改为 session）；开会话时展开为 claude.allowed_tools
  input_hash  TEXT NOT NULL DEFAULT '',            -- 控制台侧唯一允许的自动放行：同工具 + 规范化输入的 sha256 完全相同（'' = 不做控制台侧匹配）
  created_by  TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_permission_rules_lookup ON at_permission_rules (project_id, thread_id, tool_name);

-- 00360：没有节点能跑的会话先暂存在这里（每个员工最多一行），不开会话、不发 session.open
CREATE TABLE at_session_holds (
  employee_id TEXT PRIMARY KEY REFERENCES at_employees(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,  -- 暂存时员工所在的讨论串（统筹为 NULL）
  reason      TEXT NOT NULL DEFAULT '',     -- 给用户看的原因，如“没有已登录 Codex 的在线节点”
  first       JSONB NOT NULL,               -- 会话开出来时的 first_message；之后的消息追加进来（图片最多 8 张）
  resume      JSONB,                        -- 要续接的会话（ResumeRef），没有为 NULL
  more        JSONB NOT NULL DEFAULT '[]',  -- 合并进 first 的后续消息 msg_id（开会话时一起标为已送达）
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_session_holds_project ON at_session_holds (project_id);
CREATE INDEX at_session_holds_thread ON at_session_holds (thread_id);
```

`orchestrator.pickNode` 选出的节点在线、报告了 CLI 状态（`runtime_status`），但该运行时在所有候选节点上都没装或没登录时，会话不开，消息写进 `at_session_holds`；员工状态是 `queued`、子状态 `no_runtime`，气泡是 `reason`（`bubble_kind = ask`，不会被 set_status 覆盖）。节点上线、或 `node.runtimes` 报告有变化时（`ReleaseHolds`），对用户所有暂存的会话重新选节点：能开就开（暂存的消息作为第一条消息），还不行就只更新 `reason`。暂存属于 `thread_id` 那个讨论串：项目成员换到别的讨论串（`MoveToThread`）、或者发给它的消息所在讨论串和暂存的不一样时，旧暂存丢掉（旧消息留在原讨论串的对话里），讨论串结束（done/failed/archived）时它的暂存一起丢掉。所选节点离线时照旧把会话开在它上面等它上线，这种情况不暂存、也没有 `no_runtime` 原因（员工状态是 `offline`），之后别的节点登录也不会把它接走；不带 `runtime_status` 的旧守护进程照旧可调度。

### 2.8 交付

```sql
CREATE TABLE at_merge_queue (
  id          TEXT PRIMARY KEY,                    -- mq_…
  thread_id   TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
  task_id     TEXT,                                -- 关联小任务（同 conversation 下的 t<n>）
  employee    TEXT NOT NULL,
  repo        TEXT NOT NULL DEFAULT '',            -- 00070：条目属于哪个仓库（任务改了几个仓库就几个条目；'' = 无仓库项目）
  head_branch TEXT NOT NULL,                       -- thread/12/dev-1
  head_sha    TEXT NOT NULL DEFAULT '',
  trial_branch TEXT NOT NULL DEFAULT '',           -- mq/12/<id>
  trial_sha   TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'queued',      -- queued | merging | ci_running | conflict | ci_failed | merged | cancelled
  attempts    INT NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  position    BIGSERIAL,                            -- 入队顺序
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_merge_queue_active ON at_merge_queue (thread_id, position) WHERE status IN ('queued','merging','ci_running');

CREATE TABLE at_ci_runs (
  id          BIGSERIAL PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
  queue_id    TEXT REFERENCES at_merge_queue(id) ON DELETE CASCADE,
  sha         TEXT NOT NULL,
  check_name  TEXT NOT NULL,
  check_run_id BIGINT,
  status      TEXT NOT NULL,                       -- queued | in_progress | completed
  conclusion  TEXT NOT NULL DEFAULT '',            -- success | failure | cancelled | timed_out | neutral | skipped
  url         TEXT NOT NULL DEFAULT '',
  summary     TEXT NOT NULL DEFAULT '',
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (sha, check_name, queue_id)
);

CREATE TABLE at_deliverables (
  id          TEXT PRIMARY KEY,                    -- dl_…
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE SET NULL,
  kind        TEXT NOT NULL,                       -- 已知：doc | design | slides | dashboard | board | data；其他字符串按链接显示；pr 拒绝（00190 起 PR 只记在 at_thread_repos）
  title       TEXT NOT NULL,
  url         TEXT NOT NULL DEFAULT '',
  detail      JSONB NOT NULL DEFAULT '{}',         -- pr: {number, state, additions, deletions}; 其他：{by, summary}
  published_by TEXT NOT NULL DEFAULT '',           -- 员工名或 platform
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_deliverables_project ON at_deliverables (project_id, kind, created_at DESC);
CREATE INDEX at_deliverables_title_trgm ON at_deliverables USING gin (title gin_trgm_ops);   -- 交付物搜索 ?q=
-- 托管交付物（publish_file）：url = ''，detail = {by, author, hosted: true, version, entry, content_type, render, files, size}

CREATE TABLE at_skills (                           -- 上传的技能（00060）；随控制台发布的 design/docs 不在表里
  id          TEXT PRIMARY KEY,                    -- sk_…
  user_id     TEXT REFERENCES at_users(id) ON DELETE CASCADE, -- 本人的技能；NULL = 管理员发布的内置技能（00080）
  name        TEXT NOT NULL,                       -- SKILL.md 的 name，不能与随平台发布的技能重名
  description TEXT NOT NULL DEFAULT '',
  files       JSONB NOT NULL,                      -- [{"path":"SKILL.md","content":"…"}]，只收文本
  size        INT  NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name)
);
CREATE UNIQUE INDEX at_skills_builtin_name ON at_skills (name) WHERE user_id IS NULL;

CREATE TABLE at_plugins (                          -- 插件库（00080，architecture.md §6.5）
  id          TEXT PRIMARY KEY,                    -- pl_…
  user_id     TEXT REFERENCES at_users(id) ON DELETE CASCADE, -- 本人的插件；NULL = 平台内置
  name        TEXT NOT NULL,                       -- plugin.json 的 name；agent-teams-skills 保留
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,       -- 停用：岗位勾选保留，会话不加载；停用的内置插件用户看不到
  latest      INT NOT NULL DEFAULT 0,              -- 最新版本号
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX at_plugins_user_name ON at_plugins (user_id, name) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX at_plugins_builtin_name ON at_plugins (name) WHERE user_id IS NULL;

CREATE TABLE at_plugin_versions (                  -- 每次上传一个版本；保留最近 20 个，岗位钉住的不清理
  plugin_id        TEXT NOT NULL REFERENCES at_plugins(id) ON DELETE CASCADE,
  version          INT  NOT NULL,
  manifest_version TEXT NOT NULL DEFAULT '',       -- plugin.json 的 version 原样
  description      TEXT NOT NULL DEFAULT '',
  files            JSONB NOT NULL,                 -- [{"path","content","encoding":"base64"?,"executable":true?}]
  contents         JSONB NOT NULL,                 -- {"skills":[],"commands":[],"agents":[],"hooks":bool,"mcp_servers":{name:{type,command,args,env,url,headers}}}
  size             INT  NOT NULL,                  -- 解压后字节数（≤ 1 MB）
  sha256           TEXT NOT NULL,
  created_by       TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (plugin_id, version)
);
-- 可见性：用户看到自己的插件 + 已启用且自己没有同名插件的内置插件；会话按项目所有者解析

CREATE TABLE at_deliverable_files (                -- publish_file 发布的文件，按版本保存（00060）
  deliverable_id TEXT NOT NULL REFERENCES at_deliverables(id) ON DELETE CASCADE,
  version        INT  NOT NULL,                    -- 1, 2, …；at_deliverables.detail.version 是最新一版
  path           TEXT NOT NULL,                    -- index.html、spec.md、assets/logo.svg …
  content_type   TEXT NOT NULL,                    -- 由扩展名决定
  size           INT  NOT NULL,
  sha256         TEXT NOT NULL,
  data           BYTEA NOT NULL,
  published_by   TEXT NOT NULL DEFAULT '',        -- 这一版的发布者名字（员工名或 user）
  publisher_id   TEXT NOT NULL DEFAULT '',        -- 这一版的发布员工 id（00180；'' = 用户或更早的版本）；批注投递按它找人
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (deliverable_id, version, path)
);

CREATE TABLE at_upload_chunks (                    -- file.chunk 暂存，tool.call 认领后删除；一天没认领的清掉
  upload_id  TEXT NOT NULL,
  idx        INT  NOT NULL,
  session_id TEXT NOT NULL REFERENCES at_agent_sessions(id) ON DELETE CASCADE,
  data       BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (upload_id, idx)
);

CREATE TABLE at_deliverable_comments (             -- 交付物批注，投递给被批注那一版的发布者，不在了给他讨论串的组长
  id             TEXT PRIMARY KEY,                 -- dc_…
  deliverable_id TEXT NOT NULL REFERENCES at_deliverables(id) ON DELETE CASCADE,
  version        INT,
  author         TEXT NOT NULL,                    -- 用户 id
  text           TEXT NOT NULL,
  sent_to        TEXT NOT NULL DEFAULT '',         -- 收到它的员工；空 = 没有可投递的人
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 00140 起项目记忆是文件（architecture.md §6.7），替代 00020 的 at_project_memory（一行一条笔记）。
-- 项目指令（用户写的，类似 CLAUDE.md）就是 at_projects.context，不在这张表里。
CREATE TABLE at_memory_files (
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,                       -- MEMORY.md（索引）| testing.md | thread-summaries.md …
  description TEXT NOT NULL DEFAULT '',            -- 一句话，MEMORY.md 的索引行用它；MEMORY.md 自己为空
  content     TEXT NOT NULL DEFAULT '',            -- Markdown，最多 32,000 字（应用层检查）
  updated_by  TEXT NOT NULL DEFAULT '',            -- user | platform | coordinator | lead-1 · #3
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, name)
);
```

### 2.9 事件与办公室

```sql
CREATE TABLE at_events (
  id          BIGINT GENERATED ALWAYS AS IDENTITY,
  project_id  TEXT NOT NULL,
  seq         BIGINT NOT NULL,     -- 同事务内 UPDATE at_projects SET event_seq = event_seq + 1 RETURNING 分配；SSE id = seq
  thread_id   TEXT,
  type        TEXT NOT NULL,       -- message | thread | task | main_task | question | mail | activity | deliverable | merge | ci | memory | session
  actor       TEXT NOT NULL DEFAULT '',
  payload     JSONB NOT NULL,      -- 与 SSE data 相同的 View
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX at_events_project ON at_events (project_id, seq);   -- 分区表上不能建跨分区唯一约束，(project_id, seq) 的唯一性由分配方式保证
CREATE INDEX at_events_thread  ON at_events (thread_id, id) WHERE thread_id IS NOT NULL;
-- 分区：at_events_2026_11 … 由 atctl 每日维护；保留 180 天

CREATE TABLE at_status_events (
  id          BIGINT GENERATED ALWAYS AS IDENTITY,
  project_id  TEXT NOT NULL,
  seq         BIGINT NOT NULL,     -- 与 at_events 共用的项目序号
  thread_id   TEXT,
  employee_id TEXT NOT NULL,
  tier        TEXT NOT NULL,       -- coordinator | lead | member（回放时匹配布局规则用）
  role        TEXT NOT NULL,
  state       TEXT NOT NULL,       -- typing | reading | running | thinking | walking_to_board | claiming | returning |
                                   -- raising_hand | idle | waiting | offline | broken | queued
  substate    TEXT NOT NULL DEFAULT '',   -- running: test | git_commit | git_push | ci | install
  bubble      TEXT NOT NULL DEFAULT '',
  bubble_kind TEXT NOT NULL DEFAULT 'plain',  -- plain | ask | error | done
  task_id     TEXT NOT NULL DEFAULT '',
  task_kind   TEXT NOT NULL DEFAULT '',   -- 领取时从 at_tasks.kind 复制，回放不必回查任务
  cause       TEXT NOT NULL DEFAULT '',   -- tool_started:Bash | task.claimed:t8 | approval.open:ap_… | set_status
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX at_status_events_project ON at_status_events (project_id, created_at);
CREATE INDEX at_status_events_seq ON at_status_events (project_id, seq);
CREATE INDEX at_status_events_employee ON at_status_events (employee_id, created_at DESC);
-- 保留 30 天

CREATE TABLE at_employee_status (                   -- 每个员工最新状态（办公室首屏）
  employee_id TEXT PRIMARY KEY REFERENCES at_employees(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL,
  thread_id   TEXT,
  state       TEXT NOT NULL,
  substate    TEXT NOT NULL DEFAULT '',
  bubble      TEXT NOT NULL DEFAULT '',
  bubble_kind TEXT NOT NULL DEFAULT 'plain',
  bubble_until TIMESTAMPTZ,
  task_id     TEXT NOT NULL DEFAULT '',
  task_kind   TEXT NOT NULL DEFAULT '',
  since       TIMESTAMPTZ NOT NULL DEFAULT now(),
  seq         BIGINT NOT NULL              -- 对应 at_status_events.seq
);
CREATE INDEX at_employee_status_project ON at_employee_status (project_id);

CREATE TABLE at_office_layouts (                    -- 可选：数据库里的布局（覆盖或新增），文件布局不入库
  id          TEXT PRIMARY KEY,                     -- 布局 id，如 "default"、"compact"、"proj-7a1c"
  owner_id    TEXT REFERENCES at_users(id) ON DELETE CASCADE,  -- NULL = 管理员发布的全局布局
  version     INT NOT NULL DEFAULT 1,
  doc         JSONB NOT NULL,                       -- 通过 office-layout.schema.json 校验后的布局
  updated_by  TEXT NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 2.10 定时任务与使用限额（00090）

```sql
CREATE TABLE at_rate_limit_waits (   -- 员工碰到 Claude 使用限额，等到 reset_at
  employee_id TEXT PRIMARY KEY REFERENCES at_employees(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,   -- NULL = 统筹
  reset_at    TIMESTAMPTZ NOT NULL,                               -- rate_limited.retry_at；再次碰到取较晚的
  detail      TEXT NOT NULL DEFAULT '',                           -- five_hour / seven_day …
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_rate_limit_waits_reset ON at_rate_limit_waits (reset_at);
-- 写入：session.event rate_limited 且 retry_at 至少在 2 分钟后（更短的是 SDK 自己的 API 重试）；
--   同一事务里讨论串 running/waiting/queued → paused（pause_reason = 'rate_limit'，打断各成员回合）。
-- 有行期间该员工 turn_ended 不催办（§7.3 的提醒）。
-- 到期：timers 循环（advisory lock agent-teams:timers，FOR UPDATE SKIP LOCKED）删行；项目 auto_resume
--   时恢复 pause_reason = 'rate_limit' 的讨论串，并给员工发系统消息“使用限额已重置，请继续”。

CREATE TABLE at_schedules (          -- 定时发给项目统筹的消息
  id          TEXT PRIMARY KEY,                    -- sc_…
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  cron        TEXT NOT NULL,                       -- 五段：分 时 日 月 周
  timezone    TEXT NOT NULL DEFAULT 'UTC',         -- IANA 时区（二进制内嵌 tzdata）
  message     TEXT NOT NULL,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  next_run_at TIMESTAMPTZ,                         -- 停用时 NULL
  last_run_at TIMESTAMPTZ,
  last_error  TEXT NOT NULL DEFAULT '',            -- 最近一次投递失败的原因
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_schedules_project ON at_schedules (project_id, created_at);
CREATE INDEX at_schedules_due ON at_schedules (next_run_at) WHERE enabled;
-- 触发：认领 enabled AND next_run_at <= now() 的一行（FOR UPDATE SKIP LOCKED），同一事务写 last_run_at、
--   从现在算出下一次 next_run_at，提交后把 “【定时任务：name】\nmessage” 作为用户消息投给统筹；
--   停机错过的只补一次，不会重复触发。
```

### 2.11 讨论串生命周期与消息图片（00095）

```sql
ALTER TABLE at_threads ADD COLUMN archived_from TEXT NOT NULL DEFAULT '';
CREATE TABLE at_attachments (   -- 用户附在消息上的图片，以及员工 publish_file 发的截图
  id           TEXT PRIMARY KEY,                -- img_…
  project_id   TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  user_id      TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  content_type TEXT NOT NULL,                   -- 按文件头判断：image/png | image/jpeg | image/gif | image/webp
  size         INT  NOT NULL,                   -- ≤ 5 MB
  sha256       TEXT NOT NULL,
  data         BYTEA NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

员工用 `publish_file` 发布的单张位图（截图）也存成这里的一行（`user_id` = 项目所有者），不进 `at_deliverables`，并在员工所在对话里写一条 `kind = image` 的消息卡。消息只在 `at_messages.meta.attachments` 里记 `{id, name, content_type, size}`；图片跟着项目删除。删除讨论串 = 删它的 `at_conversations` 行，`at_threads`、员工、会话、任务、提问、消息、合并队列等级联删除；`at_deliverables.thread_id` 置空（交付物留在资料库）；`at_events` / `at_status_events` 没有外键，历史事件保留。

资料库“添加”的文件（`POST /api/projects/{pid}/library`）就是一条 `at_deliverables`（`published_by = 'user'`）加一条 `at_deliverable_files` 第 1 版，只收 html、md、pdf、txt、json、csv（截图、图片和 SVG 不是交付物，拒绝），5 MB 限制、同样的签名 URL 沙箱访问。项目用量（`GET /api/projects/{pid}/usage`）直接汇总 `at_usage`，不另建表。

### 2.12 模型与自定义命令（00170、00210、00320）

```sql
ALTER TABLE at_roles ADD COLUMN hidden_commands TEXT[] NOT NULL DEFAULT '{}';  -- 聊天框不显示的平台命令
ALTER TABLE at_roles ADD COLUMN commands JSONB NOT NULL DEFAULT '[]';          -- [{name, description, mode: prompt|passthrough, prompt}]
ALTER TABLE at_employees ADD COLUMN model TEXT NOT NULL DEFAULT '';            -- /model 给这位成员选的模型（统筹和组长恒为 ''）
```

没有模型列表：`at_models`（00170 建、00210 改成每个用户自己的列表）在 00320 删除。模型 ID 都是手填的——每个岗位（含统筹和组长）的 `at_roles.model`、项目的 `coordinator_model` / `thread_model` / `thread_codex_model`、/model——只校验格式（`composer.ValidModel`：`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`），空串 = 不指定。

会话的模型（集中在 `orchestrator.Model`），取第一个非空的：

- 成员（tier `member`）：`at_employees.model`（/model）→ 项目指定（按类型：Claude 用 `thread_model`，Codex 用 `thread_codex_model`）→ 岗位 `model`（员工的 `spec.model`）→ 内置兜底。
- 组长（tier `lead`）：项目指定（`thread_model`；Codex 组长用 `thread_codex_model`）→ 岗位 `model` → 内置兜底。不看 `at_employees.model`（/model 对组长回 409）。
- 统筹（tier `coordinator`）：项目 `coordinator_model` → 岗位 `model` → 内置兜底。/model 对统筹回 409。

员工开会话用的是自己的岗位快照（`at_employees.spec`）。成员的快照在保存岗位后由 EnsureStaff 整个刷新；统筹和组长的快照创建后不再刷新，只有 `model` 一项会跟着模板走：保存岗位（`PUT /api/roles`、`/api/roles/yaml`）时，`orchestrator.RolesSaved` 把统筹模板和组长模板的 `model` 写进该用户所有统筹、以及未结束讨论串（状态不是 done/failed/archived）的组长的 `spec.model`，再把算出来的模型发给它们的活会话（`session.set_mode`）。

内置兜底：Claude 为 `claude-opus-5-5`（Opus 5.5）；Codex 为空字符串，节点不传 `model`，由 Codex CLI 用自己的默认（Codex 员工不会落到 Claude 模型名）。平台内置命令存在 `at_settings.composer_commands = {commands: [...]}`。`at_usage.model` 记每一轮实际回答的模型（`session.event turn_ended.model`）。

### 2.13 共享文件（00312）

```sql
CREATE TABLE at_shared_files (          -- 项目共享文件：项目里所有会话和用户都能存取，不进 git（讨论串 #33）
  project_id         TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  path               TEXT NOT NULL,     -- 共享区路径（相对、/ 分隔、≤ 8 层、≤ 255 字节，规则见 sharedfiles.CleanPath）
  content_type       TEXT NOT NULL,     -- 按扩展名推断，未知为 application/octet-stream
  size               BIGINT NOT NULL,   -- ≤ 20 MB
  sha256             TEXT NOT NULL,
  data               BYTEA NOT NULL,
  uploaded_by        TEXT NOT NULL,     -- 最后上传者：员工名（dev-1）或 user
  uploader_id        TEXT REFERENCES at_employees(id) ON DELETE SET NULL,  -- 员工上传时的员工，用户上传为空
  uploader_thread_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL,    -- 员工会话所在讨论串（列表显示成“dev-1 #31”），统筹和用户为空
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, path)
);
```

同一路径直接覆盖（`INSERT … ON CONFLICT DO UPDATE`，更新内容、上传者和 `updated_at`，`created_at` 保留），不做版本。上限在 `server/internal/sharedfiles`：单文件 20 MB（`MaxFileBytes`）、项目合计 500 MB（`MaxProjectBytes`）、项目文件数 1000（`MaxProjectFiles`）；覆盖时总量按差值算。写入前锁 `at_projects` 行（`SELECT … FOR UPDATE`），同项目的并发上传互相看得到总量。项目删除时级联删除。

### 2.14 远程服务器（00330，讨论串 #38，remote-servers.md）

服务器在**用户设置**里全局配置，属于用户（不属于某个项目）；项目从中选用，并按岗位授权。员工会话拿不到任何凭证：会话的 ssh 经主节点 SSH 网关登录，网关每次查库判断能不能连。

```sql
CREATE TABLE at_remote_servers (            -- 用户的服务器
  id                    TEXT PRIMARY KEY,   -- srv_…
  user_id               TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name                  TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),  -- 会话 ssh_config 里的 Host 别名，用户内唯一
  host                  TEXT NOT NULL,      -- 进凭证 AAD
  port                  INT  NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),  -- 进凭证 AAD
  username              TEXT NOT NULL,      -- 进凭证 AAD
  auth_kind             TEXT NOT NULL CHECK (auth_kind IN ('password','private_key')),  -- 进凭证 AAD
  cred_ciphertext       BYTEA NOT NULL,     -- AES-256-GCM(DEK, JSON {password} | {private_key, passphrase?})
  cred_nonce            BYTEA NOT NULL,
  wrapped_dek           BYTEA NOT NULL,     -- AES-256-GCM(KEK, DEK)，AAD 同上再加 kek_id
  dek_nonce             BYTEA NOT NULL,
  kek_id                TEXT  NOT NULL,     -- 哪把 KEK 包的（派生 KEK 的 sha256 前 8 字节，hex）
  has_passphrase        BOOLEAN NOT NULL DEFAULT FALSE,
  key_fingerprint       TEXT NOT NULL DEFAULT '',   -- 私钥对应公钥的 SHA256:…（显示用）
  host_key              TEXT NOT NULL DEFAULT '',   -- 用户确认过的目标 host key（authorized_keys 一行），'' = 未确认
  host_key_fp           TEXT NOT NULL DEFAULT '',
  host_key_mac          BYTEA,              -- HMAC(KEK 派生的 MAC 密钥, id|host|port|host_key)；网关只信 MAC 正确的 host key
  host_key_confirmed_at TIMESTAMPTZ,
  created_at, updated_at, cred_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name), UNIQUE (id, user_id)
);
CREATE UNIQUE INDEX at_projects_id_user ON at_projects (id, user_id);   -- 供下面的同属主外键

CREATE TABLE at_project_servers (           -- 项目选用的服务器；项目和服务器必须同一用户（两条复合外键保证）
  project_id, server_id, user_id TEXT NOT NULL, created_at,
  PRIMARY KEY (project_id, server_id),
  FOREIGN KEY (project_id, user_id) REFERENCES at_projects (id, user_id) ON DELETE CASCADE,
  FOREIGN KEY (server_id, user_id) REFERENCES at_remote_servers (id, user_id) ON DELETE CASCADE
);

CREATE TABLE at_project_server_grants (     -- 能用这台服务器的岗位；没有行 = 谁都不能用
  project_id, server_id, role TEXT NOT NULL, created_at,
  PRIMARY KEY (project_id, server_id, role),
  FOREIGN KEY (project_id, server_id) REFERENCES at_project_servers ON DELETE CASCADE
);

CREATE TABLE at_ssh_certs (                 -- 签发过的会话证书（吊销与追溯）
  serial BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,   -- 证书序列号
  session_id TEXT NOT NULL, open_gen INT NOT NULL, node_id TEXT NOT NULL,   -- 不设外键，会话删了也能追溯
  project_id TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id TEXT, employee_id TEXT NOT NULL DEFAULT '', role TEXT NOT NULL,
  pubkey_fp TEXT NOT NULL,                  -- 会话临时公钥的 SHA256:…
  principals TEXT[] NOT NULL,               -- 证书里的服务器名
  ca_id TEXT NOT NULL DEFAULT '',           -- 哪把会话 CA 签的（CA 轮换用）
  valid_after, valid_before TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ, revoke_reason TEXT NOT NULL DEFAULT '',   -- session_closed | gen_advanced | server_removed | server_renamed | grant_removed …
  created_at
);  -- 索引：(session_id)、(project_id) WHERE revoked_at IS NULL；(valid_before)

CREATE TABLE at_ssh_audit (                 -- 网关审计，一行一个事件，保留 365 天
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, created_at,
  user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  project_id, thread_id, session_id, employee_id, employee（名字快照）, role, node_id,
  server_id, server_name（名字快照）TEXT NOT NULL DEFAULT '',   -- 不设外键：项目、服务器、会话删了审计还在
  cert_serial BIGINT, conn_id TEXT NOT NULL DEFAULT '',          -- 同一连接的多行共用 conn_id
  kind TEXT NOT NULL CHECK (kind IN ('connect','disconnect','exec','shell','subsystem','denied','auth_fail','host_key_mismatch','test')),
  command TEXT NOT NULL DEFAULT '', command_truncated BOOLEAN NOT NULL DEFAULT FALSE,   -- 脱敏后，最多 8 KB
  exit_code INT, duration_ms BIGINT, bytes_in BIGINT NOT NULL DEFAULT 0, bytes_out BIGINT NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '', detail JSONB NOT NULL DEFAULT '{}'
);  -- 索引：(project_id, created_at DESC)、(server_id, created_at DESC)、(user_id, created_at DESC)、(created_at)
```

- **凭证**（`server/internal/credvault`，remote-servers.md §11.1 方案 B）：每条凭证一把随机 32 字节 DEK，AES-256-GCM 加密明文；DEK 再用 KEK 以 AES-256-GCM 包裹。KEK = `HKDF-SHA256(AGENT_TEAMS_SECRET_KEYS 的每一把, info="agent-teams/remote-cred-kek/v1")`，第一把加密、全部可解。AAD = `credvault.ServerAAD(user_id, id, auth_kind, host, port, username)`（各字段带长度前缀），所以密文挪到别的行、或这几项被改过（哪怕直接改库）就解不开；改这几项必须同时重新录入凭证（`store.UpdateRemoteServer` 不带新凭证时返回 `ErrCredentialRequired`），改 host 或 port 还会清掉已确认的 host key。没有接口能读回明文，只能替换。`atctl admin reseal` 只重新包裹 DEK（`credvault.Vault.Rewrap`，密文不动），并用新 KEK 重算 host key MAC。
- **网关自己的密钥**：`at_settings.ssh_session_ca`（会话 CA，给会话签用户证书）和 `at_settings.ssh_gateway_host_key`（网关 host key），值是 `credvault.SealedKey = {id, public_key, fingerprint, sealed, created_at}`，私钥（OpenSSH PEM）按同一方案密封，AAD = `credvault.SettingAAD(键名)`。第一次用到时 `credvault.EnsureKey` 自动生成（Ed25519，`INSERT … ON CONFLICT DO NOTHING`，多副本并发也只留一把），reseal 一并重新包裹。
- **授权**：统筹、组长、只读岗位（`read_only` 或 gate 为 review/decision）不能被授权——`store.GrantableRoles` 按项目的岗位副本（没有就用模板）拒绝写入；查授权时（`store.SessionServers`、`store.SessionServerGrant`）再按会话员工自己的 tier 和 `spec.read_only` 判断一次，关闭的会话一律没有。授权按岗位名，换了名字的岗位要重新授权。
- **吊销**：服务器删除或改名、从项目移除、岗位授权取消时，store 在同一事务里把相关证书标 `revoked_at` 并返回序列号（网关据此断开连接）；会话关闭、`open_gen` 前进由会话生命周期调 `RevokeSessionCerts` / `RevokeOldGenCerts`。
- **清理**：每日清理（`atctl serve` 的 `maintain` → `store.PruneSSH`）删 365 天前的审计行和过期超过 30 天的证书。审计不分区（量小）；删除用户时级联删除。

## 3. 从 v1 表的映射（历史）

切换时 v1 的表原地扩展成 v2 的表；下表是当时的处理。`00120_drop_v1` 之后，v1 独有的表和列、v1 的旧对话都已删除。

| v1 表 | v2 处理 | 时机 | 说明 |
| --- | --- | --- | --- |
| `at_users` | 原表 + 新列；`role` 搬到 `casbin_rule` 后删除（00110），`platform_version` 删除（00120） | P1 / P6 后 | Go 用 `golang.org/x/crypto/scrypt` 校验 `scrypt$N$r$p$salt$digest`（urlsafe base64 无填充），新密码同格式写入，v1 也能校验 |
| `at_sessions` | 原表 + 新列 | P1 | cookie 名 `agent_teams_session` 不变，`sha256(token)` 查找不变 → 切换无需重新登录 |
| `at_settings` | 原表 | P1 | `platform`、`github_app` 键格式不变；`github_app.client_secret`/`pem` 用 Go 的 Fernet 解封（key = `urlsafe_b64(sha256(AGENT_TEAMS_SECRET_KEY))`） |
| `at_nodes` | 原表 + 新列 | P1/P2 | `token_hash` 不变 → 旧令牌继续有效；`max_runs`（hello 里）→ `max_active_sessions` 初值 |
| `at_git_connections` | 原表 + 新列 | P1 | PAT 密文 Fernet 不变；App 连接只用 `installation_id` |
| `at_projects` | 原表 + 新列 | P3 | 默认值即旧行为（worktree、需验收）；`repo`/`branch`/`git_connection_id` 由 00070 搬进 `at_project_repos`，00120 删除 |
| `at_teams` | 已删除（00120） | — | 岗位模板只在 `at_roles`；第一次读模板时建默认团队，不再从 v1 YAML 转换 |
| `at_conversations` | 原表 + `kind`（P1）+ `updated_at`（P3） | P1/P3 | v1 的对话（`kind='chat'`）连同消息、任务、提问、留言在 00120 删除；`history`、`plan` 列也删掉；`kind` 只能是 `coordinator`/`thread`，没有默认值 |
| `at_messages` | 原表 + 新列 | P3 | 旧数据 `author=''`：role leader → 显示为“负责人” |
| `at_tasks` | 原表 + 新列 | P3 | `output`（v1 结果文本）在 00120 并入空的 `summary` 后删除 |
| `at_questions` | 原表 + 新列 | P3 | 旧 approval 的 `detail{tool_name,input}` 与 v2 相同；切换时该用户 open 的阻塞型提问标 `cancelled` |
| `at_mail` | 原表不变 | — | |
| （无） | `at_threads`、`at_main_tasks`、`at_employees`、`at_agent_sessions`、`at_tool_calls`、`at_usage`、`at_permission_rules`、`at_merge_queue`、`at_ci_runs`、`at_deliverables`、`at_project_memory`、`at_events`、`at_status_events`、`at_employee_status`、`at_office_layouts`、`at_node_commands`、`at_node_logs`、`at_audit_log`、`at_thread_nodes` | P3–P5 | 新增 |

### 3.1 goose 迁移顺序

1. `00001_v1_baseline.sql`：v1 全部 DDL 的幂等版本（`CREATE TABLE IF NOT EXISTS`、`ADD COLUMN IF NOT EXISTS`），对生产是空操作；新环境靠它建出 v1 结构。
2. `00002_accounts_ext.sql`（P1）：`at_users`/`at_sessions`/`at_nodes`/`at_git_connections` 新列（含 `at_users.platform_version`）、`at_conversations.kind`、`at_audit_log`。**部署前提**：v1 防护补丁已在生产运行。
3. `00003_nodes_protocol.sql`（P2）：`at_node_commands`、`at_node_logs`。
4. `00010_threads.sql`（P3，**v1 防护补丁上线之前禁止执行**：`atctl migrate` 检查 `at_settings.platform.legacy_guard >= 1`，由补丁后的 Python 启动时写入，不满足则拒绝）：项目/对话/消息/任务/提问的新列，`at_roles`、`at_threads`、`at_main_tasks`、`at_employees`、`at_agent_sessions`、`at_tool_calls`、`at_usage`、`at_permission_rules`、`at_events`（含首批分区）、`at_status_events`、`at_employee_status`。
5. `00020_delivery.sql`（P4）：`at_merge_queue`、`at_ci_runs`、`at_deliverables`、`at_project_memory`、`at_thread_nodes`。
6. `00030_office.sql`（P5）：`at_office_layouts`、`at_projects.office_layout`。
   `00050_node_job_index.sql`：`at_node_commands` 的作业索引。`00060_design_skills.sql`：`at_roles.skills`、`at_skills`、`at_deliverable_files`、`at_upload_chunks`、`at_deliverable_comments`（architecture.md §6.5）。`00180_deliverable_publisher.sql`：`at_deliverable_files.publisher_id`（谁发布了每一版，任何员工都可以发布下一版）。`00190_pr_not_deliverable.sql`：PR 不再是交付物——加 `at_thread_repos.pr_opened_at`，用同讨论串、同链接的 `kind = 'pr'` 交付物的 `created_at` 回填，再删掉所有 `pr` 交付物（文件、批注随外键级联删除）；Down 只删列，删掉的交付物回不来。`00210_user_models.sql`：去掉平台模型列表——把平台列表（`user_id IS NULL` 的行）复制给每个用户（同类型同 ID 已有的跳过，接在用户自己的行后面，保留顺序和显示名），再补上该用户的项目（`coordinator_model` / `thread_model`）、岗位、员工（`model`、`spec.model`）用到但列表里没有的模型 ID（只补符合模型 ID 格式的，不合法的跳过）；每人每种类型没有默认的，设成平台默认；校验每个用户都拿到了全部平台行和默认（不满足就 `RAISE EXCEPTION`，整个迁移回滚），然后删除平台行，`user_id` 改为 NOT NULL，默认改为每人每种类型唯一。不删用户数据；Down 恢复平台预置行（Opus 5.5）和旧索引，用户行保留但默认标记清掉。`00230_thread_codex_model.sql`：加 `at_projects.thread_codex_model`（Codex 组长和成员的项目指定，`''` = 跟随岗位）；组长只能用 Claude，把 `at_roles` 里 `tier = 'lead' AND runtime = 'codex'` 的行改成 `runtime = 'claude'`、`model = ''`（原值是 Codex 模型）；Down 只删列，改过的组长不还原（原值没有记录，Claude 组长在旧结构下也合法）。`00310_fixed_lead_models.sql`：统筹和组长固定用默认模型，清掉旧设置留下的数据——`at_roles` 里 `tier IN ('coordinator','lead')` 的 `model`（模板和项目副本）、`at_projects.coordinator_model`（列保留，API 仍返回 `''`）、`at_employees` 里统筹和组长的 `model`（/model 覆盖）和 `spec.model`（岗位快照）；成员不动。Down 什么也不做（清掉的值没有记录，`''` 在旧结构下也合法）。（00320 起这条规则作废：统筹和组长的岗位 `model`、项目 `coordinator_model` 重新可写可用，但 00310 清掉的值不会回来。）`00320_drop_user_models.sql`：删掉用户模型列表。删表前先回填“跟随我的默认”：把每个用户在 `at_models` 里各类型的默认（`is_default`），写进该用户 `model = ''` 的岗位（模板和项目副本，所有 tier，类型对得上）和 `spec.model` 为空或缺失的员工快照（按项目所有者，类型对得上）；Claude 默认就是内置的 `claude-opus-5-5` 时不写（空串本来就落到它，岗位继续跟随内置默认），Codex 默认总是写（Codex 内置为空）。然后 `DROP TABLE at_models`。Down 按 00210 之后的结构重建空表，回填不撤销。
7. `00120_drop_v1.sql`（v1 退役后）：删 v1 的旧对话（`kind` 不是 `coordinator`/`thread` 的 `at_conversations` 行，消息、任务、提问、留言随外键级联删除）；`at_conversations` 删 `history`、`plan`，`kind` 去掉默认值 `'chat'` 并加 `CHECK (kind IN ('coordinator','thread'))`；删 `at_teams`；`at_tasks.output` 并入空的 `summary` 后删除；删 `at_users.platform_version`；删 `at_projects.repo`/`branch`/`git_connection_id`。Down 是空操作（数据回不来，要用备份）。

`00110_casbin_rule.sql`（P6 后）：建 `casbin_rule`，每个用户按 `at_users.role` 写一行 `g, <id>, admin|user`，再删 `at_users.role`；Down 加回该列并按规则回填（§2.1）。

`00095_thread_lifecycle_images.sql`：`at_threads.archived_from`、`at_attachments`（§2.11）。

`00312_shared_files.sql`：`at_shared_files`（§2.13，项目共享文件）；Down 删表。

`00360_node_runtimes.sql`：`at_node_runtimes`（§2.2，节点 CLI 状态）和 `at_session_holds`（§2.7，暂存的会话）；Down 删两张表。

`00330_remote_servers.sql`：`at_remote_servers`、`at_project_servers`、`at_project_server_grants`、`at_ssh_certs`、`at_ssh_audit` 和唯一索引 `at_projects_id_user`（§2.14，远程服务器）；Down 删表、删索引，并删掉 `at_settings` 的 `ssh_session_ca`、`ssh_gateway_host_key`。

`00080_plugins.sql`：`at_roles.plugins`、`at_plugins`、`at_plugin_versions`，`at_skills.user_id` 可为空（管理员内置技能）及其唯一索引（architecture.md §6.5）。

`00090_project_chrome.sql`：`at_projects.icon / coordinator_model / thread_model / thread_effort / auto_resume`、`at_rate_limit_waits`、`at_schedules`（§2.10）。

`00070_project_repos.sql`：`at_project_repos`、`at_thread_repos`、`at_merge_queue.repo`、`at_tasks.pushed`；把每个项目的 repo/branch/git_connection_id 迁进去（基准分支写进 context），给已有讨论串和队列条目补上仓库（architecture.md §8.1）。

`00140_memory_files.sql`：`at_memory_files`；`at_project_memory` 的用户 / 统筹笔记合成 MEMORY.md（置顶在前），讨论串总结（来源 `#<no>`、`thread:<no>`）合成 `thread-summaries.md` 并在 MEMORY.md 里链接；删除 `at_project_memory`。Down 重建旧表，每个文件还原成一条笔记（MEMORY.md 置顶）。

每个迁移的测试（`server/migrations/migrations_test.go`）：空库全量 up；在生产 v1 结构快照（`testdata/v1_schema.sql`）上 up，基线不改结构、账号/项目/节点/Git 连接保留、只有有意删除的 v1 元素消失；有数据搬迁的迁移各有一个先 `UpTo` 前一版、种数据、再 up（必要时 down）的测试。
