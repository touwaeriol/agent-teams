// Demo data for the mock API: the "agent team" project of the design canvas.
const now = Date.now();
export const ago = (min) => new Date(now - min * 60_000).toISOString();

export const me = {
  id: "u1", email: "liu@example.com", display_name: "liu", role: "admin", active: true, max_nodes: null,
  must_change_password: false, platform_version: 2, created_at: ago(60 * 24 * 4),
};

const delivery = {
  gates: { review: true, decision: true, merge_queue: true },
  ci: { mode: "checks", required: [], appear_timeout_s: 300, timeout_s: 3600, on_missing: "pass" },
  merge_method: "squash", approval_timeout_s: null, notify_on_ask: true, allow_workflow_edits: false,
};

const project = (id, name, extra = {}) => ({
  id, name, description: "", context: "", repo: null, branch: "main", git_connection_id: null, pinned: false, archived: false,
  work_mode: "worktree", auto_approve: false, require_acceptance: true, delivery, office_layout: "default", waiting: 0,
  icon: "", coordinator_model: "", thread_model: "", thread_effort: "", auto_resume: false,
  threads_active: 0, created_at: ago(60 * 24 * 3), updated_at: ago(30), ...extra,
});

export const projects = [
  project("p1", "agent team", {
    icon: "color:#2E7D4F", auto_resume: true, coordinator_model: "claude-opus-5-5", thread_effort: "medium",
    pinned: true, waiting: 3, threads_active: 2, repo: "touwaeriol/agent-teams", git_connection_id: "g1", updated_at: ago(1),
    description: "多员工协作平台",
    context: "后端 FastAPI + asyncpg，前端 React + Vite（pnpm）。改完跑 ruff、mypy、pytest 和 vite build。提交信息用中文。",
  }),
  project("p2", "sup2api", { icon: "🚀", repo: "touwaeriol/sup2api", branch: "develop", git_connection_id: "g1", updated_at: ago(40) }),
  project("p3", "new-api", { waiting: 1, repo: "touwaeriol/new-api", git_connection_id: "g1", updated_at: ago(90) }),
  project("p4", "sakura-2api", { updated_at: ago(300) }),
  project("p5", "旧官网", { archived: true }),
  project("p6", "实验田", { archived: true }),
];

const look = (color, outfit, accessory, hair = null) => ({ color, outfit, accessory, hair });
const node = (id, name) => ({ id, name, online: true });
const st = (state, bubble, extra = {}) => ({ state, substate: "", bubble, bubble_kind: "", task_id: "", task_kind: "", tier: "", role: "", since: ago(4), seq: 1, ...extra });
const emp = (id, name, tier, role, title, runtime, nodeRef, lk, status) => ({ id, name, tier, role, title, runtime, node: nodeRef, look: lk, status });

const RC = node("n1", "rc-erio");
const CVP = node("n2", "cvp");

export const coordinator = emp("e0", "leader", "coordinator", "coordinator", "项目统筹", "claude", RC, look("#7A4FD1", "hoodie", "headset", "#3B2A20"), st("idle", ""));

const step = (id, pos, title, status, done = 0, total = 0) => ({ id, pos, title, status, auto_done: status === "done", tasks_total: total, tasks_done: done });
const counts = (c) => ({ waiting: 0, questions_open: 0, approvals_open: 0, tasks_total: 0, tasks_done: 0, main_total: 0, main_done: 0, ...c });

const members12 = [
  emp("e12l", "lead-12", "lead", "lead", "组长", "claude", RC, look("#C8622A", "suit", "none", "#3B2A20"), st("reading", "审查 dev-1 的提交（只读，不改代码）")),
  emp("e12d1", "dev-1", "member", "dev", "开发", "claude", RC, look("#4C7BD0", "tee", "none"), st("waiting", "等审查：审批走 permission-prompt-tool", { substate: "review" })),
  emp("e12d2", "dev-2", "member", "dev", "开发", "claude", CVP, look("#4C7BD0", "tee", "none"), st("raising_hand", "提问 / 审批卡片 · 等你批准一条命令", { bubble_kind: "ask" })),
  emp("e12q", "qa-1", "member", "qa", "测试", "codex", CVP, look("#3F8F6B", "coat", "glasses"), st("running", "PG 往返测试", { substate: "test" })),
];
const members13 = [
  emp("e13l", "lead-13", "lead", "lead", "组长", "claude", RC, look("#C8622A", "suit", "none", "#3B2A20"), st("thinking", "")),
  emp("e13d", "dev-1", "member", "dev", "开发", "claude", RC, look("#4C7BD0", "tee", "none"), st("idle", "")),
  emp("e13q", "qa-1", "member", "qa", "测试", "codex", CVP, look("#3F8F6B", "coat", "glasses"), st("raising_hand", "等你的回答")),
];

const thread = (no, title, status, extra) => ({
  id: `t${no}`, project_id: "p1", no, title, brief: "", status, queue_position: null, pause_reason: "", branch: `thread/${no}`, base_branch: "main",
  pr_number: null, pr_url: null, pr_state: null, summary: "", auto_approve: false, opened_by: "coordinator", members: [], main_tasks: [],
  counts: counts({}), lead_last: null, cost_usd_est: null, created_at: ago(70), updated_at: ago(10), ...extra,
});

export const threads = [
  thread(13, "Codex 员工接入", "running", {
    branch: "thread/13-codex", updated_at: ago(8), members: members13,
    summary: "组长问：节点上还没装 codex，先用 CODEX_API_KEY 还是你去登录？",
    main_tasks: [step("m131", 1, "CodexWorker 与事件解析", "done", 2, 2), step("m132", 2, "节点安装与登录", "doing", 1, 3), step("m133", 3, "联调", "todo", 0, 1)],
    counts: counts({ waiting: 1, questions_open: 1, tasks_total: 6, tasks_done: 3, main_total: 3, main_done: 1 }),
    lead_last: { text: "代码部分已完成并通过测试，剩下要在节点上装好 codex 才能联调。", at: ago(9) },
  }),
  thread(12, "员工通信与审批", "running", {
    branch: "thread/12-employee-comms", updated_at: ago(0.5), created_at: ago(70), members: members12,
    summary: "后端已完成待审查，网页卡片进行中；dev-2 有一条命令等你批准。",
    main_tasks: [
      step("m121", 1, "实现约定：工具、事件、数据表和接口", "done", 1, 1),
      step("m122", 2, "后端通信与审批", "done", 5, 5),
      step("m123", 3, "网页卡片：提问 / 审批、计划、子任务", "doing", 2, 4),
      step("m124", 4, "联调验证：真实节点跑通", "todo", 0, 3),
      step("m125", 5, "开 PR 并交统筹验收", "todo"),
    ],
    counts: counts({ waiting: 1, approvals_open: 1, tasks_total: 13, tasks_done: 7, main_total: 4, main_done: 2 }),
    lead_last: { text: "后端已合并进讨论串分支，网页卡片还差计划和子任务展示，预计再 1 小时。", at: ago(3) },
  }),
  thread(11, "项目菜单（置顶 / 归档 / 删除）", "review", {
    branch: "thread/11-project-menu", updated_at: ago(20), pr_number: 41, pr_url: "https://github.com/touwaeriol/agent-teams/pull/41", pr_state: "open",
    members: [emp("e11l", "lead-11", "lead", "lead", "组长", "claude", RC, look("#C8622A", "suit", "none"), st("idle", "")), emp("e11d", "dev-1", "member", "dev", "开发", "claude", RC, look("#4C7BD0", "tee", "none"), st("idle", "已完成")), emp("e11q", "qa-1", "member", "qa", "测试", "codex", CVP, look("#3F8F6B", "coat", "glasses"), st("idle", "已完成"))],
    summary: "全部完成，测试 248 项通过，PR #41 等你验收。",
    main_tasks: [step("m111", 1, "后端字段与接口", "done", 2, 2), step("m112", 2, "侧栏菜单", "done", 3, 3), step("m113", 3, "测试与 PR", "done", 1, 1)],
    counts: counts({ tasks_total: 6, tasks_done: 6, main_total: 3, main_done: 3 }), lead_last: { text: "全部完成，PR 已开。", at: ago(20) },
  }),
  thread(10, "新建项目的仓库和分支下拉框", "done", {
    branch: "thread/10-repo-combobox", updated_at: ago(60), pr_number: 39, pr_state: "merged",
    members: [emp("e10l", "lead-10", "lead", "lead", "组长", "claude", RC, look("#C8622A", "suit", "none"), st("idle", "")), emp("e10d", "dev-2", "member", "dev", "开发", "claude", CVP, look("#4C7BD0", "tee", "none"), st("idle", "已完成"))],
    summary: "仓库、基准分支改为可搜索下拉，已合并到 main。",
    main_tasks: [step("m101", 1, "仓库下拉", "done", 1, 1), step("m102", 2, "分支下拉", "done", 1, 1)],
    counts: counts({ tasks_total: 2, tasks_done: 2, main_total: 2, main_done: 2 }), lead_last: { text: "已合并。", at: ago(60) },
  }),
  thread(9, "GitHub 一键连接", "done", {
    branch: "thread/9-github-app", updated_at: ago(180), pr_number: 36, pr_state: "merged",
    members: [emp("e9l", "lead-9", "lead", "lead", "组长", "claude", RC, look("#C8622A", "suit", "none"), st("idle", "")), emp("e9d1", "dev-1", "member", "dev", "开发", "claude", RC, look("#4C7BD0", "tee", "none"), st("idle", "")), emp("e9d2", "dev-2", "member", "dev", "开发", "claude", CVP, look("#4C7BD0", "tee", "none"), st("idle", ""))],
    summary: "GitHub App 安装授权，令牌每次运行现取。",
    main_tasks: [1, 2, 3, 4].map((i) => step(`m9${i}`, i, `步骤 ${i}`, "done", 1, 1)),
    counts: counts({ tasks_total: 4, tasks_done: 4, main_total: 4, main_done: 4 }),
  }),
];

const q = (id, thread_no, asker, kind, text, extra = {}) => ({
  id, thread_no, asker, task_id: null, audience: "user", kind, text, options: [], detail: {}, blocking: true, answer: null, status: "open",
  decided_by: null, created_at: ago(5), answered_at: null, ...extra,
});

export const questions = {
  t12: [q("q1", 12, "dev-2", "approval", "允许执行命令？", { detail: { tool_name: "Bash", input: { command: "pnpm add -D @tanstack/react-virtual" } }, task_id: "t6" })],
  t13: [q("q2", 13, "lead-13", "question", "节点上还没装 codex，先用 CODEX_API_KEY 还是你去登录？", { options: ["用 CODEX_API_KEY", "我去登录"] })],
  c0: [],
};

const msg = (id, role, author, content, min, meta = {}) => ({ id, role, author, kind: "chat", content, meta, created_at: ago(min) });

export const messages = {
  c0: [
    msg("1", "user", "liu", "员工之间要能互相沟通，审批卡片要能在网页上点。另外 Codex 员工也接上。", 75),
    msg("2", "leader", "leader", "这是两件独立的事，我开了两个讨论串分别去做，有结论会回到这里告诉你。", 74, { opens_thread: 12 }),
    msg("3", "system", "platform", "已开讨论串 #13", 74, { opens_thread: 13 }),
    msg("4", "user", "liu", "项目菜单那个做完了吗？", 6),
    msg("5", "leader", "leader", "#11「项目菜单」做完了：置顶、重命名、归档、删除都好了，测试通过。PR #41 在右侧“等你处理”里，点开就能验收。", 5),
  ],
  t12: [
    msg("a1", "employee", "项目统筹 → 组长", "员工之间要能互相沟通，权限审批要在网页上点。先出实现约定，再并行做。", 70),
    msg("a2", "leader", "组长 lead-12", "拆成 4 个主要任务，13 个小任务已放进任务池。dev-1 领了后端，dev-2 领了网页，qa-1 等联调依赖完成。", 62),
    msg("a3", "employee", "dev-1 → 组长", "审批工具的返回格式按 permission-prompt-tool 要求做好了，请审查。", 24),
  ],
  t13: [], t11: [], t10: [], t9: [],
};

const task = (id, thread_no, main, role, who, status, instruction, extra = {}) => ({
  id, thread_no, main_task_id: main, assignee: "", role, claimed_by: who, kind: role === "测试" ? "test" : "dev", instruction, status, depends_on: [],
  parent: "", priority: 0, attempt: 1, summary: "", branch: "", head_sha: "", created_at: ago(60), claimed_at: null, finished_at: null, ...extra,
});

export const tasks = {
  t12: [
    task("t1", 12, "m122", "开发", "dev-1", "done", "Comms：信箱、提问、计划"),
    task("t2", 12, "m123", "开发", "dev-2", "done", "侧栏待处理小圆点"),
    task("t6", 12, "m123", "开发", "dev-2", "running", "提问 / 审批卡片"),
    task("t8", 12, "m123", "开发", "", "pending", "计划与子任务展示"),
    task("t5", 12, "m122", "开发", "dev-1", "review", "审批走 permission-prompt-tool"),
    task("t7", 12, "m122", "测试", "qa-1", "running", "PG 往返测试"),
    task("t9", 12, "m124", "测试", "", "pending", "审批超过 5 分钟不中断", { depends_on: ["t6"] }),
    ...[3, 4, 10, 11, 12].map((n) => task(`t${n}`, 12, "m122", "开发", "dev-1", "done", `后端小任务 ${n}`)),
  ],
};

export const activity = {
  t12: [
    { employee: "qa-1", text: "在等「后端通信」完成，暂时领了 PG 往返测试", at: ago(30) },
    { employee: "lead-12", text: "把「审批走 permission-prompt-tool」移到待审查", at: ago(20) },
    { employee: "dev-1 → dev-2", text: "接口字段 detail.tool_name 已定，按这个渲染", at: ago(12) },
    { employee: "dev-2", text: "领取了「提问 / 审批卡片」", at: ago(6) },
  ],
};

export const mergeQueue = {
  t12: [
    { id: "mq1", thread_no: 12, task_id: "t1", employee: "dev-1", head_branch: "thread/12/dev-1", status: "merged", attempts: 1, error: "", created_at: ago(40), updated_at: ago(35) },
    { id: "mq2", thread_no: 12, task_id: "t5", employee: "dev-2", head_branch: "thread/12/dev-2", status: "ci_running", attempts: 1, error: "", created_at: ago(10), updated_at: ago(2) },
  ],
};

export const checks = { t12: [{ name: "verify", status: "completed", conclusion: "success", url: "", summary: "" }, { name: "integration", status: "in_progress", conclusion: null, url: "", summary: "" }] };

export const memory = [
  { id: "mem1", text: "后端 FastAPI + asyncpg，前端 React + Vite（pnpm），测试在 at-dev 容器里跑。", source: "user", pinned: true, created_at: ago(3000) },
  { id: "mem2", text: "员工工具走 /mcp；审批用 permission-prompt-tool，等待期间每 30 秒发心跳。", source: "thread:12", pinned: false, created_at: ago(2000) },
  { id: "mem3", text: "改完前端要跑 tsc 和 vite build，构建产物不提交。", source: "thread:10", pinned: false, created_at: ago(1000) },
  { id: "mem4", text: "GitHub 用 App 安装授权，令牌每次运行现取，不落盘。", source: "thread:9", pinned: false, created_at: ago(500) },
];

const deliverable = (id, kind, title, thread_no, min, summary, extra = {}) => ({
  id, project_id: "p1", thread_no, kind, title, url: "about:blank", detail: { summary, ...extra }, published_by: "dev-2", created_at: ago(min), updated_at: ago(min),
});

export const deliverables = [
  deliverable("d1", "design", "审批卡片样式", 12, 12, "审批与提问卡片", { version: 3, author: "dev-2" }),
  deliverable("d2", "pr", "PR #41 项目菜单", 11, 20, "thread/11-project-menu → main"),
  deliverable("d3", "doc", "员工通信实现约定", 12, 60, "docs/agent-comms.md"),
  deliverable("d4", "design", "新建项目下拉框", 10, 120, "仓库与基准分支"),
  deliverable("d5", "data", "审批耗时统计", 12, 60 * 20, "平均等待 4 分钟"),
  deliverable("d6", "pr", "PR #36 GitHub 一键连接", 9, 60 * 22, "thread/9-github-app → main"),
  hosted("d7", "design", "06-nodes.png", 60 * 30, 9_400, "user"),
  hosted("d8", "design", "e2e-main.png", 60 * 31, 381_800, "user"),
  hosted("d9", "doc", "架构说明.md", 60 * 33, 24_100, "designer-1"),
  hosted("d10", "design", "落地页 v2", 60 * 34, 208_600, "designer-1", "index.html"),
];

function hosted(id, kind, title, min, size, by, entry = title) {
  return { id, project_id: "p1", thread_no: by === "user" ? null : 12, kind, title, url: "", detail: { hosted: true, entry, size, version: 1, files: 1, by, render: entry.endsWith(".md") ? "markdown" : entry.endsWith(".html") ? "html" : "image" }, published_by: by, created_at: ago(min), updated_at: ago(min) };
}

export const schedules = [
  { id: "sc1", project_id: "p1", name: "每日进展", cron: "0 9 * * *", timezone: "Asia/Shanghai", message: "汇总昨天各讨论串的进展，有卡住的告诉我。", enabled: true, next_run_at: ago(-600), last_run_at: ago(840), last_error: "", created_at: ago(60 * 24 * 3), updated_at: ago(840) },
  { id: "sc2", project_id: "p1", name: "周一清理", cron: "0 10 * * 1", timezone: "Asia/Shanghai", message: "列出超过一周没动的讨论串，建议归档哪些。", enabled: false, next_run_at: null, last_run_at: null, last_error: "", created_at: ago(60 * 24 * 2), updated_at: ago(60) },
];

const tok = (input, output) => ({ input, output, cache_read: input * 3, cache_write: Math.round(input / 4) });
export const usage = {
  days: 7, cost_usd_est: 18.42, turns: 214, tokens: tok(1_240_000, 182_000),
  by_day: [6, 5, 4, 3, 2, 1, 0].map((d, i) => ({ day: new Date(Date.now() - d * 86_400_000).toISOString().slice(0, 10), cost_usd: [1.2, 2.8, 0, 3.4, 4.1, 2.2, 4.72][i], tokens: tok(100_000, 15_000) })),
  by_thread: [
    { thread_no: 12, title: "审批卡片与员工通信", cost_usd: 9.8, tokens: tok(640_000, 96_000) },
    { thread_no: 13, title: "Codex 运行时", cost_usd: 5.1, tokens: tok(380_000, 52_000) },
    { thread_no: null, title: "", cost_usd: 3.52, tokens: tok(220_000, 34_000) },
  ],
  by_employee: [
    { employee: "dev-2", thread_no: 12, cost_usd: 5.2, tokens: tok(330_000, 51_000) },
    { employee: "lead-12", thread_no: 12, cost_usd: 3.1, tokens: tok(210_000, 30_000) },
    { employee: "coordinator", thread_no: null, cost_usd: 3.52, tokens: tok(220_000, 34_000) },
    { employee: "dev-1", thread_no: 13, cost_usd: 2.9, tokens: tok(190_000, 26_000) },
  ],
};

export const nodes = [
  { id: "n1", name: "rc-erio", online: true, since: ago(120), last_seen_at: ago(0), version: "2.0.3", protocol: 2, isolation: "agent_user", runtimes: { claude: { sdk: "0.3.325", cli: "2.1.322", auth: "subscription" }, codex: {} }, capacity: { max_active_sessions: 4 }, active_sessions: 2, parked_sessions: 1, queued: 0, isolated: true, labels: [], max_active_sessions: 4, park_after_s: null, created_at: ago(9000) },
  { id: "n2", name: "cvp", online: true, since: ago(120), last_seen_at: ago(0), version: "2.0.3", protocol: 2, isolation: "sandbox", runtimes: { claude: { cli: "2.1.322", auth: "subscription" }, codex: { cli: "0.169.0", auth: "chatgpt" } }, capacity: { max_active_sessions: 4 }, active_sessions: 1, parked_sessions: 0, queued: 0, isolated: false, labels: ["big-disk"], max_active_sessions: 4, park_after_s: null, created_at: ago(9000) },
];

export const nodeSessions = {
  n1: [
    { session_id: "s1", employee: "统筹 · agent team", project: "agent team", thread_no: null, state: "active", last_active_at: ago(1) },
    { session_id: "s2", employee: "lead-12", project: "员工通信与审批 · 只读", thread_no: 12, state: "active", last_active_at: ago(0) },
  ],
  n2: [{ session_id: "s3", employee: "dev-2", project: "提问 / 审批卡片", thread_no: 12, state: "active", last_active_at: ago(0) }],
};

const role = (id, tier, roleKey, title, runtime, count, node_ids, description, lk, extra = {}) => ({
  id, tier, role: roleKey, title, runtime, count, node_ids, description, instructions: "", model: "", effort: "", skip_permissions: false,
  can_delegate: false, read_only: null, allowed_tools: [], disallowed_tools: [], sandbox: "", max_turns: null, gate: "", task_kinds: [], skills: [], plugins: [], look: lk, sort: 0, ...extra,
});

export const roles = [
  role("r0", "coordinator", "coordinator", "项目统筹", "claude", 1, ["n1"], "和你对话，判断要做什么，开讨论串、跟进状态、向你汇报。", look("#7A4FD1", "hoodie", "headset", "#3B2A20"), { model: "opus" }),
  role("r1", "lead", "lead", "组长", "claude", 1, ["n1", "n2"], "列主要任务、拆小任务、审查成员提交并合并，完成后向统筹交总结。", look("#C8622A", "suit", "none", "#3B2A20"), { model: "opus" }),
  role("r2", "member", "dev", "开发", "claude", 2, ["n1", "n2"], "从任务池领开发类小任务，在自己的 worktree 里改代码、跑测试、提交。", look("#4C7BD0", "tee", "none", "#8A5A36"), { model: "sonnet", sort: 1, task_kinds: ["dev"], skills: ["docs"], plugins: ["release-notes@2", "pr-review"] }),
  role("r3", "member", "qa", "测试", "codex", 1, ["n2"], "领测试类小任务：补用例、跑全量测试、复现问题并报告。", look("#3F8F6B", "coat", "glasses", "#6B4A2E"), { sort: 2, skip_permissions: true, sandbox: "workspace-write", task_kinds: ["test"], plugins: ["playwright-kit"] }),
  role("r4", "member", "review", "审查", "claude", 1, ["n1"], "审查合并前的改动，指出问题，不改代码。", look("#4A4843", "suit", "glasses", "#1F1E1C"), { sort: 3, gate: "review", model: "opus" }),
];

export const connections = [
  { id: "g1", name: "touwaeriol", provider: "github", login: "touwaeriol", kind: "app", installation_id: 1234, created_at: ago(60 * 24 * 1), permissions: { contents: "write", pull_requests: "write" }, needs_permission_approval: false, installation_settings_url: "https://github.com/settings/installations/1234" },
];

const repo = (full_name, default_branch, priv, used, min) => ({ full_name, private: priv, default_branch, description: "", html_url: `https://github.com/${full_name}`, pushed_at: ago(min), used_by_projects: used });
export const repos = [
  repo("touwaeriol/agent-teams", "main", true, [{ id: "p1", name: "agent team" }], 5),
  repo("touwaeriol/sup2api", "develop", true, [{ id: "p2", name: "sup2api" }], 50),
  repo("touwaeriol/new-api", "main", true, [{ id: "p3", name: "new-api" }], 90),
  repo("touwaeriol/claude-relay-service", "main", false, [], 300),
  ...Array.from({ length: 33 }, (_, i) => repo(`touwaeriol/repo-${i + 1}`, "main", i % 3 === 0, [], 1000 + i * 60)),
];

export const adminUsers = [
  { ...me, email: "admin@prophey.ai", nodes: 2, running_tasks: 0, last_login_at: ago(5), max_running_threads: null },
  { id: "u2", email: "dev@example.com", display_name: "", role: "user", active: true, max_nodes: null, must_change_password: false, platform_version: 2, created_at: ago(60 * 24), nodes: 1, running_tasks: 0, last_login_at: ago(600), max_running_threads: null },
  { id: "u3", email: "old@example.com", display_name: "", role: "user", active: false, max_nodes: null, must_change_password: false, platform_version: 2, created_at: ago(60 * 24 * 5), nodes: 0, running_tasks: 0, last_login_at: null, max_running_threads: null },
];

export const adminSettings = { registration_open: true, default_max_nodes: 5, max_running_threads: 4, delivery_default: delivery, office_default: "default" };

export const skills = [
  { id: "", name: "design", description: "单文件 HTML 原型、界面稿和 SVG 示意图，做完用 publish_file 发布。", builtin: true, size: 4200 },
  { id: "", name: "docs", description: "设计文档和说明文档的写法：结构、术语、图示。", builtin: true, size: 3100 },
  { id: "sk_brand", name: "brand-voice", description: "公司对外文案的语气和用词规范。", builtin: true, size: 2400 },
  { id: "sk_1", name: "pitch", description: "路演材料的结构和讲法。", builtin: false, size: 1800 },
];

const contents = (skills, commands, agents, hooks, mcp) => ({ skills, commands, agents, hooks, mcp_servers: mcp });
const pversion = (version, manifest_version, description, c, min, size = 18_400) => ({ version, manifest_version, description, size, sha256: "0".repeat(64), contents: c, created_at: ago(min) });
export const plugins = [
  { id: "pl_1", name: "release-notes", builtin: false, enabled: true, latest: 3, versions: [
    pversion(3, "1.2.0", "从合并的 PR 生成发布说明和变更日志。", contents(["changelog"], ["release-notes"], ["notes-writer"], false, []), 90),
    pversion(2, "1.1.0", "从合并的 PR 生成发布说明。", contents(["changelog"], ["release-notes"], [], false, []), 60 * 24 * 2),
    pversion(1, "1.0.0", "", contents(["changelog"], [], [], false, []), 60 * 24 * 6),
  ] },
  { id: "pl_2", name: "sql-helper", builtin: false, enabled: false, latest: 1, versions: [
    pversion(1, "0.3.0", "只读查询测试库，给出执行计划。", contents([], ["explain"], [], false, ["pg"]), 60 * 24 * 3, 6_200),
  ] },
  { id: "pl_3", name: "pr-review", builtin: true, enabled: true, latest: 2, versions: [
    pversion(2, "2.0.1", "按公司规范审查改动：安全、测试覆盖、命名。", contents(["review-checklist"], ["review"], ["security-reviewer", "test-reviewer"], true, []), 60 * 5),
    pversion(1, "2.0.0", "", contents(["review-checklist"], ["review"], ["security-reviewer"], true, []), 60 * 24 * 9),
  ] },
  { id: "pl_4", name: "playwright-kit", builtin: true, enabled: true, latest: 1, versions: [
    pversion(1, "1.56.0", "浏览器端到端测试：录制、断言和截图。", contents(["e2e"], ["screenshot"], [], false, ["playwright"]), 60 * 24, 42_000),
  ] },
];

export const adminPlugins = [
  plugins[2],
  plugins[3],
  { id: "pl_5", name: "legacy-deploy", builtin: true, enabled: false, latest: 4, versions: [
    pversion(4, "0.9.4", "旧的部署脚本封装，已被 CI 取代。", contents([], ["deploy"], [], true, []), 60 * 24 * 30, 9_800),
  ] },
];
