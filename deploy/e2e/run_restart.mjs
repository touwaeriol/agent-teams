#!/usr/bin/env node
// Short real-Claude run for a daemon restart while a member waits on an approval card
// (deploy/e2e/up.sh first):
//   1. the dev's write outside its worktree raises the user's card
//   2. the daemon is restarted (SIGTERM, like systemctl restart) while the card is open
//   3. the dev is resumed with the restart note, re-issues the write, and the same card is
//      attached to the new request (never cancelled, no second card)
//   4. the card is answered through the API and the dev completes its task
//   node deploy/e2e/run_restart.mjs <scratch-dir> [--model haiku]
import { existsSync, mkdirSync, readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { ASK_ME, installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-restart");
const outside = join(dir, "outside-restart");
const target = join(outside, "restarted.txt");

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one sentence. For any request, call open_thread with the user's message copied verbatim as the brief; never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: "Keep it minimal: call update_plan with one main task, then assign_task exactly one task to role dev whose instruction is the brief copied verbatim. When the dev reports the task done, report in one sentence." },
  { tier: "member", role: "dev", title: "开发", count: 1,
    instructions: "Follow the numbered steps exactly, one tool call per step, using the Write tool for every file (never Bash). Then call complete_task with a one-line summary. No other files, no tests, no exploration." },
];

const brief = () => [
  "Do these steps in order:",
  `1. Write the file ${target} containing exactly: written after the restart`,
  "2. Call complete_task.",
].join("\n");

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody(r, i, model)) });
  const node = await c.post("/api/nodes", { name: `rc-e2e-restart-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `restart e2e ${Date.now() % 100000}`, delivery: ASK_ME,
    repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

function cards(threadId) {
  const rows = sql(`SELECT id || '|' || status || '|' || coalesce(approval_id, '') || '|' || (detail ? 'detached_at')::text || '|' ||
    (detail ? 'reattached_at')::text || '|' || coalesce(detail->'input'->>'file_path', '') FROM at_questions
    WHERE thread_id = '${threadId}' AND kind = 'approval' ORDER BY created_at`);
  return rows ? rows.split("\n").map((l) => {
    const [id, status, approval, detached, reattached, path] = l.split("|");
    return { id, status, approval, detached: detached === "true", reattached: reattached === "true", path };
  }) : [];
}

const waitingCard = async (c, projectId) =>
  ((await c.get(`/api/projects/${projectId}/waiting`)).questions ?? []).find((q) => q.kind === "approval" && String(q.detail?.input?.file_path).endsWith("restarted.txt"));

async function restart(daemon, spawnDaemon) {
  const exited = new Promise((r) => daemon.once("exit", r));
  daemon.kill("SIGTERM");
  await exited;
  check(true, "daemon stopped (SIGTERM) while the card was open");
  return spawnDaemon();
}

async function main() {
  rmSync(outside, { recursive: true, force: true });
  mkdirSync(outside, { recursive: true });
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true }); // a new node each run (pkg kept)
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const spawnDaemon = () => startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  let daemon = spawnDaemon();
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const coord = await c.get(`/api/projects/${st.projectId}/coordinator`);
    await c.post(`/api/conversations/${coord.conversation_id}/messages`, { content: brief() });
    const t = await waitFor("coordinator opens a thread", async () => (await c.get(`/api/projects/${st.projectId}/threads`))?.[0],
      { timeoutMs: 300_000, everyMs: 4000 });
    const before = await waitFor("the dev's write raises the user's card", () => waitingCard(c, st.projectId), { timeoutMs: 600_000, everyMs: 2000 });
    check(true, `card ${before.id} open for ${before.detail.input.file_path} (approval ${before.approval_id ?? ""})`);
    const oldApproval = cards(t.id).find((x) => x.id === before.id).approval;

    daemon = await restart(daemon, spawnDaemon);
    await waitFor("node online again", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    check(true, "daemon restarted and reconnected");

    const note = await waitFor("the dev gets the restart note", async () =>
      sql(`SELECT content FROM at_messages WHERE conversation_id = '${t.id}' AND (meta->>'resume_cut')::boolean LIMIT 1`), { timeoutMs: 120_000 });
    check(note.includes("守护进程重启") && note.includes("restarted.txt"), `resume note names the lost request: ${note.split("\n").slice(-1)[0].slice(0, 120)}…`);
    const again = await waitFor("the re-issued request is attached to the same card", async () => {
      const card = cards(t.id).find((x) => x.id === before.id);
      return card?.reattached && !card.detached && card.approval !== oldApproval && card;
    }, { timeoutMs: 600_000, everyMs: 2000 });
    check(again.status === "open", `card ${again.id} reappeared on the new request (${oldApproval} → ${again.approval})`);
    const shown = await waitingCard(c, st.projectId);
    check(shown?.id === before.id, "the user's waiting list shows the same card");

    await c.post(`/api/conversations/${shown.conversation_id}/questions/${shown.id}/answer`, { answer: "allow" });
    await waitFor("restarted.txt written", async () => existsSync(target), { timeoutMs: 300_000 });
    check(readFileSync(target, "utf8").includes("written after the restart"), "the dev wrote the file after the answer");
    const done = await waitFor("the dev completes its task", async () =>
      ((await c.get(`/api/threads/${t.id}/tasks`)) ?? []).find((x) => x.claimed_by?.startsWith("dev") && ["review", "merging", "done"].includes(x.status)),
    { timeoutMs: 600_000, everyMs: 3000 });
    check(true, `the dev completed ${done.id} (${done.status})`);
    const all = cards(t.id);
    check(all.length === 1 && all[0].status === "answered", `one card in the thread, never cancelled: ${JSON.stringify(all.map((x) => `${x.id}=${x.status}`))}`);
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
