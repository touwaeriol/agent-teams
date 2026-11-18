# 改造计划：项目级共享员工、任务队列与会话管理

设计见 [shared-members.md](shared-members.md)（下文 “设计 §x”）。本文件把它拆成可以逐个提交、逐个验证的任务。基线：`develop` @ `ca478bc` 之后。

## 0. 做法

- **一个阶段一个分支**（`feat/staff-p0` … `feat/staff-p4`），阶段内按任务逐个提交；阶段做完：全量测试（Go、node、web、protocol）→ 代码审查（抽象 / 复用 / 解耦 / 低嵌套 / 简洁 / 低侵入）→ 修复 → 合并 `develop`。
- **每个任务都带测试**：Go 用真实 PG / Valkey（`server/scripts/test.sh`），node 和 web 用 vitest 假件；改了协议跑 `make gen-protocol`，改了 API 重新生成 `openapi.json` / `schema.d.ts`，加迁移同时加 `migrations_test.go` 用例并更新 `TestLatest`。
- **新旧守护进程共存**：节点新能力都在 `hello.features` 里声明（`git_sync`、`session_compact`、`context_usage`），控制台看到才用，没有就退回现在的行为。所以控制台可以先于节点上线，节点按版本逐台升级。
- **上线顺序**：每个阶段先部署控制台（跑迁移），再发节点新版本、各节点升级。阶段之间可以停，每个阶段单独上线也有用。

## 阶段 0：任务衔接（设计 §4.2、§4.3）

和共享员工无关，**现在就缺**：后续任务拿不到前置任务的代码，审查员可能看旧代码，节点掉线会丢未推送的提交。

| # | 任务 | 改哪里 | 测试 / 验收 |
| --- | --- | --- | --- |
| P0-1 | **协议**：`git.prepare` 加 `sync: {mode: "member" \| "review", ref?}`；`git.result` 加 `sync: {head_sha, action: reset \| merged \| conflict \| dirty \| checkout, conflicts: string[]}`；`hello.features` 加 `git_sync` | `protocol/schemas/node/git.prepare.json`、`git.result.json`、`hello.json`，`make gen-protocol` | protocol 测试、示例校验 |
| P0-2 | **节点同步**：`worktreeLocked` 遇到已存在的 worktree 且带 `sync` 时：fetch → `member`：工作区脏就返回 `dirty`；成员分支没有讨论串分支以外的提交 → `checkout -B <成员分支> origin/<讨论串分支>`（`reset`）；有 → `merge origin/<讨论串分支>`，冲突则 `merge --abort` 并返回冲突文件（`conflict`）。`review`：只读检出 `origin/<ref>`（detached，`checkout`） | `node/src/git/jobs.ts` | `node/test/git.test.ts` 用本地裸仓库覆盖 5 种结果 |
| P0-3 | **控制台在领取时同步**：`claim` 不再只给只读岗 `RefreshWorktree`，而是对所有成员排一个 `git.prepare(sync)`（审查任务 `mode=review, ref=被审任务的成员分支`），任务进 `claimed` 后**等同步结果**再发任务简报（`git.result` 回调里带 `meta.task_id` → 生成简报 → `Deliver`）；节点不支持 `git_sync`、会话还没开（首次 open 本来就会 prepare）或 2 分钟没回结果时直接发简报并注明“未同步” | `scheduler/claim.go`、`orchestrator/sessions.go`（`RefreshWorktree` → `SyncWorktree`）、`delivery` 的 git 结果分发 | engine 测试：dev-1 先做 t1，dev-2 做的 t2 合并后 dev-1 领 t3（依赖 t2），断言收到 sync(member) 且简报写“已同步到 <sha>”；审查任务收到 sync(review, ref=thread/N/dev-1)；冲突时简报列出冲突文件 |
| P0-4 | **记录合并范围**：`at_merge_queue` 加 `base_sha`（合并前讨论串分支的 sha，`startEntry` / `directMerge` 时写）；合并成功时用 GitHub compare 取 `base..head` 的改动文件，写到 `at_tasks.merged`（JSONB：`{repo: {base, head, files[]}}`，文件最多 200 个） | 迁移 `00260_task_handoff.sql`、`delivery/mergeq.go`、`ghops`（compare） | 迁移测试；engine 测试断言 `merged` 写入；fake GitHub 加 compare |
| P0-5 | **交接说明**：`complete_task` 加可选 `handoff`（≤ 4000 字），存 `at_tasks.handoff`；任务有未完成的下游依赖而 `handoff` 为空时，第一次 `complete_task` 返回 `isError` 提醒补写（再调一次就放行） | `protocol/schemas/tools/complete_task.json`、`toolsvc/member.go`、同一个迁移 | toolsvc / engine 测试 |
| P0-6 | **简报带交接内容**：`depResults` 改成每个依赖给：谁做的、总结（≤ 3000 字）、`handoff`、合并范围和文件列表、“用 `git log base..head` / `git show` 看细节”、审查结论（`Parent` 指向它的 review / decision 任务的总结）、它发布的交付物 | `scheduler/claim.go`（`taskBrief`、`depResults`） | 单元测试断言简报内容 |
| P0-7 | **回合结束自动推送**：成员回合结束且手里有 running 任务时，排一个推送作业（复用 `delivery.Push` / `push_branch` 的路径），只推成员分支；同一成员 5 分钟内最多一次；没有新提交时节点作业直接成功（确认 `push` 作业的现有行为） | `scheduler/pool.go`（`TurnEnded`）、`delivery/delivery.go` | engine 测试：turn_ended 后收到 git.push，5 分钟内第二次不推 |
| P0-8 | **提示词和文档**：成员提示词加“有下游任务时 complete_task 写 handoff”“勤提交”；architecture.md §7.3、§8 补同步、交接、自动推送 | `orchestrator/prompt.go`、docs | — |

阶段验收：本地 e2e（`deploy/e2e`，假 GitHub）跑“dev-1 做 t1 → dev-2 做依赖 t1 的 t2 → reviewer 审 t2”，看 dev-2 的 worktree 里有 t1 的改动、reviewer 检出的是 t2 的最新提交。

## 阶段 1：共享员工 + 项目级队列（设计 §2–§4、§6–§8）

| # | 任务 | 改哪里 | 测试 / 验收 |
| --- | --- | --- | --- |
| P1-1 | **迁移**：`at_employees.current_thread_id`（FK at_threads ON DELETE SET NULL）；`at_agent_sessions.thread_id`（回填 = 员工的 `thread_id`）；`at_tasks` 加 `reserved_for TEXT`、`reserved_until TIMESTAMPTZ`、`ready_at TIMESTAMPTZ`（依赖满足、可领取的时刻）；`at_threads.only_roles JSONB`；项目队列用的索引 `at_tasks (thread_id, status, role)` | 迁移 `00270_shared_members.sql` | 迁移测试（回填、Down） |
| P1-2 | **编制管理** `core.EnsureStaff(project)`：按用户岗位模板的 `count` 建 / 启用 / 停用项目级成员（`thread_id NULL`，名字 `<岗位>-<n>`，发色按实例号），新成员按节点轮流分配主节点（Codex 只分隔离节点）；手里有任务的成员等任务结束再停用。调用点：开讨论串、岗位模板保存、调度循环兜底 | `core/employees.go`、`orchestrator/threads.go`、`httpapi` 岗位保存 | Go 测试：加人、减人（有任务的不立刻停）、节点轮转 |
| P1-3 | **开讨论串只建组长**：`InstantiateThread` 只建 lead；`open_thread(roles)` 的人数忽略，只取岗位名存 `only_roles`；`assign_task` 校验岗位在 `only_roles` 里 | `orchestrator/threads.go`、`core/employees.go`、`toolsvc/coordinator.go`、`toolsvc/lead.go` | 现有开讨论串测试改断言 |
| P1-4 | **名字解析**：`EmployeeByName(project, thread, name)` 先找讨论串自己的（组长、旧成员）再找项目级；新增 `ThreadParticipants(thread)` = 组长 + 讨论串旧成员 + 在这个讨论串有任务或开过会话的项目成员，替换 `ThreadEmployees` 的调用点（名单 `roster`、`list_team`、`broadcast`、`Pause`、`setThreadModel`、`EmployeeBriefs`）；`gateRole` 按项目找岗位 | `core/employees.go` 及调研清单里的调用点 | 单元测试：同名旧成员优先；参与者集合 |
| P1-5 | **会话按讨论串开**：`PathsOf` / `loadEnv` 对项目成员取“当前讨论串”（手里任务的讨论串，否则会话的 `thread_id`）；`openSession` 写 `thread_id`、更新 `current_thread_id`；领到别的讨论串的任务 → `session.close(switch_thread)` + 新开（不 resume），首条消息 = 任务简报 + 交接摘要（`threadDigest` 按成员 × 讨论串取）；`Deliver` 的 `threadEnded` / `conversationOf` 改用当前讨论串 | `orchestrator/sessions.go`、`spec.go`、`threads.go`（handoff 摘要） | engine 测试：dev-1 在 #1 做完后领 #2 的任务，收到 close(switch_thread) 和新的 session.open（cwd = threads/2/dev-1） |
| P1-6 | **项目级调度**：`Schedule(projectID)`、Valkey 频道 `sched:<project>`、`sweep` 按项目；可领取 = pending + `ready_at` 非空 + 讨论串在 running / waiting + 岗位匹配；`idleMember` 按项目找 `thread_id IS NULL AND active` 且全项目没有 claimed / running 任务的成员；按设计 §4.1 打分（点名 > 返工预留 > 依赖粘性 > 讨论串粘性 > 同仓库 > 其他；同档按优先级、下游数、`ready_at`；`ready_at` 超过 5 分钟的不看粘性）；`TurnEnded` / `SessionLost` 用 `CurrentTaskOf(project, name)` | `scheduler/*.go`、`core/tasks.go` | 打分函数单元测试（表驱动）；engine 测试：两个讨论串共用 dev-1/dev-2，粘性、点名、饿死保护 |
| P1-7 | **返工预留**：审查打回（`rejected`）、CI 失败 / 冲突修复（`fixTask`）生成的任务写 `reserved_for = 原成员`、`reserved_until = now + 10 分钟` | `scheduler/complete.go`、`delivery/mergeq.go` | engine 测试：预留期内别人不领，过期放开 |
| P1-8 | **成员工具的讨论串**：`toolsvc.prepare` 对项目成员从当前任务 / 会话取讨论串；`requireThread`、`completeTask`、`pushBranch`、`requestReview`、`assignSubtask`、`waitTask`、`ask*`、`send_message`、`publish*` 跟着改 | `toolsvc/*.go` | 现有 toolsvc 测试改造 + 两个讨论串交替的用例 |
| P1-9 | **讨论串结束和清理**：`EndThreadWork` 按 `at_agent_sessions.thread_id` 关会话；`deleteMemberBranches` / 目录清理遍历 `DISTINCT claimed_by` + 旧成员；项目成员的 `current_thread_id` 清空 | `orchestrator/threads.go`、`delivery/cleanup.go` | engine 测试：讨论串结束后项目成员还在、#1 的成员分支被删 |
| P1-10 | **状态和 API**：`status/facts.go` 的当前任务按项目取；`EmployeeBrief.thread_no` = 当前讨论串；`ThreadView.members` = 参与者，加 `busy_elsewhere`；办公室快照照旧（成员暂时画在当前讨论串的 pod 里，阶段 3 再改） | `status/*.go`、`core/views.go`、`orchapi/office.go`、`orchapi/threads.go` | API 测试；OpenAPI 重新生成 |
| P1-11 | **节点**：项目成员的技能 / 插件目录用 `project/<name>`（`session.open` 的 `employee.thread_no` 仍给当前讨论串，另加 `employee.scope: "project"`） | `node/src/sessions/skills.ts`、`plugins.ts`，协议 `session.open.json` | node 测试 |
| P1-12 | **新旧并存**：上线时已有的讨论串成员（`thread_id` 非空）只做完手里的任务、不再领新任务；已有 `pending` 任务进项目队列 | `scheduler`、`core` | engine 测试：有旧成员的讨论串，新任务派给项目成员，旧成员手里的任务照常完成 |
| P1-13 | **网页最少改动**：讨论串成员面板显示参与者和“在 #12 忙”；团队页说明“人数 = 项目编制” | `web/src/components/app/threadviews/MembersView.vue`、`lib/present.ts`、团队页 | vitest |
| P1-14 | **提示词和文档**：组长 `assign_task` 说明、成员“你是项目固定成员”；architecture.md §6.1、§7.3、data-model.md 改成新模型 | `orchestrator/prompt.go`、docs | — |

阶段验收：本地 e2e 新脚本 `run_shared.mjs`：一个项目 dev × 2、reviewer × 1，同时开两个讨论串各拆 3 个任务（含依赖），全部跑完、PR 开出；办公室成员数恒为 3。

## 阶段 2：会话管理（设计 §5）

| # | 任务 | 改哪里 | 测试 / 验收 |
| --- | --- | --- | --- |
| P2-1 | **岗位配置**：`at_roles` 加 `context_limit`（token，空 = 模型窗口）、`compact_between_pct`、`compact_in_task_pct`、`clear_after_tasks`、`compact_focus`，百分比都相对 `context_limit`；YAML 视图 `context:` 段；按岗位类型填默认值（dev 60 / 80 / 8，审查 / 决策 — / 80 / 1，组长和统筹 — / 70 / —）；团队页岗位弹窗加这几项 | 迁移 `00280_role_context.sql`、`roles/*.go`、`httpapi`、`web/src/pages/settings/team/RoleDialog.vue` | 迁移测试、roles 校验测试、vitest |
| P2-2 | **协议**：`session.event` 的 `turn_ended` 加 `context: {used_tokens, max_tokens}`；新命令 `session.compact {session_id, open_gen, instructions}`、新上报 `session.compacted {session_id, open_gen, context}`；`session.open` 加 `context: {limit_tokens, in_task_pct}`；`session.send` 加 `anchor`（任务锚点文本，守护进程保存给压缩后用）；features `context_usage`、`session_compact` | `protocol/schemas/node/*.json`，`make gen-protocol` | protocol 测试 |
| P2-3 | **节点 · Claude**：回合结束调 `Query.getContextUsage()` 填 `context`；`session.compact` = 推一条 `/compact <instructions>`，等 `compact_boundary` 发 `session.compacted`；开会话时设 `CLAUDE_CODE_AUTO_COMPACT_WINDOW = limit_tokens` 和 `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE = in_task_pct`（按 P2-7 实测结果，必要时换算成相对模型窗口的百分比） | `node/src/sessions/claude/driver.ts`、`options.ts`、`manager.ts` | vitest（fake-query 加 getContextUsage / compact_boundary） |
| P2-4 | **节点 · Codex**：用 `thread/tokenUsage/updated`（`last`、`modelContextWindow`）填 `context`；`session.compact` = `thread/compact/start`，`thread/compacted` 后上报；`thread/start` 配置 `model_auto_compact_token_limit = limit_tokens × in_task_pct` | `node/src/sessions/codex/driver.ts`、`mapper.ts` | vitest（fake-codex） |
| P2-5 | **控制台的开工决策**：存 `at_agent_sessions.context_used`（token），和岗位 `context_limit` 比、`tasks_in_session`；领取时按设计 §5 的表决定 继续 / 压缩 / clear；压缩时任务停在 `claimed`，等 `session.compacted`（10 分钟超时就直接发简报）；节点没有 `session.compact` 能力时超过阈值改为 clear | `scheduler/claim.go`、`orchestrator/sessions.go` | engine 测试覆盖 4 种情况 |
| P2-6 | **压缩后补任务锚点**：控制台把锚点（任务 id、原始说明、分支、交接说明、最近进度）随任务简报的 `session.send.anchor` 下发，守护进程保存；Claude 用 SessionStart(`compact`) 钩子的 `additionalContext` 注入；Codex 在 `thread/compacted` 后发一条 `later` 消息 | `node/src/sessions/claude/hooks`、`codex/driver.ts`、`scheduler/claim.go` | vitest；engine 测试锚点下发 |
| P2-7 | **先做的验证**：用真 Claude 实测 SDK 0.3.325 的 PreCompact 钩子能否改写压缩指令、`CLAUDE_CODE_AUTO_COMPACT_WINDOW` 和 `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` 的语义（能否让 1M 模型按 600K 窗口、80% 即 480K 压缩）、SessionStart(`compact`) 是否触发；结果写进设计 §5.2，决定 P2-6 走钩子还是消息 | `node/scripts/smoke-*.ts` | 实测记录 |
| P2-8 | **长任务**：每个任务记 `compactions`、`ctx_start_pct`、`ctx_end_pct`（`at_tasks` 新列）；同一任务任务中压缩 ≥ 2 次给组长发“考虑拆小”；守护进程把 `.agent-notes/` 写进每个 worktree 的 `info/exclude`，提示词要求长任务记笔记 | 迁移、`scheduler`、`node/src/git/jobs.ts`、`prompt.go` | engine / node 测试 |

阶段验收：真 Claude e2e：dev 连做同一讨论串 3 个任务（不压缩）、人为调低阈值触发一次压缩、换讨论串触发 clear；审查员每个任务都是新会话。

## 阶段 3：办公室和团队页（设计 §9）

| # | 任务 | 改哪里 |
| --- | --- | --- |
| P3-1 | 布局 schema：`staffArea`（区域、按岗位分排、每排桌数、溢出）、`threadPods.templates` 只留组长座位和参与者头像位、`members.visitPods`（默认 false）；`office.example.json` 和 `office-layout.md` 同步 | `protocol/schemas/office/`、`docs/v2/office-layout.md`、`office.example.json` |
| P3-2 | `compile.ts`：成员按岗位排进工位区；pod 只摆组长 + 参与者头像；`states.rules` 的 `place: seat` 对成员指固定工位 | `web/src/office/compile.ts`、`types.ts`、`lib/office.ts` |
| P3-3 | 工位 / 名牌上讨论串颜色的 `#18 · t5` 小牌；任务板改项目队列（讨论串颜色、依赖锁、预留名字）；顶栏 `编制 · 干活 · 空闲 · 讨论串` | `web/src/office/render.ts`、`overlay.tsx` |
| P3-4 | 统筹“放下新工作台”、组长“起身离开”的序列（`states.sequences`） | 布局 JSON、`office/director` |
| P3-5 | 团队页：编制内成员、当前讨论串和任务、加员工入口 | `web/src/pages/settings/team/` |

验收：vitest（compile 纯函数快照）、截图走查（亮 / 暗两套）。

## 阶段 4：调度优化

| # | 任务 |
| --- | --- |
| P4-1 | `ready_at` 由事件维护（任务 done → 只检查直接下游），领取查询不再逐个 `DepsSatisfied` |
| P4-2 | 关键路径优先级：下游任务数（传递闭包）缓存在任务上，依赖变化时更新 |
| P4-3 | 压测：一个项目 500 个任务、20 个讨论串、10 个成员，调度一轮 < 100 ms |

## 风险和待验证

| 风险 | 对策 |
| --- | --- |
| 阶段 1 改动面大（调研清单里几十个 `thread_id` / `ThreadEmployees` 调用点） | 先加 `ThreadParticipants` 和带回退的 `EmployeeByName`，再逐个替换调用点；旧成员路径保留到阶段 1 上线后一个版本再删 |
| 同一成员在多个讨论串各有 worktree，磁盘占用变大 | 讨论串结束照常清理；`git worktree` 共享对象库，增量小 |
| 换讨论串就 clear，粘性不够时频繁冷启动 | 阶段 1 上线后看 `switch_thread` 关会话的次数，必要时调大饿死时限或加讨论串粘性权重 |
| SDK / Codex 压缩接口的实际行为 | P2-7 先实测再写 P2-3 / P2-6 |
| 节点协议新增字段与旧守护进程 | 全部按 `hello.features` 开关，旧节点退回现有行为 |

## 进度

| 阶段 | 状态 |
| --- | --- |
| 0 任务衔接 | 完成（`feat/staff-p0`）；交付物没有任务关联，简报里暂不列交付物 |
| 1 共享员工 + 项目级队列 | 完成（`feat/staff-p1`）：P1-1～P1-14 和 e2e 脚本 `deploy/e2e/run_shared.mjs`（待用真 Claude 跑一遍）。补充：跨讨论串留言带讨论串号、不换讨论串（设计 §6），节点上线触发主人项目的调度，没有讨论串的成员在办公室里站在空闲锚点 |
| 2 会话管理 | 未开始 |
| 3 办公室和团队页 | 完成（`feat/staff-p3`）：P3-1～P3-5。默认布局改为上方一排讨论串工作台（只坐组长 + 参与者头像条）、下方按岗位分排的成员工位，场景高 892；旧布局（无 `staffArea`）保留原行为。补充：快照 `board.queue`（待领任务、依赖锁、有效预留）和 `EmployeeBrief.sort / instance`；`task` 事件后网页重取办公室快照；工作台在讨论串结束后保留 `fadeMs` 淡出、组长走出去；团队页的“项目编制”面板按项目列成员，“加一人”改岗位人数（保存模板后生效） |
| 4 调度优化 | 未开始 |
