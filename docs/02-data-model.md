# 02 数据模型

PostgreSQL。主键统一 `id uuid`（任务另有项目内序号 `number`），时间统一 `timestamptz`。枚举用 `text` + CHECK，便于迁移。下文只列业务字段，`created_at` / `updated_at` 默认每张表都有。

## 关系总览

```
users ─┬─ api_tokens
       └─ projects ─┬─ project_repositories ── repositories ── git_connections
                    ├─ project_template_allocations ──┬── employee_templates
                    │                                  └── nodes ── runtimes
                    ├─ employees ─┬─ sessions ── events
                    │             ├─ memories (scope=employee)
                    │             └─ requests
                    ├─ tasks ── handoffs
                    ├─ plans
                    ├─ messages
                    ├─ memories (scope=project，即项目知识)
                    └─ project_channels
```

## 用户与授权

### users

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| name | text | |
| email | text unique | |
| password_hash | text null | 用第三方登录时为空 |
| role | text | `owner` / `admin` / `member` |

### api_tokens

MCP 和脚本用的访问令牌。OAuth 授权（之后加）也落在这张表，`kind` 区分。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| user_id | uuid | |
| kind | text | `pat` / `oauth` |
| name | text | 例：“CI 机器人”“笔记本上的 Claude Code” |
| token_hash | text | 只存哈希；明文只在创建时展示一次 |
| scopes | text[] | `read` / `chat` / `approve` / `manage` |
| project_ids | uuid[] null | 空 = 全部项目 |
| last_used_at, expires_at, revoked_at | timestamptz null | |

## 节点

### nodes

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| name | text unique | `node-a` |
| status | text | `pending`（等注册）/ `online` / `offline` / `disabled` |
| install_mode | text | `ssh` / `command` |
| ssh_target | text null | SSH 安装时的 `user@host:port`，安装完不再使用 |
| root_dir | text | 节点上的根目录，默认 `~/agent-teams` |
| daemon_version | text null | |
| credential_hash | text null | 长期节点凭证的哈希 |
| last_heartbeat_at | timestamptz null | |
| resources | jsonb | 最近一次心跳：`{cpu_pct, mem_used, mem_total, disk_used, disk_total, latency_ms}` |
| max_sessions | int | 节点总并发上限（所有运行时合计） |

### node_registrations

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| node_id | uuid | |
| token_hash | text | 一次性注册令牌 |
| expires_at | timestamptz | 默认 30 分钟 |
| used_at | timestamptz null | |

### runtimes

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| node_id | uuid | unique(node_id, kind) |
| kind | text | `claude_code` / `codex` |
| path, version | text | |
| logged_in | bool | |
| account_label | text null | 脱敏后的账号标识，只用于显示 |
| quota | jsonb null | `{week_used_pct, resets_at, measured_at}`；Codex 按 API 计费时为空 |
| slots | int | 同时运行的会话数上限 |
| min_remaining_pct | int | 额度低于这个值停止派发，默认 10 |
| status | text | `ok` / `missing` / `logged_out` / `error` |
| detected_at | timestamptz | |

## Git

### git_connections

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| provider | text | 第一版：`github_app` / `github_pat`。以后加 `gitlab_token` 等，字段保持通用 |
| name | text | |
| host | text | 第一版固定 `github.com`（以后可支持 GitHub Enterprise） |
| config | jsonb | `github_app`：`{app_id, installation_id, account}`；`github_pat`：`{account}` |
| secret_id | uuid | 指向 secrets（App 私钥或访问令牌） |
| status | text | `ok` / `expiring` / `invalid` |
| expires_at | timestamptz null | |

### secrets

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| kind | text | `github_app_key` / `token` |
| ciphertext | bytea | 数据密钥加密的内容 |
| wrapped_key | bytea | 主密钥加密的数据密钥 |

### repositories

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| git_connection_id | uuid | |
| full_name | text | `your-org/main-app`，从用户粘贴的 GitHub 链接解析；unique(git_connection_id, full_name) |
| clone_url | text | `https://github.com/<full_name>.git` |
| default_branch | text | |

### credential_issuances（审计）

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| employee_id, node_id, repository_id | uuid | |
| operation | text | `read` / `write` |
| result | text | `issued` / `denied` |
| reason | text null | 拒绝原因 |
| expires_at | timestamptz null | |

## 员工模板

### employee_templates

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| name | text | “开发（Claude Code）” |
| identity | text | `coordinator`（统筹）/ `decider`（决策）/ `executor`（执行）/ `verifier`（验收） |
| agent_kind | text | `claude_code` / `codex` |
| model | text null | 空 = 跟随节点默认 |
| permission_mode | text | `auto` / `default` / `acceptEdits` / `bypassPermissions` |
| prompt_template | text | CLAUDE.md / AGENTS.md 模板，变量 `{项目}` `{员工}` `{仓库}` |
| naming_pattern | text | `dev-{节点}{序号}`、`pm-{项目}` |
| max_per_project | int null | 统筹固定为 1 |
| allow_subagents | bool | |
| max_subagents | int null | 写进提示词的上限 |
| git_access | text | `read` / `write_own`（只推 `emp/{员工}/*`）/ `write_any_emp` / `merge` |
| task_kinds | text[] | 能接的任务类型，供统筹选人参考 |
| office_area | text | `dev` / `decision` / `head_office` |
| channel | text null | 默认归属的通道名 |
| skills | jsonb | 技能清单（写进 `.claude/skills/`） |
| extra_mcp_servers | jsonb | 额外的 MCP 服务配置 |
| version | int | 每次保存 +1，员工记录同步到的版本 |

统筹模板由系统内置一份，可以改提示词但不能删、身份不能改。

## 项目

### projects

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| name, slug | text | slug 用于目录名，unique |
| status | text | `active` / `paused` / `archived` |
| plan_mode | text | `confirm`（计划先给人确认）/ `auto` |
| balance_strategy | text | `quota` / `round_robin` / `prefer_node` |
| prefer_node_id | uuid null | |
| delivery_branch_pattern | text | 默认 `emp/{员工}/{任务号}-{简称}` |
| pr_policy | text | `pr_to_default`（建 PR，由验收身份合并）/ `push_only` |
| approval_policy | jsonb | `{delegate: "coordinator" \| null, must_human: [规则], timeout_sec: 0}` |
| created_by | uuid | |

`must_human` 规则：`{tool: "Bash", pattern: "rm -rf*"}`、`{kind: "git_push", branch: "main"}`、`{kind: "network_new_host"}`、`{kind: "edit_path", pattern: ".github/workflows/**"}`。

### project_repositories

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id, repository_id | uuid | 主键 |
| role | text | `primary` / `readonly` |
| base_branch | text | 默认起点分支 |

### project_channels

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id | uuid | |
| name | text | `decision` / `dev` |
| max_active | int | 同时在工作的员工上限 |

模板的 `channel` 决定员工默认进哪个通道，项目可以在 allocation 上覆盖。

### project_template_allocations

设计稿“员工配置”矩阵的每一格。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id, template_id, node_id | uuid | 主键（统筹的 node_id 可为空 = 自动选） |
| count | int | 这个节点上建几个 |
| channel_override | text null | |

改 count 后由控制面增删 employees（回收前检查未推送改动）。

### employees

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id, template_id, node_id | uuid | |
| name | text | unique(project_id, name)，按模板命名规则生成 |
| dir | text | 节点上的路径 `~/agent-teams/employees/<项目slug>/<员工名>` |
| follow_template | bool | true = 模板更新后同步 |
| template_version | int | 已同步到的模板版本 |
| overrides | jsonb | 项目里对该员工的覆盖（模型、权限模式、提示词追加段等） |
| status | text | 见 05 的状态机：`provisioning` / `idle` / `queued` / `working` / `waiting` / `blocked` / `paused` / `offline` / `removing` |
| current_task_id | uuid null | |
| current_session_id | uuid null | |
| git_name, git_email | text | 提交者身份 |
| provisioned_at | timestamptz null | 守护进程确认目录建好的时间 |

## 执行

### sessions

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| employee_id, runtime_id | uuid | |
| task_id | uuid null | 统筹的会话可以不挂任务 |
| agent_session_id | text null | Claude Code session id / Codex thread id，续跑用 |
| state | text | `starting` / `running` / `idle`（等消息）/ `stopped` / `failed` |
| end_reason | text null | `done` / `stopped` / `crashed` / `node_lost` / `context_restart` |
| context_pct | int null | 最近一次上报的上下文占用 |
| subagents_active | int | |
| usage | jsonb | token 统计 |
| started_at, ended_at | timestamptz | |

### tasks

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id | uuid | |
| number | int | 项目内递增，显示为 `#1315` |
| title | text | |
| brief | text | 作为会话第一条消息 |
| created_by_kind | text | `user` / `employee` |
| created_by_id | uuid | |
| plan_id, plan_step | uuid null, int null | 来自哪份计划的哪一步 |
| requirement | jsonb | `{template_id}` 或 `{identity}` 或 `{employee_id}`（点名） |
| assignee_id | uuid null | 开始执行后固定，返工也给同一人 |
| state | text | `pending` / `queued` / `running` / `waiting` / `blocked` / `done` / `cancelled` |
| depends_on | uuid[] | 依赖的任务，全部 done 才可派发 |
| priority | int | 默认 0 |
| repository_id | uuid | 主要改动的仓库 |
| base_ref | text | 起点 |
| deliver_branch | text null | 实际交付分支 |
| pr_url | text null | |
| result_summary | text null | 员工 `report(kind=done)` 的内容 |
| started_at, finished_at | timestamptz null | |

### plans

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id | uuid | |
| version | int | |
| status | text | `draft` / `pending_confirm` / `confirmed` / `superseded` |
| steps | jsonb | `[{n, title, identity, detail, status, task_ids}]` |
| confirmed_by | uuid null | |
| confirmed_at | timestamptz null | |

### requests

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id, employee_id, session_id | uuid | |
| number | int | 项目内递增，`#42` |
| kind | text | `permission` / `question` |
| payload | jsonb | 权限：`{tool, input, cwd, description}`；提问：`{question, options[], multi}` |
| must_human | bool | 命中 `must_human` 规则 |
| state | text | `pending` / `answered` / `expired` / `cancelled` |
| answer | jsonb null | `{decision: allow\|deny, remember, message}` 或 `{answers}` |
| answered_by_kind | text null | `user` / `employee` |
| answered_by_id | uuid null | |
| answered_at | timestamptz null | |

## 对话、事件、记忆

### messages（项目对话）

每个项目只有一个对话，就是这张表里该项目的所有行。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| project_id | uuid | |
| seq | bigint | 项目内递增，客户端用它增量拉取 |
| author_kind | text | `user` / `coordinator` / `employee` / `system` |
| author_id | uuid null | |
| source | text | `web` / `mcp` / `internal` |
| client_label | text null | MCP 客户端的授权名称，例“笔记本上的 Claude Code” |
| kind | text | `text` / `plan_card` / `dispatch` / `activity` / `request_card` / `progress` |
| body | jsonb | 按 kind 不同：文本、plan_id、任务列表、请求 id… |
| visibility | text | `user`（对话里直接显示）/ `folded`（员工汇报，折叠在“工作动态”里） |

### events（只追加）

时间线、办公室动画和回放的来源。按月分区。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | bigserial | |
| project_id, employee_id, session_id | uuid | |
| ts | timestamptz | |
| type | text | 统一事件类型，见 [03-daemon-protocol.md](03-daemon-protocol.md#统一事件) |
| data | jsonb | |

### memories

员工记忆和项目知识共用一张表，按条存，渲染成文件下发。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| scope | text | `employee` / `project` |
| project_id | uuid | |
| employee_id | uuid null | scope=employee 时必填 |
| content | text | 一条记忆，建议一两句话 |
| source | jsonb | `{author_kind, author_id, task_id}` |
| state | text | `active` / `pending_promotion`（员工申请提升，等统筹确认）/ `archived` |
| promoted_from | uuid null | 项目知识由哪条员工记忆提升而来 |

另有 `memory_versions(employee_id 或 project_id, version, rendered text)` 记录每次渲染结果，用于回看和恢复。

### handoffs

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| task_id | uuid | 主键 |
| employee_id | uuid | |
| content | text | Markdown：目标 / 已完成 / 下一步 / 注意 |
| version | int | |

任务结束后保留作记录，不再下发。

## 派生与索引

- 办公室各区人数、项目概况等都从 employees / sessions 实时计算，不单独存。
- 常用索引：`tasks(project_id, state, priority, created_at)`、`employees(project_id, template_id, status)`、`requests(state, project_id)`、`messages(project_id, seq)`、`events(session_id, ts)`、`events(project_id, ts)`。
