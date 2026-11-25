#!/usr/bin/env node
// Attachments end to end (#58): the browser's multipart upload straight to the object store, the
// files passed coordinator → lead → member with the bytes checked on the node, a node's shared
// file over 20 MB through a presigned PUT and back, an old daemon (no attachments_v2) getting the
// downgraded message, an expired attachment, and timings of 200 MB.
//
// Real: control plane, PostgreSQL, the object store (RustFS when AGENT_TEAMS_S3_* point at it),
// the node daemons (this repository's node/ and an older checkout's), every HTTP and WebSocket
// hop and every download/upload. Fake: the AI. attachments_daemon.mts runs the daemon with the
// test stand-in for the Agent SDK, driven by "@@use <tool> <json>" / "@@sh <cmd>" lines in the
// messages, so the tool calls an AI would make are made on cue.
//
//   node deploy/e2e/run_attachments.mjs <scratch> [--base URL] [--sql CMD] [--old-src DIR] [--phase main,legacy,expire,big]
//     --base     the control plane (default: AGENT_TEAMS_PUBLIC_URL of the e2e env file)
//     --sql      a command that runs one SQL query given as its argument (default: psql in the compose stack)
//     --old-src  node/ of an older checkout with its node_modules (the old daemon; legacy is skipped without it)
//     --keep     leave the projects (and their objects) in place for a look afterwards
//     --phase    which parts to run (default main,legacy,big); expire needs the control plane started with
//                AGENT_TEAMS_ATTACHMENT_TTL=1m and takes about 3 minutes
import { spawn, execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { createReadStream, createWriteStream, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { open } from "node:fs/promises";
import { join } from "node:path";
import { login, sql as composeSql } from "./harness.mjs";
import { check, here, readEnv, repoRoot, sleep, solidPNG, waitFor } from "./lib.mjs";

const scratch = process.argv[2];
if (!scratch) throw new Error("usage: run_attachments.mjs <scratch> [--base URL] [--sql CMD] [--old-src DIR] [--phase …]");
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const base = arg("--base", null) ?? readEnv().AGENT_TEAMS_PUBLIC_URL;
const sqlCmd = arg("--sql", null);
const oldSrc = arg("--old-src", null);
const phases = new Set(arg("--phase", "main,legacy,big").split(","));
const MB = 1 << 20;
const timings = {};

const sql = (q) => (sqlCmd ? execFileSync(sqlCmd, [q]).toString().trim() : composeSql(q));
const sha = (buf) => createHash("sha256").update(buf).digest("hex");
async function shaFile(path) {
  const h = createHash("sha256");
  for await (const c of createReadStream(path)) h.update(c);
  return h.digest("hex");
}
const secs = (ms) => `${(ms / 1000).toFixed(1)} s`;
const rate = (bytes, ms) => `${(bytes / MB / (ms / 1000)).toFixed(1)} MB/s`;

/** A file of size bytes: head + random bytes (+ tail), written to the scratch dir. */
function makeFile(name, size, head = "", tail = "") {
  const path = join(scratch, "files", name);
  mkdirSync(join(scratch, "files"), { recursive: true });
  const fd = createWriteStream(path);
  const h = createHash("sha256");
  const put = (b) => (h.update(b), fd.write(b));
  put(Buffer.from(head));
  let left = size - Buffer.byteLength(head) - Buffer.byteLength(tail);
  while (left > 0) {
    const b = randomBytes(Math.min(left, 4 * MB));
    put(b);
    left -= b.length;
  }
  put(Buffer.from(tail));
  return new Promise((resolve) => fd.end(() => resolve({ path, name, size, sha256: h.digest("hex") })));
}

/**
 * The web app's upload (web/src/lib/upload.ts): create → part URLs → PUT every part straight to
 * the store (ETag kept) → complete. Returns the attachment and the time it took.
 */
async function upload(c, pid, f) {
  const t0 = Date.now();
  const s = await c.post(`/api/projects/${pid}/uploads`, { kind: "attachment", name: f.name, size: f.size });
  const numbers = Array.from({ length: s.parts }, (_, i) => i + 1);
  const { urls } = await c.post(`/api/projects/${pid}/uploads/${s.upload_id}/parts`, { numbers });
  check(urls.every((u) => !u.url.startsWith(base)), `${f.name}: ${s.parts} part URL(s) point at the object store, not the console`);
  const fh = await open(f.path);
  const parts = [];
  try {
    let next = 0;
    const worker = async () => {
      while (next < urls.length) {
        const u = urls[next++];
        const start = (u.number - 1) * s.part_size;
        const len = Math.min(s.part_size, f.size - start);
        const buf = Buffer.alloc(len);
        await fh.read(buf, 0, len, start);
        const res = await fetch(u.url, { method: "PUT", body: buf });
        if (!res.ok) throw new Error(`PUT part ${u.number}: ${res.status} ${await res.text()}`);
        parts.push({ number: u.number, etag: res.headers.get("etag") });
      }
    };
    await Promise.all([worker(), worker(), worker()]);
  } finally {
    await fh.close();
  }
  parts.sort((a, b) => a.number - b.number);
  const done = await c.post(`/api/projects/${pid}/uploads/${s.upload_id}/complete`, { parts });
  const ms = Date.now() - t0;
  check(done.kind === "attachment" && done.attachment.size === f.size, `${f.name} uploaded (${(f.size / MB).toFixed(1)} MB, ${secs(ms)}, ${rate(f.size, ms)})`);
  return { ...done.attachment, ms };
}

/** Starts attachments_daemon.mts for a node under dir; src = the daemon's node/ tree. */
function startDaemon(dir, token, src) {
  for (const d of ["config", "data", "state", "run", "root"]) mkdirSync(join(dir, d), { recursive: true });
  const clean = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith("AGENT_TEAMS_") && !/_proxy$/i.test(k)));
  const out = createWriteStream(join(dir, "daemon.log"), { flags: "a" });
  const child = spawn(join(src, "node_modules/.bin/tsx"), [join(here, "attachments_daemon.mts"), "run", "--console", base, "--token", token,
    "--root", join(dir, "root"), "--state", join(dir, "state"), "--max-sessions", "8", "--log-level", "debug"], {
    env: { ...clean, XDG_CONFIG_HOME: join(dir, "config"), XDG_DATA_HOME: join(dir, "data"), XDG_STATE_HOME: join(dir, "state"),
      XDG_RUNTIME_DIR: join(dir, "run"), AGENT_TEAMS_ENV_FILE: join(dir, "none.env"), E2E_NODE_SRC: src, E2E_PROMPT_LOG: join(dir, "prompts.jsonl") },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.pipe(out);
  child.stderr.pipe(out);
  return child;
}
/** SIGTERM, then SIGKILL after 10 s (tsx relays the signal to the daemon process). */
function stopDaemon(child) {
  return new Promise((r) => {
    if (child.exitCode !== null || child.signalCode !== null) return r();
    const kill = setTimeout(() => child.kill("SIGKILL"), 10_000);
    child.once("exit", () => (clearTimeout(kill), r()));
    child.kill("SIGTERM");
  });
}

const prompts = (dir) => (existsSync(join(dir, "prompts.jsonl")) ? readFileSync(join(dir, "prompts.jsonl"), "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : []);
const messages = async (c, cid) => (await c.get(`/api/conversations/${cid}/messages`)).items ?? [];
/** The newest reply (leader/employee role) after message id `after` whose text matches re. */
const reply = (c, cid, after, re, what, timeoutMs = 300_000) => waitFor(what, async () =>
  (await messages(c, cid)).find((m) => Number(m.id) > Number(after) && m.role !== "user" && m.role !== "system" && re.test(m.content)), { timeoutMs, everyMs: 1500 });

/** Every file named <id>-* in an .attachments/ folder under root. */
function findAttachment(root, id) {
  const hits = [];
  const walk = (d, depth) => {
    if (depth > 8) return;
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) walk(p, depth + 1);
      else if (e.isFile() && d.endsWith(".attachments") && e.name.startsWith(`${id}-`)) hits.push(p);
    }
  };
  walk(root, 0);
  return hits;
}

async function setup(c, name) {
  // The default roles (coordinator, lead, dev …), with no permission prompts.
  const roles = (await c.get("/api/roles")).roles.map(({ warnings, ...r }) => ({ ...r, runtime: "claude", permission_mode: "bypass" }));
  check(roles.some((r) => r.role === "dev" && r.tier === "member"), "a dev member role exists");
  await c.put("/api/roles", { roles });
  const project = await c.post("/api/projects", { name });
  const coord = (await c.get(`/api/projects/${project.id}/coordinator`)).conversation_id;
  return { pid: project.id, coord };
}

const created = [];
async function newNode(c, name) {
  const n = await c.post("/api/nodes", { name: `${name}-${Date.now() % 100000}` });
  created.push(n.node.id);
  return { id: n.node.id, token: n.token };
}
const online = (c, id) => waitFor(`node ${id} online`, async () => ((await c.get("/api/nodes")).nodes ?? (await c.get("/api/nodes"))).find((x) => x.id === id)?.online, { timeoutMs: 60_000 });
const offline = (c, id) => waitFor(`node ${id} offline`, async () => !((await c.get("/api/nodes")).nodes ?? (await c.get("/api/nodes"))).find((x) => x.id === id)?.online, { timeoutMs: 120_000 });

// ── main: web upload → coordinator → forward_to_thread → lead → assign_task → member ───────────
async function mainPhase(c, ctx) {
  const dir = join(scratch, "node-new");
  const node = await newNode(c, "e2e-att-new");
  const daemon = startDaemon(dir, node.token, join(repoRoot, "node"));
  ctx.stop.push(() => stopDaemon(daemon));
  await online(c, node.id);
  const features = sql(`SELECT last_hello->'features' FROM at_nodes WHERE id = '${node.id}'`);
  check(features.includes("attachments_v2"), "the new daemon reports attachments_v2");
  const { pid, coord } = await setup(c, `attachments e2e ${Date.now() % 100000}`);
  ctx.projects.push(pid);
  Object.assign(ctx, { pid, coord, newDir: dir, newNode: node, newDaemon: daemon });

  const t = await c.post(`/api/projects/${pid}/threads`, { title: "附件联调", brief: "等用户的文件。" });
  await reply(c, t.id, 0, /./, "lead's first reply");

  const pdf = await makeFile("需求说明-30MB.pdf", 30 * MB, "%PDF-1.7\n%e2e\n", "\n%%EOF\n");
  const zip = await makeFile("日志与数据.zip", 42 * MB, "PK\x03\x04");
  const aPdf = await upload(c, pid, pdf);
  const aZip = await upload(c, pid, zip);
  timings.upload30 = aPdf.ms;
  check(aPdf.content_type === "application/pdf" && aZip.content_type === "application/zip", `types sniffed: ${aPdf.content_type}, ${aZip.content_type}`);

  // 1. to the coordinator, which forwards the user's message to thread #1.
  const words = `@@use forward_to_thread {"thread_no":${t.no}}\n需求和日志都在附件里，请转给 #${t.no}。`;
  const sent = await c.post(`/api/conversations/${coord}/messages`, { content: words, attachments: [aPdf.id, aZip.id] });
  const copy = await waitFor("forwarded copy in the thread", async () =>
    (await messages(c, t.id)).find((m) => m.meta?.forwarded_from?.message_id === Number(sent.id)), { timeoutMs: 300_000, everyMs: 2000 });
  check(copy.meta.attachments?.map((a) => a.id).join() === [aPdf.id, aZip.id].join(), "forward_to_thread carried both attachments into the thread");
  const coordGot = await waitFor("coordinator's node got the files", async () => {
    const files = [aPdf, aZip].map((a) => findAttachment(join(dir, "root"), a.id));
    return files.every((f) => f.length) && files;
  }, { timeoutMs: 600_000, everyMs: 2000 });
  check(true, `the daemon downloaded both for the coordinator and the lead (${coordGot.flat().length} copies)`);

  // 2. the lead plans and assigns a task with both attachments (the AI's calls, made on cue).
  const p0 = Number((await messages(c, t.id)).at(-1)?.id ?? 0);
  await c.post(`/api/conversations/${t.id}/messages`, { content: `@@use update_plan {"steps":[{"title":"处理日志","status":"doing"}]}\n先定计划。` });
  const planned = await reply(c, t.id, p0, /tool (ok|error)/, "lead's update_plan answered");
  check(/tool ok/.test(planned.content), `update_plan: ${planned.content.slice(0, 100)}`);
  const before = Number((await messages(c, t.id)).at(-1)?.id ?? 0);
  const t0 = Date.now();
  await c.post(`/api/conversations/${t.id}/messages`, {
    content: `@@use assign_task {"assignee":"dev","instruction":"按需求说明处理日志","main":"处理日志","attachments":["${aPdf.id}","${aZip.id}"]}\n派给开发。` });
  const assigned = await reply(c, t.id, before, /tool (ok|error)/, "lead's assign_task answered");
  check(/tool ok/.test(assigned.content), `assign_task: ${assigned.content.slice(0, 160)}`);
  const brief = await waitFor("the member's brief with the files", async () =>
    prompts(dir).find((p) => p.text.includes("[Task t1") && p.text.includes(aPdf.id) && p.text.includes(aZip.id)), { timeoutMs: 600_000, everyMs: 2000 });
  timings.memberDelivery = Date.now() - t0;
  check(/\[附件\]/.test(brief.text) && brief.text.includes(".attachments/"), "the member's brief lists the files with their local paths");
  for (const [a, f] of [[aPdf, pdf], [aZip, zip]]) {
    const local = findAttachment(brief.cwd, a.id);
    check(local.length === 1, `member has ${a.name} under .attachments/`, local);
    const got = await shaFile(local[0]);
    check(got === f.sha256 && statSync(local[0]).size === f.size, `member's copy of ${a.name}: sha256 ${got.slice(0, 12)}… matches the original`);
  }
  check(!brief.blocks.some((b) => b.type === "document"), "a 30 MB PDF is given by path, not as a document block (over 15 MB)");
  const task = sql(`SELECT attachments::text FROM at_tasks WHERE thread_id = '${t.id}' ORDER BY created_at DESC LIMIT 1`);
  check(task.includes(aPdf.id) && task.includes(aZip.id), "the task keeps its attachments (get_task, rework)");
  console.log(`     assign_task → member has both files on disk: ${secs(timings.memberDelivery)}`);

  // 3. a shared file over 20 MB written by the node (presigned PUT) and read back by another session.
  const big = 25 * MB;
  const b0 = Number((await messages(c, t.id)).at(-1)?.id ?? 0);
  await c.post(`/api/conversations/${t.id}/messages`, {
    content: `@@sh head -c ${big} /dev/urandom > big25.bin && sha256sum big25.bin\n@@use write_shared_file {"file":"big25.bin","path":"e2e/big25.bin"}\n写共享文件。` });
  const wrote = await reply(c, t.id, b0, /tool (ok|error)/, "write_shared_file answered", 600_000);
  check(/tool ok/.test(wrote.content), `write_shared_file of 25 MB: ${wrote.content.slice(0, 120)}`);
  const leadSh = prompts(dir).find((p) => p.sh?.some((s) => s.cmd.includes("big25.bin")));
  const want = leadSh.sh[0].out.split(" ")[0];
  const row = sql(`SELECT sha256, storage_key IS NOT NULL, data IS NULL, size FROM at_shared_files WHERE project_id = '${pid}' AND path = 'e2e/big25.bin'`).split("\t");
  check(row[1] === "true" && row[2] === "true" && Number(row[3]) === big, "stored in the object store (storage_key set, no bytes in the database)", row);
  check(row[0] === want || row[0] === "" || row[0] === "<nil>", `sha256 recorded (or pending): ${row[0].slice(0, 12)}`);
  await waitFor("the shared file's sha256", async () => sql(`SELECT sha256 FROM at_shared_files WHERE project_id = '${pid}' AND path = 'e2e/big25.bin'`) === want, { timeoutMs: 180_000, everyMs: 3000 });
  const r0 = Number((await messages(c, coord)).at(-1)?.id ?? 0);
  await c.post(`/api/conversations/${coord}/messages`, { content: `@@use read_shared_file {"path":"e2e/big25.bin"}\n读共享文件。` });
  const read = await reply(c, coord, r0, /tool (ok|error)/, "read_shared_file answered", 600_000);
  check(/tool ok/.test(read.content), `read_shared_file: ${read.content.slice(0, 100)}`);
  const coordCwd = prompts(dir).find((p) => p.text.includes("read_shared_file"))?.cwd;
  const local = join(coordCwd, ".shared/e2e/big25.bin");
  check(existsSync(local) && (await shaFile(local)) === want, "the coordinator's .shared/e2e/big25.bin has the same sha256");
  const dl = sql(`SELECT result::text FROM at_tool_calls WHERE tool = 'read_shared_file' ORDER BY created_at DESC LIMIT 1`);
  check(/"shared_download": ?\{"files": ?\[\{"url": ?"https?:/.test(dl) && !/"chunks": ?[1-9]/.test(dl), "read_shared_file used a presigned download (no chunk frames)");
  return { t, aPdf, aZip };
}

// ── big: 200 MB through the node (write_shared_file → finalize copy + hash) and back ─────────
async function bigPhase(c, ctx) {
  const { pid, coord, newDir: dir } = ctx;
  const t = (await c.get(`/api/projects/${pid}/threads`)).items?.[0] ?? (await c.get(`/api/projects/${pid}/threads`))[0];
  const size = 200 * MB;
  const b0 = Number((await messages(c, t.id)).at(-1)?.id ?? 0);
  const t0 = Date.now();
  await c.post(`/api/conversations/${t.id}/messages`, {
    content: `@@sh head -c ${size} /dev/urandom > big200.bin && sha256sum big200.bin\n@@use write_shared_file {"file":"big200.bin","path":"e2e/big200.bin"}\n写 200 MB。` });
  const wrote = await reply(c, t.id, b0, /tool (ok|error)/, "200 MB write_shared_file answered", 1_800_000);
  const total = Date.now() - t0;
  check(/tool ok/.test(wrote.content), `write_shared_file 200 MB: ${wrote.content.slice(0, 100)}`);
  const want = prompts(dir).find((p) => p.sh?.some((s) => s.cmd.includes("big200.bin")))?.sh[0].out.split(" ")[0];
  // The tool call opens after the daemon's PUT: created_at → finished_at is the console's finalize
  // (size check, copy to a key no URL was signed for, sha256 streamed and compared).
  const [fin] = sql(`SELECT (extract(epoch from finished_at - created_at) * 1000)::float8 FROM at_tool_calls WHERE tool = 'write_shared_file' ORDER BY created_at DESC LIMIT 1`).split("\t");
  timings.write200 = total;
  timings.finalize200 = Number(fin);
  console.log(`     200 MB: message → tool ok ${secs(total)} (dd + sha256 + PUT + finalize); finalize alone ${secs(Number(fin))}`);
  await waitFor("200 MB sha256", async () => sql(`SELECT sha256 FROM at_shared_files WHERE project_id = '${pid}' AND path = 'e2e/big200.bin'`) === want, { timeoutMs: 600_000, everyMs: 3000 });
  const r0 = Number((await messages(c, coord)).at(-1)?.id ?? 0);
  const d0 = Date.now();
  await c.post(`/api/conversations/${coord}/messages`, { content: `@@use read_shared_file {"path":"e2e/big200.bin"}\n读 200 MB。` });
  const read = await reply(c, coord, r0, /tool (ok|error)/, "200 MB read_shared_file answered", 1_800_000);
  timings.download200 = Date.now() - d0;
  check(/tool ok/.test(read.content), `read_shared_file 200 MB in ${secs(timings.download200)} (${rate(size, timings.download200)})`);
  const cwd = prompts(dir).find((p) => p.text.includes("big200.bin") && p.text.includes("read_shared_file"))?.cwd;
  check((await shaFile(join(cwd, ".shared/e2e/big200.bin"))) === want, "the 200 MB copy on the node matches");
}

// ── legacy: an old daemon (no attachments_v2) gets the downgraded message ──────────────────────
async function legacyPhase(c, ctx) {
  if (!oldSrc) return console.log("skip legacy: no --old-src");
  for (const stop of ctx.stop.splice(0)) await stop(); // only the old node is online now
  if (ctx.newNode) await offline(c, ctx.newNode.id);
  const dir = join(scratch, "node-old");
  const node = await newNode(c, "e2e-att-old");
  const daemon = startDaemon(dir, node.token, oldSrc);
  ctx.stop.push(() => stopDaemon(daemon));
  await online(c, node.id);
  check(!sql(`SELECT last_hello->'features' FROM at_nodes WHERE id = '${node.id}'`).includes("attachments_v2"), "the old daemon has no attachments_v2");
  const { pid, coord } = await setup(c, `attachments legacy ${Date.now() % 100000}`);
  ctx.projects.push(pid);
  const png = solidPNG(64, [220, 30, 30]);
  writeFileSync(join(scratch, "files", "red.png"), png);
  const small = await makeFile("小文档.pdf", 1 * MB, "%PDF-1.4\n", "\n%%EOF\n");
  const bigPdf = await makeFile("大文档.pdf", 30 * MB, "%PDF-1.4\n", "\n%%EOF\n");
  const zip = await makeFile("包.zip", 6 * MB, "PK\x03\x04");
  writeFileSync(join(scratch, "files", "notes.md"), "# 记录\n\n第三行：口令 LEGACY42\n");
  const a = {
    png: await upload(c, pid, { path: join(scratch, "files", "red.png"), name: "red.png", size: png.length }),
    small: await upload(c, pid, small), big: await upload(c, pid, bigPdf), zip: await upload(c, pid, zip),
    md: await upload(c, pid, { path: join(scratch, "files", "notes.md"), name: "notes.md", size: statSync(join(scratch, "files", "notes.md")).size }),
  };
  const ids = Object.values(a).map((x) => x.id);
  // The session.open first message, then a session.send: both pass the old daemon's schema check.
  for (const [i, what] of [[1, "first message (session.open)"], [2, "next message (session.send)"]]) {
    const before = Number((await messages(c, coord)).at(-1)?.id ?? 0);
    await c.post(`/api/conversations/${coord}/messages`, { content: `第 ${i} 条：看附件`, attachments: ids });
    await reply(c, coord, before, /echo:/, `old daemon answered the ${what}`, 300_000);
    const p = prompts(dir).filter((x) => x.text.includes(`第 ${i} 条`)).at(-1);
    const types = p.blocks.map((b) => `${b.type}:${b.media_type}`).sort();
    check(types.join() === "document:application/pdf,image:image/png", `${what}: the PNG and the 1 MB PDF as content blocks`, p.blocks);
    for (const x of [a.big, a.zip]) check(p.text.includes(x.id) && p.text.includes("版本过旧"), `${what}: ${x.name} named by id with the reason`);
    check(p.text.includes("LEGACY42"), `${what}: the text file's content is inline`);
  }
  const rejected = readFileSync(join(dir, "daemon.log"), "utf8").match(/schema|invalid message|ajv/gi) ?? [];
  check(rejected.length === 0, "no schema rejection in the old daemon's log", rejected.slice(0, 3));
}

// ── expire: retention runs out (control plane with AGENT_TEAMS_ATTACHMENT_TTL=1m) ─────────────
async function expirePhase(c, ctx) {
  for (const stop of ctx.stop.splice(0)) await stop();
  const dir = join(scratch, "node-expire");
  const node = await newNode(c, "e2e-att-exp");
  let daemon = startDaemon(dir, node.token, join(repoRoot, "node"));
  ctx.stop.push(() => stopDaemon(daemon)); // whichever daemon runs at the end
  await online(c, node.id);
  const { pid, coord } = await setup(c, `attachments expire ${Date.now() % 100000}`);
  ctx.projects.push(pid);
  await c.post(`/api/conversations/${coord}/messages`, { content: "你好" });
  await reply(c, coord, 0, /echo:/, "coordinator up");
  const f = await makeFile("快过期.zip", 2 * MB, "PK\x03\x04");
  const att = await upload(c, pid, f);
  const ttl = sql(`SELECT extract(epoch from expires_at - now())::float8 FROM at_attachments WHERE id = '${att.id}'`);
  check(Number(ttl) < 120, `retention is short in this run (${Math.round(Number(ttl))} s left; start the console with AGENT_TEAMS_ATTACHMENT_TTL=1m)`);
  // The message waits in the outbox while the node is away; the attachment expires meanwhile.
  await stopDaemon(daemon);
  await offline(c, node.id);
  const sent = await c.post(`/api/conversations/${coord}/messages`, { content: "离线时发的：看附件", attachments: [att.id] });
  check(Boolean(sent.id), "message accepted while the node is offline");
  const t0 = Date.now();
  await waitFor("attachment expired (blobgc)", async () => sql(`SELECT state FROM at_attachments WHERE id = '${att.id}'`) === "expired", { timeoutMs: 300_000, everyMs: 5000 });
  console.log(`     expired ${secs(Date.now() - t0)} after its last use`);
  const res = await fetch(`${base}/nodes/attachments/${att.id}/url`, { headers: { Authorization: `Bearer ${node.token}` } });
  check(res.status === 410, `GET /nodes/attachments/{id}/url → ${res.status}`);
  const page = await c.raw("GET", `/api/attachments/${att.id}`);
  check(page.status === 410, `the page's download → ${page.status}`);
  daemon = startDaemon(dir, node.token, join(repoRoot, "node"));
  const p = await waitFor("the queued message reaches the node", async () => prompts(dir).find((x) => x.text.includes("离线时发的")), { timeoutMs: 300_000, everyMs: 2000 });
  check(p.text.includes(att.id) && p.text.includes("附件已过期"), "the [附件] block says 附件已过期", p.text.slice(-400));
  const listed = (await messages(c, coord)).find((m) => Number(m.id) === Number(sent.id));
  check(listed?.meta?.attachments?.[0]?.expired === true, "the message list marks it expired");
  const refused = await c.raw("POST", `/api/conversations/${coord}/messages`, { content: "再发一次", attachments: [att.id] });
  check(refused.status === 410, `sending it again → ${refused.status} ${refused.json?.code}`);
}

async function main() {
  mkdirSync(join(scratch, "files"), { recursive: true });
  const c = await login(base);
  const ctx = { stop: [], projects: [] };
  try {
    if (phases.has("main") || phases.has("big")) await mainPhase(c, ctx);
    if (phases.has("big")) await bigPhase(c, ctx);
    if (phases.has("legacy")) await legacyPhase(c, ctx);
    if (phases.has("expire")) await expirePhase(c, ctx);
  } finally {
    for (const stop of ctx.stop) await stop();
    // Deleting the projects queues their objects (projects/<pid>/) for blobgc.
    if (!process.argv.includes("--keep")) {
      for (const pid of ctx.projects) await c.raw("DELETE", `/api/projects/${pid}`);
      for (const id of created) await c.raw("DELETE", `/api/nodes/${id}`);
    }
    writeFileSync(join(scratch, "attachments-timings.json"), JSON.stringify(timings, null, 2));
    console.log("timings (ms):", JSON.stringify(timings));
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
