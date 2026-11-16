#!/usr/bin/env node
// Real-Claude run for read-only threads, archive and wake-on-message (deploy/v2/e2e/up.sh first):
//   1. a research thread that changes no file ends with the lead's finish alone: done, no PR,
//      summary stored, its sessions closed on the node
//   2. archiving it keeps every session closed (the node holds no live record of them)
//   3. the coordinator's session is parked (park_after_s); the next message wakes it, the
//      coordinator shows as busy meanwhile, and it answers
//   node deploy/v2/e2e/run_readonly.mjs <scratch-dir> [--model haiku]
import { readFileSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-readonly");

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "This is a test. Never open threads and never call remember. Reply in one short line." },
  { tier: "lead", role: "lead", title: "组长", count: 1,
    instructions: "This is a cheap test. Do the work yourself with Read; never call assign_task and never call open_pr. " +
      "Steps: update_plan with one step \"读 README\" (status done), read README.md, then call finish(summary) with its first line. Nothing else." },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, model)) });
  const nodes = await c.get("/api/nodes");
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-") && !x.online)) await c.call("DELETE", `/api/nodes/${n.id}`);
  const node = await c.post("/api/nodes", { name: `rc-e2e-readonly-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `readonly e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const lastId = async (c, cid) => Math.max(0, ...(await messages(c, cid)).map((m) => Number(m.id)));

const threadSessions = (tid) => sql(`SELECT s.id || ':' || s.state || ':' || coalesce(s.close_reason, '') FROM at_agent_sessions s
  JOIN at_employees e ON e.id = s.employee_id WHERE e.thread_id = '${tid}' ORDER BY s.opened_at`).split("\n").filter(Boolean);

const coordSession = (pid) => sql(`SELECT s.state FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id
  WHERE e.project_id = '${pid}' AND e.tier = 'coordinator' AND s.state <> 'closed' ORDER BY s.opened_at DESC LIMIT 1`);

/** nodeRecords maps session ids to the state the daemon persisted for them. */
function nodeRecords() {
  const out = {};
  const walk = (d) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (p.includes("/sessions/") && p.endsWith(".json")) {
        try {
          const rec = JSON.parse(readFileSync(p, "utf8"));
          if (rec.session_id) out[rec.session_id] = rec.state;
        } catch { /* partial write */ }
      }
    }
  };
  walk(join(nodeDir, "state"));
  return out;
}

async function readOnlyThread(c, st) {
  const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "读 README", brief: "只读调研：读仓库 local/hello 的 README.md，把第一行写进总结。不要改任何文件，不要开 PR。" });
  const done = await waitFor("thread done", async () => {
    const v = await c.get(`/api/threads/${t.id}`);
    return v.status === "done" && v;
  }, { timeoutMs: 600_000, everyMs: 3000 });
  check(!done.pr_number && !done.pr_url, `thread #${done.no} reached done without a PR`);
  check(Boolean(done.summary), `summary stored: ${String(done.summary).slice(0, 60)}`);
  const repos = sql(`SELECT count(*) FROM at_thread_repos WHERE thread_id = '${t.id}' AND (branch_created OR pr_number IS NOT NULL)`);
  check(repos === "0", "no thread branch and no PR in any repository");
  await waitFor("thread sessions closed", async () => threadSessions(t.id).every((s) => s.split(":")[1] === "closed"), { timeoutMs: 60_000 });
  check(true, `sessions closed after done: ${threadSessions(t.id).join(", ")}`);
  return t;
}

async function archive(c, t) {
  const v = await c.post(`/api/threads/${t.id}/archive`);
  check(v.status === "archived", "thread archived");
  const ids = threadSessions(t.id).map((s) => s.split(":")[0]);
  await waitFor("node closed the thread sessions", async () => {
    const recs = nodeRecords();
    return ids.every((id) => recs[id] === undefined || recs[id] === "closed");
  }, { timeoutMs: 60_000 });
  check(ids.length > 0, `node holds no live record of ${ids.length} thread session(s)`);
  const r = await c.raw("POST", `/api/conversations/${t.id}/messages`, { content: "继续" });
  check(r.status === 409 && JSON.stringify(r.json).includes("取消归档后再继续"), `message to the archived thread refused: ${r.json?.detail ?? r.json?.message ?? r.status}`);
}

async function wakeCoordinator(c, st) {
  const cid = (await c.get(`/api/projects/${st.projectId}/coordinator`)).conversation_id;
  await waitFor("coordinator has a session", async () => coordSession(st.projectId) !== "", { timeoutMs: 300_000 });
  await c.patch(`/api/nodes/${st.nodeId}`, { park_after_s: 5 });
  await waitFor("coordinator session parked", async () => coordSession(st.projectId) === "parked", { timeoutMs: 300_000, everyMs: 3000 });
  await c.patch(`/api/nodes/${st.nodeId}`, { park_after_s: 3600 });
  check(true, "coordinator session parked");
  const after = await lastId(c, cid);
  await c.post(`/api/conversations/${cid}/messages`, { content: "What is 2+2? Reply with just the number." });
  const busy = await waitFor("coordinator shown busy", async () => {
    const s = (await c.get(`/api/projects/${st.projectId}/coordinator`)).employee.status;
    return s && s.state !== "idle" && s;
  }, { timeoutMs: 60_000, everyMs: 300 });
  check(true, `coordinator shown as ${busy.state}${busy.substate ? ` · ${busy.substate}` : ""} while waking`);
  const reply = await waitFor("coordinator reply", async () =>
    (await messages(c, cid)).find((m) => Number(m.id) > after && m.role === "leader"), { timeoutMs: 300_000 });
  check(reply.content.includes("4"), `parked coordinator woke and answered: ${reply.content.trim().slice(0, 40)}`);
}

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const t = await readOnlyThread(c, st);
    await archive(c, t);
    await wakeCoordinator(c, st);
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
