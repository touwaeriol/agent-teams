#!/usr/bin/env node
// End-to-end run with REAL Claude employees against the local v2 stack (deploy/v2/e2e/up.sh):
// registers a user, installs the daemon tarball the control plane serves at /node/dist/, starts
// it under <scratch>/node (never ~/.config/agent-teams/node.env), creates a project on the
// localgh repository local/hello, configures coordinator + lead + one developer, asks the
// coordinator for hello.txt and checks the whole flow over REST and SSE.
//   node deploy/v2/e2e/run.mjs <scratch-dir> [--model sonnet]
import { createWriteStream, existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { answerApprovals as answerAll, ASK_ME, gitShow as gitShowIn, installDaemon, login as loginAt, nodeOnline, record, roleBody, sql, startDaemon } from "./harness.mjs";
import { SSE, check, readEnv, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "sonnet";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node");
const statePath = join(dir, "run-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));
const log = createWriteStream(join(dir, "run-events.jsonl"), { flags: "a" });

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one or two sentences. For any change to the repository, call open_thread with a one-line brief; never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: "Keep it minimal: call update_plan with one main task, then assign_task exactly one task to role dev with the full instruction. When the dev reports the task done, call enqueue_merge for it, then report in one sentence. Do not review at length." },
  { tier: "member", role: "dev", title: "开发", count: 1,
    instructions: "Do exactly the assigned task with the Write tool, then call complete_task with a one-line summary. No extra files, no tests, no exploration." },
];

async function setup(c) {
  if (state.projectId) return;
  const node = await c.post("/api/nodes", { name: "rc-e2e" });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  const conn = await c.post("/api/git/connections", { name: "local", token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: "hello e2e", delivery: ASK_ME, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  state.projectId = project.id;
  const roles = await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody(r, i, model)) });
  check(roles.roles.length === 3, "role templates saved (coordinator, lead, dev)", roles.warnings);
  save();
}

async function threadOf(c) {
  const threads = await c.get(`/api/projects/${state.projectId}/threads`);
  return threads?.[0];
}

const login = () => loginAt(base);
const answerApprovals = (c, answered) => answerAll(c, state.projectId, answered);

async function progress(c, answered) {
  await answerApprovals(c, answered);
  const t = await threadOf(c);
  if (!t) return null;
  const tasks = (await c.get(`/api/threads/${t.id}/tasks`)) ?? [];
  console.log(`  thread #${t.no} ${t.status}; tasks ${tasks.map((x) => `${x.id}:${x.kind}:${x.status}@${x.claimed_by ?? "-"}`).join(", ")}`);
  return { t, tasks };
}

const gitShow = (ref, path) => gitShowIn(env.E2E_GIT_ROOT, "local/hello", ref, path);

async function main() {
  const c = await login();
  await setup(c);
  // E2E_DAEMON=node/dist/node.mjs runs a local build instead of the served tarball (faster iteration).
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: state.nodeToken, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await drive(c);
  } finally {
    daemon.kill("SIGTERM");
    log.end();
  }
}

async function drive(c) {
  const node = await waitFor("node online", () => nodeOnline(c, state.nodeId), { timeoutMs: 60_000 });
  check(true, `daemon online: ${node.version ?? ""} runtimes ${JSON.stringify(node.runtimes ?? {})}`);
  const sse = new SSE(c, `/api/projects/${state.projectId}/events`);
  const recorder = record(sse, log);
  const coord = await c.get(`/api/projects/${state.projectId}/coordinator`);
  if (!state.sent) {
    await c.post(`/api/conversations/${coord.conversation_id}/messages`, { content: "Please add a file hello.txt at the repository root containing exactly: hello from agent-teams v2" });
    state.sent = true;
    save();
  }
  const answered = new Map();
  const thread = await waitFor("coordinator opens a thread", async () => (await progress(c, answered))?.t, { timeoutMs: 300_000, everyMs: 5000 });
  check(Boolean(thread), `coordinator opened thread #${thread.no}: ${thread.title}`);
  const done = await waitFor("dev completes the task", async () => {
    const p = await progress(c, answered);
    const task = p?.tasks.find((x) => x.kind !== "review" && x.kind !== "fix" && x.claimed_by);
    return task && ["review", "merging", "done"].includes(task.status) && { ...p, task };
  }, { timeoutMs: 900_000, everyMs: 5000 });
  await verify(c, sse, coord, done, answered);
  await waitFor("lead merges the task", async () => {
    const p = await progress(c, answered);
    return p?.tasks.find((x) => x.id === done.task.id && x.status === "done");
  }, { timeoutMs: 600_000, everyMs: 5000 }).then(() => {
    check(gitShow(done.t.branch, "hello.txt")?.trim() === "hello from agent-teams v2", `merge queue merged hello.txt into ${done.t.branch}`);
  }, (e) => console.log(`note: ${e.message}`));
  await sleep(1500);
  clearInterval(recorder);
  sse.close();
  writeFileSync(join(dir, "run-summary.json"), JSON.stringify({ state, thread: await threadOf(c), sseTypes: sse.types(), events: sse.events.length }, null, 2));
}

async function verify(c, sse, coord, done, answered) {
  const { t, task } = done;
  check(task.claimed_by && task.claimed_by.startsWith("dev"), `task ${task.id} claimed by ${task.claimed_by}`);
  const mainTasks = await c.get(`/api/threads/${t.id}/main-tasks`);
  check((mainTasks ?? []).length >= 1, "lead created the plan (main tasks)", mainTasks);
  const wt = join(nodeDir, "root", "projects", state.projectId, "threads", String(t.no), task.claimed_by, "local__hello", "hello.txt");
  check(existsSync(wt) && readFileSync(wt, "utf8").trim() === "hello from agent-teams v2", `dev wrote hello.txt in its worktree ${wt}`);
  const pushed = gitShow(task.branch ?? `thread/${t.no}/${task.claimed_by}`, "hello.txt");
  check(pushed?.trim() === "hello from agent-teams v2", `complete_task pushed ${task.branch} to the repository`);
  check([...answered.values()].some((q) => q.kind === "approval"), "an approval card appeared and was answered through the API",
    [...answered.values()].map((q) => q.text));
  const coordDir = join(nodeDir, "root", "projects", state.projectId, "coordinator");
  check(!existsSync(join(coordDir, "hello.txt")) && gitShow("main", "hello.txt") === null, "coordinator did not touch code (no hello.txt outside the dev worktree, main unchanged)");
  const coordTools = JSON.parse(sql(`SELECT s.init->'tools' FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id
    WHERE e.project_id = '${state.projectId}' AND e.tier = 'coordinator' AND s.state <> 'closed'`) || "[]");
  // TodoWrite (checklist card) and AskUserQuestion (platform question card) are chat tools every tier has (architecture.md §6.6).
  const chatTools = new Set(["TodoWrite", "AskUserQuestion"]);
  check(coordTools.length > 0 && coordTools.every((n) => n.startsWith("mcp__platform__") || chatTools.has(n)),
    `coordinator session has only platform and chat tools (${coordTools.join(", ")})`);
  const calls = sql(`SELECT t.tool FROM at_tool_calls t JOIN at_agent_sessions s ON s.id = t.session_id JOIN at_employees e ON e.id = s.employee_id
    WHERE e.project_id = '${state.projectId}' AND e.tier = 'coordinator' ORDER BY t.created_at`).split("\n").filter(Boolean);
  check(calls.includes("open_thread"), `coordinator called ${calls.join(", ")}`);
  const types = sse.types();
  check(types.includes("status") && types.includes("thread") && types.includes("task"), `SSE delivered status/thread/task events (${types.join(", ")})`);
  const office = await c.get(`/api/projects/${state.projectId}/office`);
  const emps = office.employees ?? [];
  check(emps.length >= 3 && emps.some((e) => (e.status?.state ?? e.state) && String(e.name ?? e.id).startsWith("dev")), "office live endpoint lists the employees with their states",
    emps.map((e) => `${e.name}:${e.status?.state ?? e.state}`));
  console.log(`  office: ${emps.map((e) => `${e.name}=${e.status?.state ?? e.state}${e.status?.substate ? "/" + e.status.substate : ""}`).join(" ")}`);
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
