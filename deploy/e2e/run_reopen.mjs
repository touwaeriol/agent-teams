#!/usr/bin/env node
// Real-Claude run for reopening a finished thread (deploy/e2e/up.sh first):
//   1. a read-only thread whose brief carries a code word ends with the lead's finish (done)
//   2. the user asks the lead about the earlier conversation: the message reopens the thread
//      (running, old summary kept in a note, coordinator told), the lead resumes its old SDK
//      session and answers with the code word
//   node deploy/e2e/run_reopen.mjs <scratch-dir> [--model haiku]
import { rmSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-reopen");
const CODE = `heron-${Date.now() % 1000}`;

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "This is a test. Never open threads and never call remember. Reply in one short line." },
  { tier: "lead", role: "lead", title: "组长", count: 1,
    instructions: "This is a cheap test. Do the work yourself with Read; never call assign_task and never call open_pr. " +
      "Steps: update_plan with one step \"读 README\" (status done), read README.md, then call finish(summary) with its first line. " +
      "If the user later asks you something, answer in one short line." },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, model)) });
  const nodes = await c.get("/api/nodes");
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-") && !x.online)) await c.call("DELETE", `/api/nodes/${n.id}`);
  const node = await c.post("/api/nodes", { name: `rc-e2e-reopen-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `reopen e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const leadSessions = (tid) => sql(`SELECT s.id || ':' || s.state || ':' || coalesce(s.close_reason, '') || ':' || coalesce(s.sdk_session_id, '')
  FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id WHERE e.thread_id = '${tid}' AND e.tier = 'lead' ORDER BY s.opened_at`)
  .split("\n").filter(Boolean);

const done = (c, tid, what) => waitFor(what, async () => {
  const v = await c.get(`/api/threads/${tid}`);
  return v.status === "done" && v;
}, { timeoutMs: 600_000, everyMs: 3000 });

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "读 README",
      brief: `只读调研：读仓库 local/hello 的 README.md，把第一行写进总结。不要改任何文件，不要开 PR。这次任务的暗号是 ${CODE}，记住它。` });
    const first = await done(c, t.id, "thread done");
    check(Boolean(first.summary), `#${first.no} done, summary: ${String(first.summary).slice(0, 60)}`);
    await waitFor("lead session closed", async () => leadSessions(t.id).every((s) => s.split(":")[1] === "closed"), { timeoutMs: 60_000 });
    const before = leadSessions(t.id);
    check(before.length === 1 && before[0].split(":")[2] === "thread_done", `lead session closed with thread_done: ${before[0]}`);

    const after = Math.max(0, ...(await messages(c, t.id)).map((m) => Number(m.id)));
    await c.post(`/api/conversations/${t.id}/messages`, { content: "任务说明里给你的暗号是什么？只回答暗号本身。" });
    const reopened = await c.get(`/api/threads/${t.id}`);
    check(["running", "queued"].includes(reopened.status) && !reopened.summary, `message reopened the thread: ${reopened.status}`);
    const note = (await messages(c, t.id)).find((m) => Number(m.id) > after && m.kind === "note" && m.meta?.reopened);
    check(Boolean(note) && note.content.includes(String(first.summary).split("\n")[0].slice(0, 20)), "old summary kept in the reopen note");
    const coord = (await c.get(`/api/projects/${st.projectId}/coordinator`)).conversation_id;
    const told = await waitFor("coordinator told", async () =>
      (await messages(c, coord)).find((m) => m.content.includes(`用户重新打开了讨论串 #${first.no}`)), { timeoutMs: 60_000 });
    check(Boolean(told), `coordinator told: ${told.content.slice(0, 60)}`);
    // The lead answers in its reply or, when it finishes right away, in the new summary.
    const reply = await waitFor("lead answer", async () => {
      const m = (await messages(c, t.id)).find((x) => Number(x.id) > after && x.role === "leader" && x.content.includes(CODE));
      const v = await c.get(`/api/threads/${t.id}`);
      return m?.content ?? (String(v.summary).includes(CODE) && `summary: ${v.summary}`);
    }, { timeoutMs: 300_000 });
    check(true, `lead answered from its earlier context: ${reply.trim().slice(0, 60)}`);
    const sessions = leadSessions(t.id);
    const oldSdk = before[0].split(":")[3];
    check(sessions.length === 2 && sessions[1].split(":")[3] === oldSdk, `lead resumed its old SDK session ${oldSdk}`);
    const resumedNote = (await messages(c, t.id)).some((m) => Number(m.id) > after && m.kind === "resumed");
    check(!resumedNote, "no recap fallback was needed");
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
