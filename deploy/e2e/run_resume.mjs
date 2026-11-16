#!/usr/bin/env node
// Real-Claude run for session resume and question cards (deploy/e2e/up.sh first). The
// coordinator is told a code word, then every way its session can be interrupted is tried and
// after each one it is asked for the word, which it must still know:
//   1. idle park (park_after_s) and unpark by the next message
//   2. daemon SIGTERM (parks the sessions) and start again
//   3. daemon SIGKILL (nothing parked) and start again
//   4. control plane restart
//   5. transcript deleted on the node (resume fails): a fresh session seeded with the recap,
//      and a "会话已恢复" note in the chat
// Then the question cards: Claude's own AskUserQuestion (routed to the platform card, blocking)
// and a structured ask_user, both answered through the API with per-question choices.
//   node deploy/e2e/run_resume.mjs <scratch-dir> [--model haiku] [--only park,sigterm,…]
import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, rmSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, repoRoot, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const only = arg("--only", "park,sigterm,sigkill,console,transcript,askq,structured").split(",");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-resume");
const WORD = `heron-${Math.floor(Math.random() * 9000 + 1000)}`;

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "This is a test of your memory. Never open threads and never call remember. Reply in one short line. When asked for the code word, reply with exactly the code word and nothing else. When the user asks you to use a tool, use exactly that tool as told." },
  { tier: "lead", role: "lead", title: "组长", count: 1, instructions: "Reply in one line." },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody(r, i, model)) });
  const nodes = await c.get("/api/nodes");
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-") && !x.online)) await c.call("DELETE", `/api/nodes/${n.id}`); // node quota
  const node = await c.post("/api/nodes", { name: `rc-e2e-resume-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `resume e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}, code word ${WORD}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const lastId = async (c, cid) => Math.max(0, ...(await messages(c, cid)).map((m) => Number(m.id)));

/** say posts a message and waits for the coordinator's next reply. */
async function say(c, cid, text, timeoutMs = 300_000) {
  const after = await lastId(c, cid);
  await c.post(`/api/conversations/${cid}/messages`, { content: text });
  return waitFor(`reply to "${text.slice(0, 40)}"`, async () =>
    (await messages(c, cid)).find((m) => Number(m.id) > after && m.role === "leader"), { timeoutMs, everyMs: 2000 });
}

async function askWord(c, cid, after) {
  const reply = await say(c, cid, "What was the code word? Reply with just the word.");
  check(reply.content.includes(WORD), `after ${after}: the coordinator still knows the code word (${reply.content.trim().slice(0, 60)})`);
}

const session = (projectId) => {
  const row = sql(`SELECT s.id || '|' || s.state || '|' || coalesce(s.sdk_session_id, '') || '|' || s.open_gen FROM at_agent_sessions s
    JOIN at_employees e ON e.id = s.employee_id WHERE e.project_id = '${projectId}' AND e.tier = 'coordinator' AND s.state <> 'closed'
    ORDER BY s.opened_at DESC LIMIT 1`);
  const [id, state, sdk, gen] = (row || "|||").split("|");
  return { id, state, sdk, gen: Number(gen) };
};

function transcriptOf(sdk) {
  const projects = join(process.env.CLAUDE_CONFIG_DIR ?? join(homedir(), ".claude"), "projects");
  for (const d of readdirSync(projects)) {
    const p = join(projects, d, `${sdk}.jsonl`);
    if (existsSync(p)) return p;
  }
  return null;
}

async function stopDaemon(daemon, signal) {
  const exited = new Promise((r) => daemon.once("exit", r));
  daemon.kill(signal);
  await exited;
}

function compose(...args) {
  execFileSync(join(repoRoot, "deploy/compose.sh"), ["-f", join(repoRoot, "deploy/docker-compose.e2e.yml"), ...args],
    { env: { ...process.env, ENV_FILE: join(repoRoot, "deploy/.env.e2e") }, stdio: "ignore" });
}

async function consoleUp() {
  await waitFor("control plane healthy", async () => (await fetch(`${base}/healthz`).catch(() => null))?.ok, { timeoutMs: 120_000 });
}

/** answerCard waits for the coordinator's open question card and answers it with per-question choices. */
async function answerCard(c, cid, pick) {
  const card = await waitFor("question card", async () =>
    ((await c.get(`/api/conversations/${cid}/questions?status=open`)) ?? []).find((q) => q.kind === "question"), { timeoutMs: 300_000 });
  const items = card.detail?.questions ?? [];
  check(items.length > 0 && items.every((q) => q.options.length >= 2), `card ${card.id}: ${items.length} structured question(s), via ${card.detail?.via ?? "ask_user"}`);
  const answers = items.map((q) => ({ selected: [pick(q)] }));
  const answered = await c.post(`/api/conversations/${cid}/questions/${card.id}/answer`, { answers });
  check(answered.status === "answered" && answered.detail.answers?.length === items.length, `answered through the API: ${answered.answer}`);
  return { card, answers: answered.detail.answers };
}

const steps = {
  async park(ctx) {
    await ctx.c.patch(`/api/nodes/${ctx.st.nodeId}`, { park_after_s: 5 });
    await waitFor("coordinator session parked", async () => session(ctx.st.projectId).state === "parked", { timeoutMs: 180_000, everyMs: 3000 });
    check(true, "the idle coordinator session was parked");
    await ctx.c.patch(`/api/nodes/${ctx.st.nodeId}`, { park_after_s: 3600 });
    await askWord(ctx.c, ctx.cid, "idle park → unpark");
  },
  async sigterm(ctx) {
    await stopDaemon(ctx.daemon, "SIGTERM");
    ctx.daemon = ctx.spawn();
    await waitFor("node online again", () => nodeOnline(ctx.c, ctx.st.nodeId), { timeoutMs: 60_000 });
    await askWord(ctx.c, ctx.cid, "daemon SIGTERM + start");
  },
  async sigkill(ctx) {
    await stopDaemon(ctx.daemon, "SIGKILL");
    ctx.daemon = ctx.spawn();
    await waitFor("node online again", () => nodeOnline(ctx.c, ctx.st.nodeId), { timeoutMs: 60_000 });
    await askWord(ctx.c, ctx.cid, "daemon SIGKILL + start");
  },
  async console(ctx) {
    compose("restart", "control-plane");
    await consoleUp();
    await waitFor("node online after the console restart", () => nodeOnline(ctx.c, ctx.st.nodeId), { timeoutMs: 120_000 });
    await askWord(ctx.c, ctx.cid, "control plane restart");
  },
  async transcript(ctx) {
    const before = session(ctx.st.projectId);
    await stopDaemon(ctx.daemon, "SIGTERM");
    const path = transcriptOf(before.sdk);
    check(Boolean(path), `transcript of ${before.sdk} found on the node`);
    rmSync(path);
    ctx.daemon = ctx.spawn();
    await waitFor("node online again", () => nodeOnline(ctx.c, ctx.st.nodeId), { timeoutMs: 60_000 });
    await askWord(ctx.c, ctx.cid, "transcript deleted (resume failed → recap)");
    const after = session(ctx.st.projectId);
    check(after.id !== before.id, `a fresh session replaced ${before.id}: ${after.id}`);
    const note = (await messages(ctx.c, ctx.cid)).find((m) => m.kind === "resumed");
    check(note?.content === "会话已恢复", "the chat shows the 会话已恢复 note");
  },
  async askq(ctx) {
    const after = await lastId(ctx.c, ctx.cid);
    await ctx.c.post(`/api/conversations/${ctx.cid}/messages`, {
      content: "Use your AskUserQuestion tool (not ask_user) to ask me one question: which color I prefer, header \"Color\", options Red and Blue. After I answer, reply with exactly CHOSEN=<the color I picked>.",
    });
    const { card } = await answerCard(ctx.c, ctx.cid, (q) => q.options.find((o) => /blue/i.test(o.label))?.label ?? q.options[1].label);
    check(card.detail.via === "AskUserQuestion" && card.blocking, "Claude's AskUserQuestion became a blocking platform card");
    const reply = await waitFor("CHOSEN reply", async () =>
      (await messages(ctx.c, ctx.cid)).find((m) => Number(m.id) > after && m.role === "leader" && /CHOSEN=\s*(red|blue)/i.test(m.content)), { timeoutMs: 300_000 });
    check(/CHOSEN=\s*blue/i.test(reply.content), `the answer came back as the tool result: ${reply.content.trim().slice(0, 60)}`);
  },
  async structured(ctx) {
    const after = await lastId(ctx.c, ctx.cid);
    await ctx.c.post(`/api/conversations/${ctx.cid}/messages`, {
      content: "Call the ask_user platform tool with questions (structured, not a plain question): one question \"Which database?\" with header \"DB\" and options PostgreSQL (description \"relational\", recommended) and SQLite (description \"file based\"). When the answer arrives, reply with exactly DB=<the chosen option>.",
    });
    const { answers } = await answerCard(ctx.c, ctx.cid, (q) => q.options.find((o) => /sqlite/i.test(o.label))?.label ?? q.options[1].label);
    check(/sqlite/i.test(answers[0].selected[0]), "the structured choice is stored per question");
    const reply = await waitFor("DB reply", async () =>
      (await messages(ctx.c, ctx.cid)).find((m) => Number(m.id) > after && m.role === "leader" && /DB=\s*(sqlite|postgres)/i.test(m.content)), { timeoutMs: 300_000 });
    check(/DB=\s*sqlite/i.test(reply.content), `the coordinator got the structured answer: ${reply.content.trim().slice(0, 60)}`);
  },
};

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const spawn = () => startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  const ctx = { c, st, spawn, daemon: spawn(), cid: "" };
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    ctx.cid = (await c.get(`/api/projects/${st.projectId}/coordinator`)).conversation_id;
    const ok = await say(c, ctx.cid, `Remember this code word: ${WORD}. Reply only OK.`);
    check(true, `coordinator replied: ${ok.content.trim().slice(0, 40)}`);
    for (const name of only) {
      await steps[name](ctx);
      await sleep(1000);
    }
  } finally {
    ctx.daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
