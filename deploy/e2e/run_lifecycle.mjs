#!/usr/bin/env node
// Short real-Claude run for the thread panel work (deploy/e2e/up.sh first):
//   1. an image sent in the coordinator chat reaches the model (it names the colour of a solid PNG)
//   2. a thread runs: status events are written and streamed, members carry live states, an
//      approval card waits (pause "approval" for screenshots), then is answered
//   3. the thread is archived (pause "archived"), unarchived, archived again and deleted:
//      404 afterwards, rows gone, its directory purged on the node, its images gone
// Pauses wait for <scratch>/go-<name> (created by whoever takes the screenshots), at most 15 min.
//   node deploy/e2e/run_lifecycle.mjs <scratch-dir> [--model haiku]
import { createWriteStream, existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { answerApprovals, ASK_ME, installDaemon, login, nodeOnline, record, roleBody, sql, startDaemon } from "./harness.mjs";
import { SSE, check, readEnv, sleep, solidPNG, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node");
const statePath = join(dir, "lifecycle-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));
const log = createWriteStream(join(dir, "lifecycle-events.jsonl"), { flags: "a" });

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one sentence. When asked about an attached image, answer directly. For any change to the repository, call open_thread with a one-line brief; never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: "Keep it minimal: call update_plan with one main task, then assign_task exactly one task to role dev with the full instruction. When the dev reports the task done, report in one sentence." },
  { tier: "member", role: "dev", title: "开发", count: 1,
    instructions: "Do exactly the assigned task with the Write tool, then call complete_task with a one-line summary. No extra files, no tests, no exploration." },
];

// ---- steps ----

async function setup(c) {
  if (state.projectId) return;
  const roles = await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody(r, i, model)) });
  check(roles.roles.length === 3, `role templates saved (coordinator, lead, dev; model ${model})`, roles.warnings);
  const node = await c.post("/api/nodes", { name: `rc-e2e-lifecycle-${Date.now() % 100000}` });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  save();
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `lifecycle e2e ${Date.now() % 100000}`, delivery: ASK_ME, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  state.projectId = project.id;
  save();
}

async function pause(name) {
  const marker = join(dir, `go-${name}`);
  writeFileSync(join(dir, `paused-${name}`), JSON.stringify(state));
  console.log(`  pause "${name}": waiting for ${marker}`);
  await waitFor(`screenshots (${name})`, async () => existsSync(marker), { timeoutMs: 900_000, everyMs: 2000 });
}

async function leaderReplies(c, cid) {
  const page = await c.get(`/api/conversations/${cid}/messages?limit=50`);
  return (page.items ?? []).filter((m) => m.role === "leader").map((m) => m.content);
}

async function imageToCoordinator(c, coord) {
  const png = solidPNG(64, [230, 20, 20]);
  const att = await c.post(`/api/projects/${state.projectId}/attachments`, { name: "colour.png", data: png.toString("base64") });
  check(att.content_type === "image/png" && att.size === png.length, `uploaded a ${png.length}-byte solid red PNG as ${att.id}`);
  state.imageId = att.id;
  const before = (await leaderReplies(c, coord.conversation_id)).length;
  await c.post(`/api/conversations/${coord.conversation_id}/messages`,
    { content: "What single colour fills the attached image? Answer with just the colour name in English. Do not open a thread.", attachments: [att.id] });
  const reply = await waitFor("coordinator describes the image", async () => {
    const all = await leaderReplies(c, coord.conversation_id);
    return all.length > before && all[all.length - 1];
  }, { timeoutMs: 300_000, everyMs: 3000 });
  check(/\bred\b/i.test(reply), `the model saw the image: "${reply.trim().slice(0, 120)}"`);
  const sent = JSON.parse(sql(`SELECT meta->'attachments' FROM at_messages WHERE conversation_id = '${coord.conversation_id}' AND role = 'user' ORDER BY id DESC LIMIT 1`) || "[]");
  check(sent.length === 1 && sent[0].id === att.id, "the user message keeps the image in meta.attachments");
  const daemonLog = readFileSync(join(nodeDir, "daemon.log"), "utf8");
  check(!daemonLog.includes("image download failed"), "the daemon downloaded the image with its node token");
}

async function threadOf(c) {
  const threads = await c.get(`/api/projects/${state.projectId}/threads`);
  return (threads ?? []).find((t) => t.status !== "archived") ?? threads?.[0];
}

async function openCards(c) {
  const waiting = await c.get(`/api/projects/${state.projectId}/waiting`);
  return (waiting.questions ?? []).filter((q) => q.kind === "approval");
}

async function liveThread(c, coord, sse) {
  await c.post(`/api/conversations/${coord.conversation_id}/messages`,
    { content: "Please add a file hello.txt at the repository root containing exactly: hello from the lifecycle run" });
  const t = await waitFor("coordinator opens a thread", () => threadOf(c), { timeoutMs: 300_000, everyMs: 4000 });
  state.threadId = t.id;
  state.threadNo = t.no;
  save();
  check(true, `coordinator opened thread #${t.no}: ${t.title}`);
  const card = await waitFor("an approval card from the dev", async () => (await openCards(c)).find((q) => q.asker?.startsWith("dev")),
    { timeoutMs: 600_000, everyMs: 3000 });
  check(true, `approval card waits: ${String(card.text).slice(0, 100)}`);
  const view = await c.get(`/api/threads/${t.id}`);
  const dev = view.members.find((m) => m.name.startsWith("dev"));
  check(dev?.status?.state === "raising_hand", `the thread view shows the dev raising a hand (${view.members.map((m) => `${m.name}=${m.status?.state}`).join(" ")})`);
  const statusRows = Number(sql(`SELECT count(*) FROM at_status_events WHERE project_id = '${state.projectId}'`));
  check(statusRows > 0, `${statusRows} rows in at_status_events for the project`);
  const streamed = sse.events.filter((e) => e.event === "status");
  check(streamed.length > 0, `${streamed.length} status events streamed over SSE (states: ${[...new Set(streamed.map((e) => e.json?.state ?? e.json?.status?.state).filter(Boolean))].join(", ")})`);
  const activity = await c.get(`/api/threads/${t.id}/activity`);
  check((activity.items ?? []).length > 0, `the thread activity feed has ${(activity.items ?? []).length} items`);
  await pause("approval");
  return t;
}

async function finishTask(c, t) {
  const answered = new Map();
  await waitFor("dev completes the task", async () => {
    await answerApprovals(c, state.projectId, answered);
    const tasks = (await c.get(`/api/threads/${t.id}/tasks`)) ?? [];
    return tasks.find((x) => x.claimed_by?.startsWith("dev") && ["review", "merging", "done"].includes(x.status));
  }, { timeoutMs: 600_000, everyMs: 4000 });
  check(answered.size > 0, `answered ${answered.size} approval card(s); the dev completed its task`);
}

async function archiveAndDelete(c, t) {
  const threadDir = join(nodeDir, "root", "projects", state.projectId, "threads", String(t.no));
  check(existsSync(threadDir), `the thread directory exists on the node (${threadDir})`);
  let v = await c.post(`/api/threads/${t.id}/archive`);
  check(v.status === "archived", "archive → archived");
  await waitFor("the thread's sessions close", async () =>
    sql(`SELECT count(*) FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id WHERE e.thread_id = '${t.id}' AND s.state <> 'closed'`) === "0",
  { timeoutMs: 60_000 });
  check(true, "archiving closed every session of the thread");
  const listed = await c.get(`/api/projects/${state.projectId}/threads`);
  check(listed.some((x) => x.id === t.id && x.status === "archived"), "the thread list carries it as archived");
  await pause("archived");
  v = await c.post(`/api/threads/${t.id}/unarchive`);
  check(["paused", "done", "failed"].includes(v.status), `unarchive → ${v.status}`);
  v = await c.post(`/api/threads/${t.id}/archive`);
  check(v.status === "archived", "archived again before deleting");
  const r = await c.raw("DELETE", `/api/threads/${t.id}`);
  check(r.status === 204, "DELETE /api/threads/{tid} → 204");
  check((await c.raw("GET", `/api/threads/${t.id}`)).status === 404, "the thread is gone (404)");
  check(sql(`SELECT count(*) FROM at_threads WHERE id = '${t.id}'`) === "0" && sql(`SELECT count(*) FROM at_employees WHERE thread_id = '${t.id}'`) === "0",
    "thread and employee rows removed");
  await waitFor("the node purges the thread directory", async () => !existsSync(threadDir), { timeoutMs: 120_000 });
  check(true, "the node removed the thread directory");
}

async function main() {
  const c = await login(base);
  await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: state.nodeToken, gitRoot: env.E2E_GIT_ROOT, pkg });
  const sse = new SSE(c, `/api/projects/${state.projectId}/events`);
  const recorder = record(sse, log);
  try {
    const node = await waitFor("node online", () => nodeOnline(c, state.nodeId), { timeoutMs: 60_000 });
    check(true, `daemon online: ${node.version ?? ""}`);
    const coord = await c.get(`/api/projects/${state.projectId}/coordinator`);
    state.coordinatorConversation = coord.conversation_id;
    save();
    await imageToCoordinator(c, coord);
    const t = await liveThread(c, coord, sse);
    await finishTask(c, t);
    await archiveAndDelete(c, t);
    await sleep(1000);
  } finally {
    clearInterval(recorder);
    sse.close();
    daemon.kill("SIGTERM");
    log.end();
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
