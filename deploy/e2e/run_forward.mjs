#!/usr/bin/env node
// Real-Claude run for forwarding project-chat messages to threads (deploy/e2e/up.sh first):
//   1. a thread is open; the user writes to the coordinator about it with an image and a code
//      word; the coordinator forwards the user's message with forward_to_thread instead of
//      paraphrasing, and the lead's reply names the image's colour and the code word
//   2. a manual forward through the API to a new thread opens it with the message as its brief;
//      the new lead answers with that message's code word
// --pause waits for <scratch>/go-forward afterwards (screenshots).
//   node deploy/e2e/run_forward.mjs <scratch-dir> [--model haiku] [--pause]
import { existsSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, startDaemon } from "./harness.mjs";
import { check, readEnv, sleep, solidPNG, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-forward");
const CODE1 = `HERON${Date.now() % 1000}`;
const CODE2 = `OWL${Date.now() % 997}`;

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "This is a cheap test. When the user's message is about an existing thread (they name it like #1), call forward_to_thread " +
      "with thread_no and the message_ids taken from [message_id: N] of that message, then reply in one short line. " +
      "If the user says 只记录, reply 好 and call no tool. Never open threads yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1,
    instructions: "This is a cheap test. Never call update_plan, assign_task, open_pr or finish and never read files. " +
      "Answer each message from the user in one short line: when it has an image, name its main colour; always repeat any all-caps code word you were given." },
  { tier: "member", role: "dev", title: "开发", count: 1, instructions: "Reply in one line." },
];

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, model)) });
  const node = await c.post("/api/nodes", { name: `rc-e2e-forward-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `forward e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
const leadReply = (c, tid, after, needles, what) => waitFor(what, async () =>
  (await messages(c, tid)).find((m) => Number(m.id) > after && m.role === "leader" && needles.every((n) => n.test(m.content))), { timeoutMs: 300_000, everyMs: 3000 });

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "登录页", brief: "登录页的需求讨论。现在只需要回一句“准备好了”。" });
    await leadReply(c, t.id, 0, [/./], "lead ready");
    const coord = (await c.get(`/api/projects/${st.projectId}/coordinator`)).conversation_id;

    // 1. the coordinator forwards the user's own message (image + words) to #1.
    const png = solidPNG(64, [220, 30, 30]);
    const att = await c.post(`/api/projects/${st.projectId}/attachments`, { name: "button.png", data: png.toString("base64") });
    const words = `关于 #${t.no} 登录页：登录按钮改成截图里的颜色。口令 ${CODE1}`;
    const sent = await c.post(`/api/conversations/${coord}/messages`, { content: words, attachments: [att.id] });
    const copy = await waitFor("forwarded copy in the thread", async () =>
      (await messages(c, t.id)).find((m) => m.meta?.forwarded_from?.message_id === Number(sent.id)), { timeoutMs: 240_000, everyMs: 3000 });
    check(copy.role === "user" && copy.content === words && copy.meta.forwarded_from.by === "coordinator" && copy.meta.attachments?.[0]?.id === att.id,
      `coordinator forwarded the user's exact words and image (copy ${copy.id})`, copy);
    const note = (await messages(c, coord)).find((m) => m.meta?.forwarded_to?.no === t.no);
    check(Boolean(note), `project chat note: ${note?.content}`);
    const reply = await leadReply(c, t.id, Number(copy.id), [/红|red/i, new RegExp(CODE1)], "lead reply naming the colour and the code word");
    check(true, `lead saw the image and the words: ${reply.content.trim().slice(0, 80)}`);

    // 2. a manual forward to a new thread: the message becomes its brief.
    const later = await c.post(`/api/conversations/${coord}/messages`, { content: `只记录：新需求 暗色模式，口令 ${CODE2}` });
    await sleep(2000);
    const fv = await c.post(`/api/conversations/${coord}/forward`, { message_ids: [Number(later.id)], new_thread: { title: "暗色模式" } });
    check(fv.created && fv.count === 1 && fv.messages[0].content.includes(CODE2), `manual forward opened #${fv.thread_no} ${fv.title}`, fv);
    const again = await c.post(`/api/conversations/${coord}/forward`, { message_ids: [Number(later.id)], new_thread: { title: "暗色模式" } });
    check(again.duplicate && again.thread_no === fv.thread_no, "a double click opened nothing new");
    const brief = (await c.get(`/api/threads/${fv.thread_id}`)).brief;
    check(brief.includes(CODE2) && brief.includes("用户本人的原话"), "the new thread's brief is the forwarded message");
    const reply2 = await leadReply(c, fv.thread_id, 0, [new RegExp(CODE2)], "new lead answers with the code word");
    check(true, `new lead: ${reply2.content.trim().slice(0, 80)}`);
    writeFileSync(join(dir, "forward-state.json"), JSON.stringify({ projectId: st.projectId, threadNo: t.no, newThreadNo: fv.thread_no, coord, copy: copy.id }, null, 2));
    if (process.argv.includes("--pause")) {
      console.log(`paused: touch ${join(dir, "go-forward")}`);
      await waitFor("go-forward", async () => existsSync(join(dir, "go-forward")), { timeoutMs: 1_800_000, everyMs: 2000 });
    }
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
