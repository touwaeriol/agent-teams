#!/usr/bin/env node
// Short real-Claude run for permission modes and lead review (deploy/e2e/up.sh first):
// The dev is an ask role (逐条问), the only mode that still raises approval cards; the default
// bypass is covered by run_bypass.mjs.
//   1. the first write goes to the thread lead first, which allows it (the user never sees that card)
//   2. (removed: auto mode no longer exists)
//   3. a second one the lead hands over (ask_user) becomes the user's card, answered through the
//      API, and the member finishes its task
// Pauses wait for <scratch>/go-<name> ("running", "escalated"), at most 15 min, for screenshots.
//   node deploy/e2e/run_permissions.mjs <scratch-dir> [--model haiku]
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-perm");
const outside = join(dir, "outside");
const statePath = join(dir, "perm-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));

const LEAD_RULE = "When you receive a [Permission request qN …] message, call review_permission right away: " +
  "if its 内容 path contains needs-user, decision ask_user with reason \"写到用户目录，请用户确认\"; otherwise decision allow with reason \"任务要求的输出文件\".";

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one sentence. For any request, call open_thread with the user's message copied verbatim as the brief; never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: `Keep it minimal: call update_plan with one main task, then assign_task exactly one task to role dev whose instruction is the brief copied verbatim. When the dev reports the task done, report in one sentence. ${LEAD_RULE}` },
  { tier: "member", role: "dev", title: "开发", count: 1, permission_mode: "ask",
    instructions: "Follow the numbered steps exactly, one tool call per step, using the Write tool for every file (never Bash for writing). Then call complete_task with a one-line summary. No other files, no tests, no exploration." },
];

const brief = () => [
  "Do these steps in order:",
  `1. Write the file ${join(outside, "approved.txt")} containing exactly: approved by the lead`,
  `2. Write the file ${join(outside, "needs-user.txt")} containing exactly: approved by the user`,
  "3. Call complete_task.",
].join("\n");

async function setup(c) {
  if (state.projectId) return;
  const roles = await c.put("/api/roles", { roles: ROLES.map((r, i) => ({ ...roleBody(r, i, model), permission_mode: r.permission_mode ?? "ask" })) });
  check(roles.roles.find((r) => r.role === "dev")?.permission_mode === "ask", `roles saved, dev in ask mode (model ${model})`, roles.warnings);
  const node = await c.post("/api/nodes", { name: `rc-e2e-perm-${Date.now() % 100000}` });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  save();
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `permissions e2e ${Date.now() % 100000}`,
    delivery: { approval_routing: "lead_first", lead_review_timeout_s: 900 }, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(project.delivery?.approval_routing === "lead_first", "project routes approvals to the lead first");
  state.projectId = project.id;
  save();
}

async function pause(name) {
  const marker = join(dir, `go-${name}`);
  writeFileSync(join(dir, `paused-${name}`), JSON.stringify(state));
  console.log(`  pause "${name}": waiting for ${marker}`);
  await waitFor(`screenshots (${name})`, async () => existsSync(marker), { timeoutMs: 900_000, everyMs: 2000 });
}

/** userCards polls the user's waiting list and remembers every approval card it ever showed. */
async function userCards(c) {
  const waiting = await c.get(`/api/projects/${state.projectId}/waiting`);
  return (waiting.questions ?? []).filter((q) => q.kind === "approval");
}

const pathOf = (q) => String(q.detail?.input?.file_path ?? q.detail?.input?.command ?? "");

function approvals(threadId) {
  const rows = sql(`SELECT id || '|' || status || '|' || audience || '|' || coalesce(decided_by, '') || '|' || coalesce(answer, '') || '|' ||
    (detail->>'tool_name') || '|' || (detail ? 'escalated_at')::text || '|' || coalesce(detail->'input'->>'file_path', detail->'input'->>'command', '')
    FROM at_questions WHERE thread_id = '${threadId}' AND kind = 'approval' ORDER BY created_at`);
  return rows ? rows.split("\n").map((l) => { const [id, status, audience, by, answer, tool, escalated, path] = l.split("|"); return { id, status, audience, by, answer, tool, escalated: escalated === "true", path }; }) : [];
}

async function openThread(c, coord) {
  if (state.threadId) return c.get(`/api/threads/${state.threadId}`);
  await c.post(`/api/conversations/${coord.conversation_id}/messages`, { content: brief() });
  const t = await waitFor("coordinator opens a thread", async () => (await c.get(`/api/projects/${state.projectId}/threads`))?.[0],
    { timeoutMs: 300_000, everyMs: 4000 });
  state.threadId = t.id;
  state.threadNo = t.no;
  save();
  check(true, `coordinator opened thread #${t.no}: ${t.title}`);
  await waitFor("the dev's task is in progress", async () =>
    ((await c.get(`/api/threads/${t.id}/tasks`)) ?? []).find((x) => x.claimed_by?.startsWith("dev") && x.status === "running"),
  { timeoutMs: 600_000, everyMs: 3000 });
  await pause("running");
  return t;
}

async function leadDecides(t) {
  const a = await waitFor("the lead decides the first outside write", async () =>
    approvals(t.id).find((x) => x.path.endsWith("approved.txt") && x.status === "answered"), { timeoutMs: 600_000, everyMs: 2000 });
  check(a.by.startsWith("lead:") && a.answer === "allow", `lead review: ${a.id} ${a.tool} ${a.path} → ${a.answer} by ${a.by}`);
  check(a.audience === "lead" && !a.escalated, "the card the lead decided was never addressed to the user");
  await waitFor("approved.txt written", async () => existsSync(join(outside, "approved.txt")), { timeoutMs: 300_000 });
  check(readFileSync(join(outside, "approved.txt"), "utf8").includes("approved by the lead"), "the member wrote approved.txt after the lead allowed it");
}

async function userDecides(c, t) {
  const card = await waitFor("the lead hands the second write to the user", async () =>
    (await userCards(c)).find((q) => pathOf(q).endsWith("needs-user.txt")), { timeoutMs: 600_000, everyMs: 2000 });
  const review = card.detail.review ?? {};
  check(card.audience === "user" && review.decision === "ask_user" && String(review.by).startsWith("lead:"),
    `escalated: ${card.id} reached the user with ${review.by} 的理由 "${review.reason}"`);
  await pause("escalated");
  await c.post(`/api/conversations/${card.conversation_id}/questions/${card.id}/answer`, { answer: "allow" });
  const done = await waitFor("the dev completes its task", async () =>
    ((await c.get(`/api/threads/${t.id}/tasks`)) ?? []).find((x) => x.claimed_by?.startsWith("dev") && ["review", "merging", "done"].includes(x.status)),
  { timeoutMs: 600_000, everyMs: 3000 });
  check(readFileSync(join(outside, "needs-user.txt"), "utf8").includes("approved by the user"), "the member wrote needs-user.txt after the user allowed it");
  check(true, `the member continued and completed ${done.id} (${done.status})`);
  const final = approvals(t.id);
  check(final.length === 2 && final[1].by.startsWith("user:"), `exactly two approval requests in the thread: ${final.map((a) => `${a.path.split("/").pop()}=${a.answer}/${a.by.split(":")[0]}`).join(", ")}`);
  const feed = JSON.stringify(await c.get(`/api/threads/${t.id}/activity?limit=100`));
  check(feed.includes("放行了") && feed.includes("转给你决定"), "the activity feed records the lead's allow and hand-over");
}

async function main() {
  rmSync(outside, { recursive: true, force: true });
  mkdirSync(outside, { recursive: true });
  const c = await login(base);
  await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: state.nodeToken, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    const node = await waitFor("node online", () => nodeOnline(c, state.nodeId), { timeoutMs: 60_000 });
    check(true, `daemon online: ${node.version ?? ""}`);
    const coord = await c.get(`/api/projects/${state.projectId}/coordinator`);
    const t = await openThread(c, coord);
    const mode = sql(`SELECT s.permission_mode FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id WHERE s.thread_id = '${t.id}' AND e.tier = 'member' LIMIT 1`);
    check(mode === "default", `the dev's session opened in permission mode ${mode}`);
    await leadDecides(t);
    await userDecides(c, t);
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
