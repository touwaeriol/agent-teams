#!/usr/bin/env node
// Real-Claude run for /model and custom commands (deploy/e2e/up.sh first):
//   1. the lead runs on haiku (the role's model); /model (PUT /conversations/{tid}/model) switches
//      the running lead to sonnet: session.set_mode reaches the live session (live: true, no new
//      session) and the next turn reports the sonnet model (turn_ended.model → /model, /context)
//   2. a prompt-template command of the lead's role ("/codeword falcon") is expanded by the
//      server; the lead follows the expanded instruction while the stored message keeps the words
//   node deploy/e2e/run_model.mjs <scratch-dir> [--from haiku] [--to sonnet] [--pause]
// With --pause the run waits for <scratch>/go-model before stopping the daemon (screenshots).
import { existsSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const from = arg("--from", "haiku");
const to = arg("--to", "sonnet");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-model");
const WORD = `falcon${Date.now() % 1000}`;

const LEAD_ONLY = "This is a cheap test. Never call assign_task, open_pr, publish_file or finish. Answer each user message yourself in one short line, then stop.";
const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1, instructions: "This is a test. Never open threads. Answer in one short line." },
  { tier: "lead", role: "lead", title: "组长", count: 1, instructions: LEAD_ONLY, hidden_commands: ["usage"],
    commands: [{ name: "codeword", description: "说出口令", mode: "prompt", prompt: "只回复一行：口令是 {{args}}，把口令全部写成大写字母。不要做别的。" }] },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, from)) });
  await c.put("/api/models/mine", { models: [{ runtime: "claude", model: "haiku", label: "Haiku（别名）" }, { runtime: "claude", model: "sonnet", label: "Sonnet（别名）" }] });
  const nodes = await c.get("/api/nodes");
  // Earlier nodes of this run (even one whose link has not timed out yet) must not get the lead.
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-model-") || (x.name.startsWith("rc-e2e-") && !x.online))) await c.call("DELETE", `/api/nodes/${n.id}`);
  const node = await c.post("/api/nodes", { name: `rc-e2e-model-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `model e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, ${from} → ${to}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const lastId = async (c, cid) => Math.max(0, ...(await messages(c, cid)).map((m) => Number(m.id)));
const reply = (c, cid, after, what) => waitFor(what, async () =>
  (await messages(c, cid)).find((m) => Number(m.id) > after && m.role === "leader" && m.kind === "chat" && m.content.trim()), { timeoutMs: 300_000, everyMs: 3000 });
const sessions = (tid) => sql(`SELECT count(*) FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id WHERE e.thread_id = '${tid}' AND e.tier = 'lead'`);
const turnModels = (tid) => sql(`SELECT string_agg(u.model, ',' ORDER BY u.id) FROM at_usage u JOIN at_agent_sessions s ON s.id = u.session_id JOIN at_employees e ON e.id = s.employee_id WHERE e.thread_id = '${tid}' AND e.tier = 'lead'`);

async function switchModel(c, st) {
  const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "换模型", brief: "只读测试：先回复一句“在”，然后等用户的消息。不要读代码、不要改文件、不要 finish。" });
  await reply(c, t.id, 0, "lead says hello");
  const before = await waitFor("first turn model", async () => {
    const m = await c.get(`/api/conversations/${t.id}/model`);
    return m.session && m;
  }, { timeoutMs: 60_000 });
  check(before.live && before.session.includes(from), `before: live session answered with ${before.session} (role model ${before.model})`);
  const opened = sessions(t.id);
  const res = await c.put(`/api/conversations/${t.id}/model`, { model: to, scope: "lead" });
  check(res.applied.length === 1 && res.applied[0].live && res.applied[0].model === to, `PUT /model: ${JSON.stringify(res.applied)}`);
  const after = await lastId(c, t.id);
  await c.post(`/api/conversations/${t.id}/messages`, { content: "再回复一句“好”。" });
  await reply(c, t.id, after, "lead reply after /model");
  const now = await waitFor("turn on the new model", async () => {
    const m = await c.get(`/api/conversations/${t.id}/model`);
    return m.session.includes(to) && m;
  }, { timeoutMs: 60_000 });
  check(now.model === to && now.override === to, `after: the next turn answered with ${now.session}; model ${now.model}`);
  check(sessions(t.id) === opened, `the same session switched (sessions: ${opened})`);
  check(true, `at_usage models per turn: ${turnModels(t.id)}`);
  const ctx = await c.get(`/api/conversations/${t.id}/context`);
  check(ctx.session.model.includes(to), `/context reports ${ctx.session.model}`);
  return t;
}

async function customCommand(c, t) {
  const cmds = await c.get(`/api/conversations/${t.id}/commands`);
  check(cmds.custom.some((x) => x.name === "codeword" && x.source === "role") && cmds.hidden.includes("usage"), `/commands: custom ${cmds.custom.map((x) => x.name).join(", ")}, hidden ${cmds.hidden.join(", ")}`);
  const after = await lastId(c, t.id);
  await c.post(`/api/conversations/${t.id}/messages`, { content: `/codeword ${WORD}` });
  const r = await reply(c, t.id, after, "lead reply to /codeword");
  check(r.content.includes(WORD.toUpperCase()), `the lead followed the expanded template: ${r.content.trim().slice(0, 120)}`);
  const sent = (await messages(c, t.id)).find((m) => Number(m.id) > after && m.role === "user");
  check(sent.content === `/codeword ${WORD}` && sent.meta?.command?.name === "codeword", "the stored message keeps the typed command (meta.command)");
}

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const t = await switchModel(c, st);
    await customCommand(c, t);
    writeFileSync(join(dir, "model-state.json"), JSON.stringify({ projectId: st.projectId, threadNo: t.no, threadId: t.id }));
    if (process.argv.includes("--pause")) {
      console.log(`paused; touch ${join(dir, "go-model")} to finish`);
      while (!existsSync(join(dir, "go-model"))) await sleep(2000);
    }
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
