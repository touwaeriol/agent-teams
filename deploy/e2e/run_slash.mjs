#!/usr/bin/env node
// Real-Claude run for the composer's slash commands, @ references and text attachments (deploy/e2e/up.sh first):
//   1. a lead whose role has the design skill: GET /commands lists "design" from the session's own
//      init; the message "/design …" runs the skill (the reply names its section 2 heading)
//   2. the coordinator gets a message with @README.md of local/hello and a notes.md attachment and
//      quotes a line of the file and the code word in the attachment
//   3. /references lists the repository's files; /context reports the session's token use
//   node deploy/e2e/run_slash.mjs <scratch-dir> [--model haiku] [--pause]
// With --pause the run waits for <scratch>/go-slash before stopping the daemon (screenshots).
import { existsSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, startDaemon } from "./harness.mjs";
import { check, readEnv, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-slash");
const WORD = `heron-${Date.now() % 1000}`;
const README_LINE = "A tiny repository for the agent-teams v2 end-to-end test.";

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1, instructions: "This is a test. Never open threads. Answer the user directly in one or two short lines." },
  { tier: "lead", role: "lead", title: "组长", count: 1, skills: ["design"],
    instructions: "This is a cheap test. Never call assign_task, open_pr, publish_file or finish. Answer each user message yourself in one or two short lines, then stop." },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, model)) });
  const nodes = await c.get("/api/nodes");
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-") && !x.online)) await c.call("DELETE", `/api/nodes/${n.id}`);
  const node = await c.post("/api/nodes", { name: `rc-e2e-slash-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `slash e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const lastId = async (c, cid) => Math.max(0, ...(await messages(c, cid)).map((m) => Number(m.id)));
const reply = (c, cid, after, what) => waitFor(what, async () =>
  (await messages(c, cid)).find((m) => Number(m.id) > after && m.role === "leader" && m.kind === "chat" && m.content.trim()), { timeoutMs: 300_000, everyMs: 3000 });

async function leadSlash(c, st) {
  const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "设计技能", brief: "只读测试：先回复一句“在”，然后等用户的消息。不要读代码、不要改文件、不要 finish。" });
  await reply(c, t.id, 0, "lead says hello");
  const cmds = await waitFor("session commands", async () => {
    const r = await c.get(`/api/conversations/${t.id}/commands`);
    return r.from === "session" && r;
  }, { timeoutMs: 60_000 });
  const design = cmds.commands.find((x) => x.name === "design");
  check(design?.source === "skill", `/commands from the session: ${cmds.commands.map((x) => x.name).join(", ")}`);
  const after = await lastId(c, t.id);
  await c.post(`/api/conversations/${t.id}/messages`, { content: "/design 这次不用真的做设计，只用一句话说出这个技能说明第 2 节的标题。" });
  const r = await reply(c, t.id, after, "lead reply to /design");
  check(/build rules|构建规则/i.test(r.content), `the lead ran the design skill: ${r.content.trim().slice(0, 120)}`);
  return t;
}

async function coordinatorRefs(c, st) {
  const coord = (await c.get(`/api/projects/${st.projectId}/coordinator`)).conversation_id;
  const files = await c.get(`/api/projects/${st.projectId}/references?q=readme`);
  check(files.items.some((x) => x.kind === "file" && x.path === "README.md" && x.repo === "local/hello"), `@ candidates: ${files.items.map((x) => x.label).join(", ")}`);
  const att = await c.post(`/api/projects/${st.projectId}/attachments`, { name: "notes.md", data: Buffer.from(`# 笔记\n\n口令：${WORD}\n`).toString("base64") });
  check(att.content_type === "text/plain", "notes.md uploaded as a text attachment");
  const after = await lastId(c, coord);
  await c.post(`/api/conversations/${coord}/messages`, {
    content: "看我引用的 @README.md：原样引用它的第三行；再告诉我附件 notes.md 里的口令。",
    attachments: [att.id], refs: [{ kind: "file", repo: "local/hello", path: "README.md" }] });
  const r = await reply(c, coord, after, "coordinator quotes the file");
  check(r.content.includes(README_LINE), `coordinator quoted the referenced file: ${r.content.trim().slice(0, 160)}`);
  check(r.content.includes(WORD), `coordinator read the attached text file: ${WORD}`);
  const sent = (await messages(c, coord)).find((m) => Number(m.id) > after && m.role === "user");
  check(sent.meta?.refs?.[0]?.label === "README.md" && !sent.content.includes(README_LINE), "the stored message keeps the words and the @ chip");
  const ctx = await c.get(`/api/conversations/${coord}/context`);
  check(ctx.session?.turns > 0 && ctx.session.total.output > 0, `/context: ${ctx.session?.turns} turns, ${ctx.session?.total.output} output tokens`);
  return coord;
}

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const t = await leadSlash(c, st);
    await coordinatorRefs(c, st);
    writeFileSync(join(dir, "slash-state.json"), JSON.stringify({ projectId: st.projectId, threadNo: t.no }));
    if (process.argv.includes("--pause")) {
      console.log(`paused; touch ${join(dir, "go-slash")} to finish`);
      while (!existsSync(join(dir, "go-slash"))) await sleep(2000);
    }
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
