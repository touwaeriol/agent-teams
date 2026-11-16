# v2 数据模型（PostgreSQL 17）

迁移工具：goose（`/server/migrations`，SQL 迁移 + 少量 Go 迁移），版本表 `goose_db_version`；表结构只由迁移定义。查询用 GORM（`internal/store` 的模型对应这些表，不用 AutoMigrate），少数 UNION / CTE / `RETURNING` 语句手写 SQL。

## 0. 约定

- **迁移期规则（P6 之前）**：Python v1 与 Go v2 共用这个库，Python 每次启动会跑自己的 `CREATE … IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`。因此 v2 迁移**只新增表、只给 v1 表加带默认值的列、只加 v1 写入不会违反的索引和约束**；不改列类型、不改 v1 列的默认值、不删任何东西。所有标“P6 后”的清理在 Python 退役后的迁移里做。
- **v1 不得读到 v2 的行**：v2 的讨论串任务、提问、留言写在 v1 的 `at_tasks`/`at_questions`/`at_mail` 里，状态值也有重叠（`pending`、`running`）。v1 的 `Hub.recover()` 会按 `status IN ('pending','running')` 恢复对话并接管。因此 **v1 防护补丁**（`architecture.md` §14.1 第 4 条：v1 的所有对话、任务、提问、留言查询只看 `at_conversations.kind = 'chat'` 且所有者 `platform_version = 1`）必须在 `00010_threads` 之前部署到生产。`at_conversations.kind` 和 `at_users.platform_version` 因此提前到 P1 的 `00002` 加（Python 补丁的启动 DDL 也幂等地加同样两列）。每个迁移的 CI 冒烟测试都包含“种入 v2 行 → 重启 v1 → 断言不变”。
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
          ├─ at_roles（岗位模板；替代 at_teams）
          ├─ at_teams（v1 YAML，只读保留）
          └─ at_projects ─┬─ at_conversations（kind: coordinator | thread | chat）─┬─ at_messages
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
                          ├─ at_project_memory
                          ├─ at_permission_rules
                          └─ at_office_layouts（项目覆盖布局，可选）
                          ├─ at_rate_limit_waits（碰到使用限额的员工，§2.10）
                          └─ at_schedules（定时任务，§2.10）
at_settings（全局键值）  at_audit_log  goose_db_version
```

## 2. 表结构

### 2.1 账号与设置（v1 表，原地扩展）

```sql
-- v1: id, email, password_hash, role, active, max_nodes, created_at
ALTER TABLE at_users
  ADD COLUMN IF NOT EXISTS platform_version SMALLINT NOT NULL DEFAULT 1,   -- 1 = 走 Python，2 = 走 Go v2（迁移期分流；v1 防护补丁也读它）
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

-- v1: key PK, value JSONB。键：platform、github_app。platform 增加字段 max_running_threads（默认 3）。v2 新增键：
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
  ADD COLUMN IF NOT EXISTS max_active_sessions INT,              -- NULL = 用守护进程上报值
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
```

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
  model            TEXT NOT NULL DEFAULT '',            -- 空 = 平台默认 claude-opus-5-5
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

`look` 的取值与办公室布局的 `looks` 对应（`office-layout.md` §4.6）：`outfit` 和 `accessory` 是**字符串**，合法值 = 当前全局布局 `looks.outfits` / `looks.accessories` 的键，由 Go 在保存时校验（库里不加 CHECK，OpenAPI 里也是 `string`，这样布局 JSON 新增一种服装，Team 页和接口不用改代码就能用）。Schema 要求布局至少包含内置的 `tee|hoodie|suit|coat` 和 `none|glasses|headset|cap`。`hair` 为空时按实例编号从布局的发色表里取。

### 2.5 项目、对话、讨论串

```sql
-- v1 at_projects: id, user_id, name, description, context, repo, branch, git_connection_id, created_at, updated_at, pinned, archived
-- 00070 起 repo / branch / git_connection_id 不再读写（留作回滚）：仓库在 at_project_repos，基准分支写在 context 里
ALTER TABLE at_projects
  ADD COLUMN IF NOT EXISTS work_mode TEXT NOT NULL DEFAULT 'worktree',      -- worktree | clone
  ADD COLUMN IF NOT EXISTS auto_approve BOOLEAN NOT NULL DEFAULT FALSE,     -- 新讨论串默认“全部放行”
  ADD COLUMN IF NOT EXISTS require_acceptance BOOLEAN NOT NULL DEFAULT TRUE,
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
    -- require_acceptance=false 时 PR 的必需 check 通过后按 merge_method 自动合并（architecture.md §8.4）
  ADD COLUMN IF NOT EXISTS office_layout TEXT NOT NULL DEFAULT '';          -- '' = 全局默认；否则布局 id

ALTER TABLE at_projects                                                     -- 00090
  ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT '',                   -- '' = 首字母配稳定底色；表情；或 color:#RRGGBB
  ADD COLUMN IF NOT EXISTS coordinator_model TEXT NOT NULL DEFAULT '',      -- '' = 统筹岗位的 model
  ADD COLUMN IF NOT EXISTS thread_model TEXT NOT NULL DEFAULT '',           -- '' = 组长/成员各自岗位的 model
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
  ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'chat',               -- coordinator | thread | chat（v1 旧数据；v1 只看 chat）
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
  summary        TEXT NOT NULL DEFAULT '',
  auto_approve   BOOLEAN NOT NULL DEFAULT FALSE,
  opened_by      TEXT NOT NULL DEFAULT 'coordinator', -- coordinator | user
  roles_snapshot JSONB NOT NULL DEFAULT '[]',     -- 开串时的岗位模板快照（之后模板改动不影响进行中的串）
  waiting_count  INT NOT NULL DEFAULT 0,          -- 冗余：等你处理数（触发器/应用维护），列表排序用
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at     TIMESTAMPTZ,
  finished_at    TIMESTAMPTZ,
  archived_from  TEXT NOT NULL DEFAULT '',        -- 00095：归档前的状态（取消归档时 done/failed 回原状态，其余回 paused）
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
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,   -- NULL = 项目统筹
  name        TEXT NOT NULL,                       -- coordinator | lead-12 | dev-1
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
  close_reason    TEXT NOT NULL DEFAULT ''
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
```

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
  kind        TEXT NOT NULL,                       -- 已知：pr | doc | design | slides | dashboard | board | data；其他字符串按链接显示
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
  published_by   TEXT NOT NULL DEFAULT '',
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

CREATE TABLE at_deliverable_comments (             -- 交付物批注，投递给发布者或组长
  id             TEXT PRIMARY KEY,                 -- dc_…
  deliverable_id TEXT NOT NULL REFERENCES at_deliverables(id) ON DELETE CASCADE,
  version        INT,
  author         TEXT NOT NULL,                    -- 用户 id
  text           TEXT NOT NULL,
  sent_to        TEXT NOT NULL DEFAULT '',         -- 收到它的员工；空 = 没有可投递的人
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE at_project_memory (                   -- 替代 threads.md 设计里的 at_projects.memory JSONB
  id          TEXT PRIMARY KEY,                    -- me_…
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  text        TEXT NOT NULL,
  source      TEXT NOT NULL,                       -- user | coordinator | #<no>
  pinned      BOOLEAN NOT NULL DEFAULT FALSE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_project_memory_project ON at_project_memory (project_id, created_at DESC);
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
CREATE TABLE at_attachments (   -- 用户附在消息上的图片
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

消息只在 `at_messages.meta.attachments` 里记 `{id, name, content_type, size}`；图片跟着项目删除。删除讨论串 = 删它的 `at_conversations` 行，`at_threads`、员工、会话、任务、提问、消息、合并队列等级联删除；`at_deliverables.thread_id` 置空（交付物留在资料库）；`at_events` / `at_status_events` 没有外键，历史事件保留。

资料库“添加”的文件（`POST /api/projects/{pid}/library`）就是一条 `at_deliverables`（`published_by = 'user'`）加一条 `at_deliverable_files` 第 1 版，与 `publish_file` 同样的类型和 5 MB 限制、同样的签名 URL 沙箱访问。项目用量（`GET /api/projects/{pid}/usage`）直接汇总 `at_usage`，不另建表。

## 3. 从 v1 表的映射

| v1 表 | v2 处理 | 时机 | 说明 |
| --- | --- | --- | --- |
| `at_users` | 原表 + 新列 | P1 | Go 用 `golang.org/x/crypto/scrypt` 校验 `scrypt$N$r$p$salt$digest`（urlsafe base64 无填充），新密码同格式写入，v1 也能校验 |
| `at_sessions` | 原表 + 新列 | P1 | cookie 名 `agent_teams_session` 不变，`sha256(token)` 查找不变 → 切换无需重新登录 |
| `at_settings` | 原表 | P1 | `platform`、`github_app` 键格式不变；`github_app.client_secret`/`pem` 用 Go 的 Fernet 解封（key = `urlsafe_b64(sha256(AGENT_TEAMS_SECRET_KEY))`） |
| `at_nodes` | 原表 + 新列 | P1/P2 | `token_hash` 不变 → 旧令牌继续有效；`max_runs`（hello 里）→ `max_active_sessions` 初值 |
| `at_git_connections` | 原表 + 新列 | P1 | PAT 密文 Fernet 不变；App 连接只用 `installation_id` |
| `at_projects` | 原表 + 新列 | P3 | 默认值即旧行为（worktree、需验收）；`clear_repo` 等语义不变 |
| `at_teams` | 转换为 `at_roles`，原表只读保留 | 用户切到 v2 时（Go 迁移函数 `ConvertTeam(user)`） | 新格式（`coordinator/lead/members`）直接映射；旧格式 `leader + employees`：leader → `lead` 岗位（同时复制为 `coordinator`，节点相同），每个 employee → `count:1` 的 member 岗位（`role` = 原名）；`node` 名 → `node_ids`；所有 member 岗位 `permission_mode: bypass`（默认）；`allowed_tools`/`disallowed_tools`/`model`/`max_turns`/`can_delegate`/`instructions`/`description` 原样；`claude_command`/`extra_args`/`workdir` 丢弃（v2 不支持，转换报告里列出）。P6 后删表 |
| `at_conversations` | 原表 + `kind`（P1）+ `updated_at`（P3） | P1/P3 | v1 数据全部 `kind='chat'`，v2 里“旧对话（只读）”；`history`（Claude session id）不再使用；`plan` JSONB 仅用于旧对话展示（v2 用 `at_main_tasks`） |
| `at_messages` | 原表 + 新列 | P3 | 旧数据 `author=''`：role leader → 显示为“负责人” |
| `at_tasks` | 原表 + 新列 | P3 | 旧数据 `thread_id IS NULL`；v1 状态值是 v2 的子集，**v2 行对 v1 不可见只靠 v1 防护补丁**（§0） |
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
   `00050_node_job_index.sql`：`at_node_commands` 的作业索引。`00060_design_skills.sql`：`at_roles.skills`、`at_skills`、`at_deliverable_files`、`at_upload_chunks`、`at_deliverable_comments`（architecture.md §6.5）。
7. `00100_drop_v1.sql`（P6 后）：删 `at_teams`、`at_conversations.history`；`at_tasks.output` 并入 `summary`；去掉不再需要的默认值。

`00095_thread_lifecycle_images.sql`：`at_threads.archived_from`、`at_attachments`（§2.11）。

`00080_plugins.sql`：`at_roles.plugins`、`at_plugins`、`at_plugin_versions`，`at_skills.user_id` 可为空（管理员内置技能）及其唯一索引（architecture.md §6.5）。

`00090_project_chrome.sql`：`at_projects.icon / coordinator_model / thread_model / thread_effort / auto_resume`、`at_rate_limit_waits`、`at_schedules`（§2.10）。

`00070_project_repos.sql`：`at_project_repos`、`at_thread_repos`、`at_merge_queue.repo`、`at_tasks.pushed`；把每个项目的 repo/branch/git_connection_id 迁进去（基准分支写进 context），给已有讨论串和队列条目补上仓库（architecture.md §8.1）。

每个迁移在 CI 里：对“生产结构快照”执行 → 启动 Python v1 跑冒烟测试（注册、建项目、发消息、节点连接）→ 种入 v2 用户的讨论串、`pending`/`running`/`claimed` 任务、open 提问与留言 → 重启 Python v1 → 断言这些行未变、v1 对话列表不含它们 → 通过才算合规。
