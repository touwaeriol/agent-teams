#!/usr/bin/env node
// Short real-Claude run for the default permission mode, bypass (deploy/e2e/up.sh first):
//   1. a member in bypass runs Bash, writes in its worktree and outside it: no approval card at all
//      (at_questions has no approval rows for the thread)
//   2. the member stops with ask_user; the question reaches the user, is answered through the API,
//      the member writes the answer down and completes its task
// The node is a plain one (not marked isolated): bypass applies on any node.
//   node deploy/e2e/run_bypass.mjs <scratch-dir> [--model haiku]
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-bypass");
const outside = join(dir, "bypass-outside");
const statePath = join(dir, "bypass-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one sentence. For any request, call open_thread with the user's message copied verbatim as the brief; never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: "Keep it minimal: call update_plan with one main task, then assign_task exactly one task to role dev whose instruction is the brief copied verbatim. When the dev reports the task done, report in one sentence." },
  { tier: "member", role: "dev", title: "开发", count: 1, permission_mode: "bypass",
    instructions: "Follow the numbered steps exactly, one tool call per step. No other files, no tests, no exploration." },
];

const brief = () => [
  "Do these steps in order:",
  "1. Run the Bash command: ls -la > listing.txt && mkdir -p build && echo built > build/out.txt",
  "2. Use the Write tool to write hello.txt at the repository root containing exactly: hello from bypass",
  `3. Use the Write tool to write the file ${join(outside, "outside.txt")} containing exactly: written without asking`,
  "4. Call ask_user with the question \"要用哪个颜色？\" and the options [\"red\", \"blue\"], and wait for the answer.",
  "5. Use the Write tool to write answer.txt at the repository root containing exactly the answer you got.",
  "6. Call complete_task with a one-line summary.",
].join("\n");

async function setup(c) {
  if (state.projectId) return;
  const roles = await c.put("/api/roles", { roles: ROLES.map((r, i) => ({ ...roleBody(r, i, model), permission_mode: r.permission_mode ?? "bypass" })) });
  check(roles.roles.find((r) => r.role === "dev")?.permission_mode === "bypass" && roles.warnings.length === 0,
    `roles saved, dev in bypass mode with no warnings (model ${model})`, roles.warnings);
  const node = await c.post("/api/nodes", { name: `rc-e2e-bypass-${Date.now() % 100000}` });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  save();
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `bypass e2e ${Date.now() % 100000}`,
    delivery: { approval_routing: "lead_first" }, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  state.projectId = project.id;
  save();
}

const approvalRows = (threadId) => Number(sql(`SELECT count(*) FROM at_questions WHERE thread_id = '${threadId}' AND kind = 'approval'`));

function worktreeFile(t, name) {
  const found = execFileSync("find", [join(nodeDir, "root", "projects", state.projectId, "threads", String(t.no)), "-name", name, "-not", "-path", "*/.git/*"])
    .toString().trim().split("\n")[0];
  return found ? readFileSync(found, "utf8") : null;
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
    check(!node.isolated, `daemon online on a plain node (isolation ${node.isolation}, isolated ${node.isolated}): ${node.version ?? ""}`);
    const coord = await c.get(`/api/projects/${state.projectId}/coordinator`);
    await c.post(`/api/conversations/${coord.conversation_id}/messages`, { content: brief() });
    const t = await waitFor("coordinator opens a thread", async () => (await c.get(`/api/projects/${state.projectId}/threads`))?.[0],
      { timeoutMs: 300_000, everyMs: 4000 });
    state.threadId = t.id;
    save();
    check(true, `coordinator opened thread #${t.no}: ${t.title}`);

    const question = await waitFor("the dev's ask_user reaches the user", async () =>
      ((await c.get(`/api/projects/${state.projectId}/waiting`)).questions ?? []).find((q) => q.kind !== "approval" && String(q.asker).startsWith("dev")),
    { timeoutMs: 900_000, everyMs: 3000 });
    const mode = sql(`SELECT s.permission_mode FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id WHERE s.thread_id = '${t.id}' AND e.tier = 'member' LIMIT 1`);
    check(mode === "bypassPermissions", `the dev's session opened in permission mode ${mode}`);
    check(worktreeFile(t, "listing.txt")?.startsWith("total") && worktreeFile(t, "out.txt")?.trim() === "built", "the dev ran Bash (ls, mkdir, echo) in its worktree");
    check(worktreeFile(t, "hello.txt")?.includes("hello from bypass"), "the dev wrote hello.txt with the Write tool");
    check(readFileSync(join(outside, "outside.txt"), "utf8").includes("written without asking"), "the dev wrote a file outside its worktree");
    check(approvalRows(t.id) === 0, "no approval rows in at_questions for the thread before the question");
    check(question.audience === "user" || question.audience === undefined, `ask_user reached the user: ${question.id} "${question.text}" options ${JSON.stringify(question.options ?? [])}`);

    await c.post(`/api/conversations/${question.conversation_id}/questions/${question.id}/answer`, { answer: "blue" });
    const done = await waitFor("the dev completes its task", async () =>
      ((await c.get(`/api/threads/${t.id}/tasks`)) ?? []).find((x) => x.claimed_by?.startsWith("dev") && ["review", "merging", "done"].includes(x.status)),
    { timeoutMs: 600_000, everyMs: 3000 });
    check(worktreeFile(t, "answer.txt")?.includes("blue"), "the dev got the user's answer and wrote answer.txt");
    check(true, `the dev completed ${done.id} (${done.status})`);
    check(approvalRows(t.id) === 0, "still no approval rows in at_questions for the thread");
    const any = Number(sql(`SELECT count(*) FROM at_questions WHERE project_id = '${state.projectId}' AND kind = 'approval'`));
    check(any === 0, "no approval rows anywhere in the project (coordinator, lead and dev)");
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
