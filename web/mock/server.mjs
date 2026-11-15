#!/usr/bin/env node
// Tiny mock of the v2 API (docs/v2/api.md) for `npm run dev:mock` and the
// screenshot runs: in-memory demo data, cookie session + CSRF check, SSE with
// Last-Event-ID resume and a looping script of status events for the office.
// Not shipped: the production build only contains src/.
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import * as F from "./fixtures.mjs";

const PORT = Number(process.env.MOCK_PORT ?? 3001);
const here = path.dirname(fileURLToPath(import.meta.url));
const layoutPath = path.resolve(here, "../../office/default.json");
const { ago, ...data } = F;
const state = structuredClone(data);
let seq = 100;
const streams = new Set();

const json = (res, status, body, headers = {}) => {
  res.writeHead(status, { "Content-Type": "application/json", ...headers });
  res.end(body === undefined ? "" : JSON.stringify(body));
};
const fail = (res, status, detail, code = "") => json(res, status, { detail, code });
const cookies = (req) => Object.fromEntries((req.headers.cookie ?? "").split(";").map((c) => c.trim().split("=")).filter((p) => p[0]));
const now = () => new Date().toISOString();
const threadById = (tid) => state.threads.find((t) => t.id === tid);
const threadByNo = (no) => state.threads.find((t) => t.no === Number(no));

function body(req) {
  return new Promise((resolve) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", () => {
      try {
        resolve(raw ? JSON.parse(raw) : {});
      } catch {
        resolve({});
      }
    });
  });
}

function emit(type, data, pid = "p1") {
  seq += 1;
  for (const s of streams) if (s.pid === pid) s.res.write(`id: ${seq}\nevent: ${type}\ndata: ${JSON.stringify(data)}\n\n`);
}

function openStream(req, res, pid) {
  res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no", Connection: "keep-alive" });
  res.write(`id: ${seq}\n: hello\n\n`);
  const s = { pid, res };
  streams.add(s);
  const ping = setInterval(() => res.write(": ping\n\n"), 15000);
  req.on("close", () => {
    clearInterval(ping);
    streams.delete(s);
  });
}

// Looping office script: who does what, every 2.5 s.
const SCRIPT = [
  ["e12d2", 12, { state: "typing", bubble: "改 QuestionCard.tsx" }],
  ["e12q", 12, { state: "running", substate: "test", bubble: "跑 pytest tests/test_comms.py" }],
  ["e12d1", 12, { state: "waiting", substate: "review", bubble: "等审查：审批走 permission-prompt-tool" }],
  ["e13d", 13, { state: "reading", bubble: "看 codex_worker.py" }],
  ["e12l", 12, { state: "reading", bubble: "审查 dev-1 的提交" }],
  ["e12d2", 12, { state: "raising_hand", bubble: "等你批准一条命令", bubble_kind: "ask" }],
  ["e0", null, { state: "thinking", bubble: "整理 #11 的总结" }],
  ["e13d", 13, { state: "idle", bubble: "" }],
  ["e0", null, { state: "idle", bubble: "" }],
];
let step = 0;
setInterval(() => {
  const [id, threadNo, status] = SCRIPT[step++ % SCRIPT.length];
  const full = { substate: "", bubble_kind: "", task_id: "", task_kind: "", since: now(), ...status, seq: seq + 1 };
  emit("status", { employee: { id, thread_no: threadNo }, status: full, seq: seq + 1, at: now() });
}, 2500);

function officeSnapshot() {
  const roster = [{ ...state.coordinator, thread_no: null }, ...state.threads.filter((t) => ["running", "waiting", "review", "queued"].includes(t.status)).flatMap((t) => t.members.map((m) => ({ ...m, thread_no: t.no })))];
  return {
    layout_id: "default",
    layout_version: "1",
    threads: state.threads.filter((t) => t.status !== "done"),
    employees: roster,
    board: { claimable: 3, by_thread: { 12: 2, 13: 1 } },
    nodes: state.nodes.map((n) => ({ id: n.id, name: n.name, online: n.online, isolation: n.isolation, slots: n.max_active_sessions, active_sessions: n.active_sessions, parked_sessions: n.parked_sessions, queued: n.queued })),
    merge_queue: state.mergeQueue.t12,
    ci: { 12: state.checks.t12 },
    last_event_id: seq,
  };
}

function replay() {
  const start = Date.now() - 3600_000;
  const events = Array.from({ length: 120 }, (_, i) => {
    const [id, , status] = SCRIPT[i % SCRIPT.length];
    return { id: i + 1, at: new Date(start + i * 30_000).toISOString(), type: "status", data: { employee: id, status: { substate: "", bubble_kind: "", ...status } } };
  });
  return { snapshot: { ...officeSnapshot(), last_event_id: 0 }, events };
}

function page(items) {
  return { items, next_cursor: null };
}

const storageBytes = () => state.deliverables.reduce((n, d) => n + (d.detail.size ?? 0), 0);

async function sendMessage(res, cid, req) {
  const b = await body(req);
  const m = { id: String(Date.now()), role: "user", author: "liu", kind: b.interrupt ? "interrupt" : "chat", content: b.content, meta: {}, created_at: now() };
  (state.messages[cid] ??= []).push(m);
  emit("message", { conversation_id: cid, message: m });
  setTimeout(() => {
    const reply = { id: String(Date.now() + 1), role: "leader", author: cid === "c0" ? "leader" : threadById(cid)?.members[0]?.name ?? "lead", kind: "chat", content: `收到：「${b.content}」。我来安排。`, meta: {}, created_at: now() };
    state.messages[cid].push(reply);
    emit("message", { conversation_id: cid, message: reply });
  }, 1200);
  json(res, 202, m);
}

async function answer(res, cid, qid, req) {
  const b = await body(req);
  const list = state.questions[cid] ?? [];
  const q = list.find((x) => x.id === qid);
  if (!q) return fail(res, 404, "找不到这个提问", "question_not_found");
  Object.assign(q, { status: "answered", answer: b.answer, answered_at: now(), decided_by: "liu" });
  const t = threadById(cid);
  if (t) {
    t.counts.waiting = Math.max(0, t.counts.waiting - 1);
    emit("thread", { thread: t });
  }
  emit("question", { thread_no: q.thread_no, question: q });
  json(res, 200, q);
}

const routes = [
  ["POST", /^\/auth\/(login|register)$/, async (req, res) => {
    const b = await body(req);
    const user = { ...state.me, email: b.email || state.me.email };
    json(res, 200, user, { "Set-Cookie": ["agent_teams_session=mock; Path=/; HttpOnly; SameSite=Lax", "agent_teams_csrf=csrf-mock; Path=/; SameSite=Lax"] });
  }],
  ["POST", /^\/auth\/logout$/, (req, res) => json(res, 204, undefined, { "Set-Cookie": "agent_teams_session=; Path=/; Max-Age=0" })],
  ["GET", /^\/api\/me$/, (req, res) => json(res, 200, state.me)],
  ["GET", /^\/api\/me\/sessions$/, (req, res) => json(res, 200, [{ id: "s1", created_at: F.ago(600), current: true, expires_at: F.ago(-60000), last_seen_at: F.ago(0), user_agent: "Chrome · Linux" }, { id: "s2", created_at: F.ago(6000), current: false, expires_at: F.ago(-6000), last_seen_at: F.ago(3000), user_agent: "Safari · iPhone" }])],
  ["POST", /^\/api\/me\/password$/, (req, res) => json(res, 204)],
  ["DELETE", /^\/api\/me\/sessions\/[^/]+$/, (req, res) => json(res, 204)],
  ["GET", /^\/api\/events$/, (req, res) => openStream(req, res, "user")],
  ["GET", /^\/api\/projects$/, (req, res) => json(res, 200, state.projects)],
  ["POST", /^\/api\/projects$/, async (req, res) => {
    const b = await body(req);
    const p = { ...state.projects[3], id: `p${Date.now()}`, name: b.name, repo: b.repo ?? null, branch: b.branch ?? "main", git_connection_id: b.git_connection_id ?? null, pinned: false, archived: false, waiting: 0, created_at: now(), updated_at: now() };
    state.projects.unshift(p);
    json(res, 201, p);
  }],
  ["GET", /^\/api\/projects\/([^/]+)$/, (req, res, [pid]) => {
    const p = state.projects.find((x) => x.id === pid);
    return p ? json(res, 200, p) : fail(res, 404, "找不到项目", "project_not_found");
  }],
  ["PATCH", /^\/api\/projects\/([^/]+)$/, async (req, res, [pid]) => {
    const b = await body(req);
    const p = state.projects.find((x) => x.id === pid);
    if (b.delivery) b.delivery = { ...p.delivery, ...b.delivery };
    Object.assign(p, b, { updated_at: now() });
    json(res, 200, p);
  }],
  ["DELETE", /^\/api\/projects\/([^/]+)$/, (req, res, [pid]) => {
    state.projects = state.projects.filter((x) => x.id !== pid);
    json(res, 204);
  }],
  ["GET", /^\/api\/projects\/([^/]+)\/events$/, (req, res, [pid]) => openStream(req, res, pid)],
  ["GET", /^\/api\/projects\/([^/]+)\/coordinator$/, (req, res) => json(res, 200, { conversation_id: "c0", employee: state.coordinator, session_state: "active" })],
  ["GET", /^\/api\/projects\/([^/]+)\/threads$/, (req, res, [pid]) => json(res, 200, pid === "p1" ? state.threads : [])],
  ["GET", /^\/api\/projects\/([^/]+)\/waiting$/, (req, res, [pid]) => json(res, 200, pid === "p1" ? waiting() : { questions: [], reviews: [] })],
  ["GET", /^\/api\/projects\/([^/]+)\/overview$/, (req, res, [pid]) => {
    const p = state.projects.find((x) => x.id === pid);
    json(res, 200, { project: p, threads: pid === "p1" ? state.threads : [], waiting: pid === "p1" ? waiting() : { questions: [], reviews: [] }, recent_deliverables: state.deliverables.slice(0, 3), memory: state.memory.slice(0, 3), usage_7d: { cost_usd_est: 12.4, by_day: [] }, nodes: [] });
  }],
  ["GET", /^\/api\/projects\/([^/]+)\/deliverables$/, (req, res, _m, url) => {
    const kind = url.searchParams.get("kind");
    const q = url.searchParams.get("q") ?? "";
    json(res, 200, { ...page(state.deliverables.filter((d) => (!kind || d.kind === kind) && d.title.includes(q))), storage_bytes: storageBytes() });
  }],
  ["POST", /^\/api\/projects\/([^/]+)\/library$/, async (req, res, [pid]) => {
    const b = await body(req);
    const size = Math.floor((b.data?.length ?? 0) * 0.75);
    const name = String(b.name).split(/[\\/]/).pop();
    const d = { id: `d${Date.now()}`, project_id: pid, thread_no: null, kind: /\.(png|jpe?g|gif|webp|svg|html?)$/i.test(name) ? "design" : "doc", title: b.title || name, url: "", detail: { hosted: true, entry: name, size, version: 1, files: 1, by: "user" }, published_by: "user", created_at: now(), updated_at: now() };
    state.deliverables.unshift(d);
    json(res, 201, d);
  }],
  ["GET", /^\/api\/projects\/([^/]+)\/usage$/, (req, res, _m, url) => json(res, 200, { ...state.usage, days: Number(url.searchParams.get("days") ?? 7) })],
  ["GET", /^\/api\/projects\/([^/]+)\/schedules$/, (req, res) => json(res, 200, state.schedules)],
  ["POST", /^\/api\/projects\/([^/]+)\/schedules$/, async (req, res, [pid]) => {
    const b = await body(req);
    const s = { id: `sc${Date.now()}`, project_id: pid, enabled: true, last_run_at: null, last_error: "", next_run_at: ago(-60), created_at: now(), updated_at: now(), ...b };
    state.schedules.push(s);
    json(res, 201, s);
  }],
  ["PATCH", /^\/api\/schedules\/([^/]+)$/, async (req, res, [id]) => {
    const s = state.schedules.find((x) => x.id === id);
    Object.assign(s, await body(req), { updated_at: now() });
    s.next_run_at = s.enabled ? ago(-60) : null;
    json(res, 200, s);
  }],
  ["DELETE", /^\/api\/schedules\/([^/]+)$/, (req, res, [id]) => ((state.schedules = state.schedules.filter((s) => s.id !== id)), json(res, 204))],
  ["POST", /^\/api\/schedules\/([^/]+)\/run$/, (req, res, [id]) => {
    const s = state.schedules.find((x) => x.id === id);
    s.last_run_at = now();
    json(res, 202);
  }],
  ["POST", /^\/api\/projects\/([^/]+)\/deliverables\/request$/, async (req, res) => {
    const b = await body(req);
    req.body = b;
    const m = { id: String(Date.now()), role: "user", author: "liu", kind: "chat", content: `做一个${b.kind}：${b.brief}`, meta: { request_kind: b.kind }, created_at: now() };
    state.messages.c0.push(m);
    json(res, 202, m);
  }],
  ["GET", /^\/api\/projects\/([^/]+)\/memory$/, (req, res) => json(res, 200, state.memory)],
  ["POST", /^\/api\/projects\/([^/]+)\/memory$/, async (req, res) => {
    const b = await body(req);
    const m = { id: `mem${Date.now()}`, text: b.text, source: "user", pinned: false, created_at: now() };
    state.memory.push(m);
    json(res, 201, m);
  }],
  ["PATCH", /^\/api\/projects\/([^/]+)\/memory\/([^/]+)$/, async (req, res, [, id]) => {
    const m = state.memory.find((x) => x.id === id);
    Object.assign(m, await body(req));
    json(res, 200, m);
  }],
  ["DELETE", /^\/api\/projects\/([^/]+)\/memory\/([^/]+)$/, (req, res, [, id]) => {
    state.memory = state.memory.filter((x) => x.id !== id);
    json(res, 204);
  }],
  ["GET", /^\/api\/projects\/([^/]+)\/office$/, (req, res) => json(res, 200, officeSnapshot())],
  ["GET", /^\/api\/projects\/([^/]+)\/office\/replay$/, (req, res) => json(res, 200, replay())],
  ["GET", /^\/api\/office\/layouts\/([^/]+)$/, (req, res) => {
    res.writeHead(200, { "Content-Type": "application/json", ETag: '"mock-1"' });
    res.end(readFileSync(layoutPath));
  }],
  ["GET", /^\/api\/employees\/([^/]+)$/, (req, res, [id]) => {
    const e = [state.coordinator, ...state.threads.flatMap((t) => t.members)].find((x) => x.id === id);
    if (!e) return fail(res, 404, "找不到员工");
    json(res, 200, { ...e, current_task: state.tasks.t12.find((t) => t.claimed_by === e.name && t.status === "running") ?? null, recent_activity: state.activity.t12.slice(0, 3), session: { state: "active", node: e.node?.name ?? null } });
  }],
  ["GET", /^\/api\/conversations$/, (req, res) => json(res, 200, [{ id: "legacy1", title: "v1：给项目加置顶", created_at: F.ago(60 * 24 * 6), project_id: "p1" }])],
  ["GET", /^\/api\/conversations\/([^/]+)\/messages$/, (req, res, [cid]) => json(res, 200, page(state.messages[cid] ?? (cid === "legacy1" ? [{ id: "l1", role: "user", author: "liu", kind: "chat", content: "给项目加个置顶功能", meta: {}, created_at: F.ago(8000) }, { id: "l2", role: "leader", author: "leader", kind: "chat", content: "好的，已经交给工程师。", meta: {}, created_at: F.ago(7990) }] : [])))],
  ["POST", /^\/api\/conversations\/([^/]+)\/messages$/, (req, res, [cid]) => sendMessage(res, cid, req)],
  ["POST", /^\/api\/conversations\/([^/]+)\/interrupt$/, (req, res) => json(res, 202)],
  ["GET", /^\/api\/conversations\/([^/]+)\/questions$/, (req, res, [cid]) => json(res, 200, state.questions[cid] ?? [])],
  ["POST", /^\/api\/conversations\/([^/]+)\/questions\/([^/]+)\/answer$/, (req, res, [cid, qid]) => answer(res, cid, qid, req)],
  ["GET", /^\/api\/conversations\/([^/]+)\/tasks$/, (req, res) => json(res, 200, [{ id: "t1", assignee: "engineer", instruction: "实现置顶", status: "done" }])],
  ["GET", /^\/api\/threads\/([^/]+)$/, (req, res, [tid]) => json(res, 200, threadById(tid))],
  ["PATCH", /^\/api\/threads\/([^/]+)$/, async (req, res, [tid]) => {
    const t = threadById(tid);
    const b = await body(req);
    Object.assign(t, b.status ? { status: b.status } : {}, b.auto_approve !== undefined ? { auto_approve: b.auto_approve } : {}, b.title ? { title: b.title } : {});
    emit("thread", { thread: t });
    json(res, 200, t);
  }],
  ["PATCH", /^\/api\/threads\/([^/]+)\/main-tasks\/([^/]+)$/, async (req, res, [tid, mid]) => {
    const t = threadById(tid);
    const m = t.main_tasks.find((x) => x.id === mid);
    Object.assign(m, await body(req));
    emit("main_task", { thread_no: t.no, main_tasks: t.main_tasks });
    json(res, 200, m);
  }],
  ["GET", /^\/api\/threads\/([^/]+)\/tasks$/, (req, res, [tid]) => json(res, 200, state.tasks[tid] ?? [])],
  ["POST", /^\/api\/threads\/([^/]+)\/tasks\/([^/]+)\/(cancel|retry)$/, (req, res, [tid, id, op]) => {
    const task = (state.tasks[tid] ?? []).find((t) => t.id === id);
    task.status = op === "cancel" ? "cancelled" : "pending";
    emit("task", { thread_no: threadById(tid).no, task });
    json(res, 200, task);
  }],
  ["GET", /^\/api\/threads\/([^/]+)\/members$/, (req, res, [tid]) => json(res, 200, threadById(tid)?.members ?? [])],
  ["POST", /^\/api\/threads\/([^/]+)\/members\/([^/]+)\/message$/, async (req, res, [tid]) => {
    const b = await body(req);
    json(res, 202, { id: String(Date.now()), role: "user", author: "liu", kind: "chat", content: b.content, meta: {}, created_at: now(), tid });
  }],
  ["GET", /^\/api\/threads\/([^/]+)\/activity$/, (req, res, [tid]) => json(res, 200, page(state.activity[tid] ?? []))],
  ["GET", /^\/api\/threads\/([^/]+)\/mail$/, (req, res) => json(res, 200, page([]))],
  ["GET", /^\/api\/threads\/([^/]+)\/merge-queue$/, (req, res, [tid]) => json(res, 200, state.mergeQueue[tid] ?? [])],
  ["GET", /^\/api\/threads\/([^/]+)\/checks$/, (req, res, [tid]) => json(res, 200, state.checks[tid] ?? [])],
  ["POST", /^\/api\/threads\/([^/]+)\/(accept|reject|open-pr)$/, (req, res, [tid, op]) => {
    const t = threadById(tid);
    if (op === "accept") Object.assign(t, { status: "done", pr_state: "merged" });
    if (op === "reject") t.status = "running";
    if (op === "open-pr") Object.assign(t, { status: "review", pr_number: 42, pr_url: "https://github.com/touwaeriol/agent-teams/pull/42" });
    emit("thread", { thread: t });
    json(res, 200, t);
  }],
  ["GET", /^\/api\/nodes$/, (req, res) => json(res, 200, state.nodes)],
  ["POST", /^\/api\/nodes$/, async (req, res) => {
    const b = await body(req);
    const n = { ...state.nodes[0], id: `n${Date.now()}`, name: b.name, online: false, since: null, active_sessions: 0, version: "" };
    state.nodes.push(n);
    json(res, 201, { node: n, token: "atn_mock", install: `curl -fsSL http://localhost:5173/install-node.sh | sh -s -- --token atn_mock` });
  }],
  ["PATCH", /^\/api\/nodes\/([^/]+)$/, async (req, res, [id]) => json(res, 200, Object.assign(state.nodes.find((n) => n.id === id), await body(req)))],
  ["DELETE", /^\/api\/nodes\/([^/]+)$/, (req, res, [id]) => ((state.nodes = state.nodes.filter((n) => n.id !== id)), json(res, 204))],
  ["POST", /^\/api\/nodes\/([^/]+)\/token$/, (req, res) => json(res, 200, { token: "atn_new", install: "curl -fsSL http://localhost:5173/install-node.sh | sh -s -- --token atn_new" })],
  ["POST", /^\/api\/nodes\/([^/]+)\/upgrade$/, (req, res) => json(res, 202)],
  ["GET", /^\/api\/nodes\/([^/]+)\/sessions$/, (req, res, [id]) => json(res, 200, state.nodeSessions[id] ?? [])],
  ["GET", /^\/api\/roles$/, (req, res) => json(res, 200, { roles: state.roles, warnings: [] })],
  ["PUT", /^\/api\/roles$/, async (req, res) => {
    const b = await body(req);
    state.roles = b.roles.map((r, i) => ({ ...r, id: r.id || `r${Date.now()}${i}` }));
    json(res, 200, { roles: state.roles, warnings: [] });
  }],
  ["POST", /^\/api\/roles\/look\/random$/, (req, res) => {
    const colors = ["#4C7BD0", "#3F8F6B", "#C8622A", "#7A4FD1", "#B7413E", "#2F8F9D", "#C79A1E", "#4A4843"];
    const pick = (a) => a[Math.floor(Math.random() * a.length)];
    json(res, 200, { color: pick(colors), outfit: pick(["tee", "hoodie", "suit", "coat"]), accessory: pick(["none", "glasses", "headset", "cap"]), hair: null });
  }],
  ["GET", /^\/api\/roles\/yaml$/, (req, res) => json(res, 200, { yaml: state.roles.map((r) => `- role: ${r.role}\n  tier: ${r.tier}\n  title: ${r.title}\n  runtime: ${r.runtime}\n  count: ${r.count}`).join("\n") + "\n" })],
  ["PUT", /^\/api\/roles\/yaml$/, (req, res) => json(res, 200, { roles: state.roles, warnings: [] })],
  ["GET", /^\/api\/git\/connections$/, (req, res) => json(res, 200, state.connections)],
  ["POST", /^\/api\/git\/connections$/, async (req, res) => json(res, 201, { ...state.connections[0], id: `g${Date.now()}`, kind: "token", installation_settings_url: null })],
  ["DELETE", /^\/api\/git\/connections\/([^/]+)$/, (req, res) => json(res, 204)],
  ["POST", /^\/api\/git\/connections\/([^/]+)\/refresh$/, (req, res) => json(res, 200, state.connections[0])],
  ["GET", /^\/api\/git\/connections\/([^/]+)\/repos$/, (req, res, _m, url) => {
    const q = url.searchParams.get("q") ?? "";
    json(res, 200, { items: state.repos.filter((r) => r.full_name.includes(q)), next_cursor: null });
  }],
  ["GET", /^\/api\/git\/connections\/([^/]+)\/branches$/, (req, res) => json(res, 200, ["main", "develop", "release/2.0"])],
  ["GET", /^\/api\/git\/github-app$/, (req, res) => json(res, 200, { configured: true, install_url: "https://github.com/apps/agent-teams-mock/installations/new", app: { slug: "agent-teams-mock", name: "Agent Teams (mock)", owner: "touwaeriol", html_url: "https://github.com/apps/agent-teams-mock", permissions: { contents: "write", pull_requests: "write", checks: "read" }, webhook_active: true } })],
  ["GET", /^\/api\/skills$/, (req, res) => json(res, 200, { skills: state.skills })],
  ["GET", /^\/api\/roles\/presets$/, (req, res) => json(res, 200, { roles: [] })],
  ["GET", /^\/api\/plugins$/, (req, res) => json(res, 200, { plugins: state.plugins })],
  ["PATCH", /^\/api\/plugins\/([^/]+)$/, async (req, res, [id]) => json(res, 200, Object.assign(state.plugins.find((p) => p.id === id), await body(req)))],
  ["DELETE", /^\/api\/plugins\/([^/]+)$/, (req, res, [id]) => ((state.plugins = state.plugins.filter((p) => p.id !== id)), json(res, 204))],
  ["GET", /^\/api\/admin\/plugins$/, (req, res) => json(res, 200, { plugins: state.adminPlugins })],
  ["PATCH", /^\/api\/admin\/plugins\/([^/]+)$/, async (req, res, [id]) => json(res, 200, Object.assign(state.adminPlugins.find((p) => p.id === id), await body(req)))],
  ["GET", /^\/api\/admin\/users$/, (req, res) => json(res, 200, state.adminUsers)],
  ["PATCH", /^\/api\/admin\/users\/([^/]+)$/, async (req, res, [id]) => json(res, 200, Object.assign(state.adminUsers.find((u) => u.id === id), await body(req)))],
  ["DELETE", /^\/api\/admin\/users\/([^/]+)$/, (req, res, [id]) => ((state.adminUsers = state.adminUsers.filter((u) => u.id !== id)), json(res, 204))],
  ["POST", /^\/api\/admin\/users\/([^/]+)\/password-reset$/, (req, res) => json(res, 200, { temp_password: "tmp-7f3k-9q2m" })],
  ["GET", /^\/api\/admin\/settings$/, (req, res) => json(res, 200, state.adminSettings)],
  ["PUT", /^\/api\/admin\/settings$/, async (req, res) => json(res, 200, Object.assign(state.adminSettings, await body(req)))],
  ["GET", /^\/api\/admin\/audit$/, (req, res) => json(res, 200, { items: [{ id: 1, action: "login", actor: "admin@prophey.ai", created_at: F.ago(5), detail: {}, ip: "127.0.0.1", target_id: "u1", target_type: "user", user_id: "u1" }], next_cursor: null })],
];

function waiting() {
  return { questions: [...state.questions.t12, ...state.questions.t13].filter((q) => q.status === "open"), reviews: state.threads.filter((t) => t.status === "review") };
}

const PUBLIC = new Set(["/auth/login", "/auth/register"]);

createServer(async (req, res) => {
  const url = new URL(req.url, "http://mock");
  const c = cookies(req);
  if (!PUBLIC.has(url.pathname) && !c.agent_teams_session) return fail(res, 401, "请先登录", "unauthenticated");
  const write = !["GET", "HEAD"].includes(req.method);
  if (write && !PUBLIC.has(url.pathname) && req.headers["x-csrf-token"] !== c.agent_teams_csrf) return fail(res, 403, "安全校验失败，请刷新页面后重试", "csrf");
  for (const [method, re, handler] of routes) {
    const m = req.method === method && url.pathname.match(re);
    if (m) return handler(req, res, m.slice(1), url);
  }
  fail(res, 404, `mock: no route for ${req.method} ${url.pathname}`, "not_found");
}).listen(PORT, () => console.log(`mock API on http://127.0.0.1:${PORT}`));
