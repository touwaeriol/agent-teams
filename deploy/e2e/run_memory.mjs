#!/usr/bin/env node
// Real-Claude run for project instructions and auto memory (deploy/e2e/up.sh first):
//   1. the project instructions say every answer starts with 【测】: the coordinator's reply does
//   2. the coordinator is asked to memory_write a fact: deploy.md exists and MEMORY.md links it
//   3. a new thread's lead (a fresh session) answers the fact from project memory
//   node deploy/e2e/run_memory.mjs <scratch-dir> [--model haiku]
// With --pause the run waits for <scratch>/go-memory before stopping the daemon (screenshots).
import { existsSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-memory");
const CODE = `osprey-${Date.now() % 1000}`;
const MARK = "【测】";

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1, instructions: "This is a test. Never open threads. Keep replies to one short line." },
  { tier: "lead", role: "lead", title: "组长", count: 1,
    instructions: "This is a cheap test. Do the work yourself; never call assign_task and never call open_pr. " +
      "Steps: update_plan with one step \"回答\" (status done), answer the brief, then call finish(summary) with the answer." },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, model)) });
  const nodes = await c.get("/api/nodes");
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-") && !x.online)) await c.call("DELETE", `/api/nodes/${n.id}`);
  const node = await c.post("/api/nodes", { name: `rc-e2e-memory-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `memory e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  await c.call("PATCH", `/api/projects/${project.id}`, { context: `回答都以${MARK}开头` });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const lastId = async (c, cid) => Math.max(0, ...(await messages(c, cid)).map((m) => Number(m.id)));

/** The coordinator's next chat reply after message id `after`. */
const reply = (c, cid, after, what) => waitFor(what, async () =>
  (await messages(c, cid)).find((m) => Number(m.id) > after && m.role === "leader" && m.kind === "chat" && m.content.trim()), { timeoutMs: 300_000, everyMs: 3000 });

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  writeFileSync(join(dir, "memory-state.json"), JSON.stringify({ projectId: st.projectId }));
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const coord = (await c.get(`/api/projects/${st.projectId}/coordinator`)).conversation_id;

    let after = await lastId(c, coord);
    await c.post(`/api/conversations/${coord}/messages`, { content: "你好，用一句话介绍你能做什么。" });
    const hello = await reply(c, coord, after, "coordinator reply");
    check(hello.content.trim().startsWith(MARK), `coordinator follows the project instruction: ${hello.content.trim().slice(0, 50)}`);

    after = await lastId(c, coord);
    await c.post(`/api/conversations/${coord}/messages`, {
      content: `请用 memory_write 把这条事实记进项目记忆，文件名 deploy.md，描述“发布方式和发布代号”：发布走滚动发布，发布代号是 ${CODE}。记好后回复一句。` });
    await reply(c, coord, after, "coordinator confirms");
    const file = await waitFor("deploy.md written", async () => {
      const r = await c.raw("GET", `/api/projects/${st.projectId}/memory/deploy.md`);
      return r.status === 200 && r.json;
    }, { timeoutMs: 60_000 });
    check(file.content.includes(CODE) && file.updated_by === "coordinator", `deploy.md by ${file.updated_by}: ${file.content.trim().slice(0, 60)}`);
    const index = await c.get(`/api/projects/${st.projectId}/memory/MEMORY.md`);
    check(index.content.includes("[deploy.md](deploy.md)"), `MEMORY.md links deploy.md: ${index.content.trim().split("\n").slice(-1)[0]}`);

    const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "发布代号",
      brief: "只读问答，不要读代码、不要改文件、不要开 PR：根据项目记忆回答发布代号是什么，把代号写进 finish 的总结。" });
    const done = await waitFor("thread done", async () => {
      const v = await c.get(`/api/threads/${t.id}`);
      return v.status === "done" && v;
    }, { timeoutMs: 600_000, everyMs: 3000 });
    check(String(done.summary).includes(CODE), `new lead answered from memory: ${String(done.summary).trim().slice(0, 60)}`);
    const tools = sql(`SELECT string_agg(DISTINCT c.tool, ',') FROM at_tool_calls c JOIN at_agent_sessions s ON s.id = c.session_id
      JOIN at_employees e ON e.id = s.employee_id WHERE s.thread_id = '${t.id}' AND c.tool LIKE 'memory_%'`);
    check(true, `lead memory tools used: ${tools || "(none — answered from the injected MEMORY.md)"}`);
    const summaries = await c.get(`/api/projects/${st.projectId}/memory/thread-summaries.md`);
    check(summaries.content.includes(`## #${done.no} 发布代号`), "finish summary filed under thread-summaries.md");
    const leadMsgs = (await messages(c, t.id)).filter((m) => m.role === "leader" && m.kind === "chat" && m.content.trim());
    check(true, `lead chat replies starting with ${MARK}: ${leadMsgs.filter((m) => m.content.trim().startsWith(MARK)).length}/${leadMsgs.length}`);
    if (process.argv.includes("--pause")) {
      console.log(`paused; touch ${join(dir, "go-memory")} to finish`);
      while (!existsSync(join(dir, "go-memory"))) await sleep(2000);
    }
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
