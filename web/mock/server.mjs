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
state.feedback ??= [];
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
/** What the server adds to a thread in lists: its deliverable count, newest deliverable and when anything last happened. */
const threadOut = (t) => {
  const own = state.deliverables.filter((d) => d.thread_no === t.no);
  const latest = [...own].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))[0] ?? null;
  const times = [t.updated_at, t.lead_last?.at, latest?.updated_at].filter(Boolean).map((x) => Date.parse(x));
  return { ...t, counts: { ...t.counts, deliverables: own.length }, latest_deliverable: latest, last_activity_at: new Date(Math.max(...times)).toISOString() };
};
/** Threads whose merge already failed once in the mock (the next accept merges). */
const mergeTried = new Set();
/** The built-in last resort per runtime (orchestrator.DefaultModel / DefaultCodexModel). */
const builtinModel = (runtime) => (runtime === "codex" ? "" : "claude-opus-5-5");
const roleOf = (tier, roleKey) => state.roles.find((r) => (roleKey ? r.role === roleKey : r.tier === tier));
/** orchestrator.Model: member /model → project → role → built in; lead project → role → built in; coordinator coordinator_model → role → built in. */
function effectiveModel(m, p) {
  const runtime = m.runtime ?? "claude";
  const project = m.tier === "coordinator" ? p?.coordinator_model : runtime === "codex" ? p?.thread_codex_model : p?.thread_model;
  const role = m.tier === "member" ? roleOf("member", m.role) : roleOf(m.tier);
  return (m.tier === "member" && m.override) || project || role?.model || builtinModel(runtime);
}
/** GET /api/conversations/{cid}/model: the coordinator's or the lead's effective model (no /model override); members as above. */
function conversationModel(cid) {
  const t = threadById(cid);
  if (!t) {
    const p = state.projects.find((x) => x.id === "p1");
    const model = effectiveModel({ tier: "coordinator", runtime: "claude" }, p);
    return { scope: "coordinator", employee: state.coordinator.name, runtime: "claude", model, override: "", inherited: model, session: "", live: false, members: [] };
  }
  const p = state.projects.find((x) => x.id === t.project_id);
  const lead = t.members.find((m) => m.tier === "lead");
  const runtime = lead?.runtime ?? "claude";
  const model = effectiveModel({ tier: "lead", runtime }, p);
  const members = t.members.map((m) => ({ name: m.name, tier: m.tier, runtime: m.runtime, model: effectiveModel(m, p), override: "" }));
  return { scope: "thread", employee: lead?.name ?? "", runtime, model, override: "", inherited: model, session: "", live: false, members };
}
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
  ["e12d2", 12, { state: "typing", bubble: "改 QuestionCard.tsx", task_id: "t6" }],
  ["e12q", 12, { state: "running", substate: "test", bubble: "跑 pytest tests/test_comms.py", task_id: "t4" }],
  ["e12d1", 12, { state: "waiting", substate: "review", bubble: "等审查：审批走 permission-prompt-tool" }],
  ["e14d", 14, { state: "reading", bubble: "看 Hero.vue", task_id: "t2" }],
  ["e12l", 12, { state: "reading", bubble: "审查 dev-1 的提交" }],
  ["e12d2", 12, { state: "raising_hand", bubble: "等你批准一条命令", bubble_kind: "ask", task_id: "t6" }],
  ["e0", null, { state: "thinking", bubble: "整理 #11 的总结" }],
  ["e14d", 14, { state: "idle", bubble: "" }],
  ["e0", null, { state: "idle", bubble: "" }],
];
let step = 0;
setInterval(() => {
  const [id, threadNo, status] = SCRIPT[step++ % SCRIPT.length];
  const full = { substate: "", bubble_kind: "", task_id: "", task_kind: "", since: now(), ...status, seq: seq + 1 };
  emit("status", { employee: { id, thread_no: threadNo }, status: full, seq: seq + 1, at: now() });
}, 2500);

// Live activity for thread #12, so the progress tab and chat chips have something to stream.
const LINES = [
  ["dev-2", "Bash pnpm vitest run src/components/cards.test.tsx"],
  ["qa-1", "Bash go test ./internal/approvals/..."],
  ["dev-2", "Edit web/src/components/app/cards.tsx"],
  ["lead-12", "Read server/internal/approvals/approvals.go"],
  ["qa-1", "PG 往返测试 12/14 通过"],
];
let line = 0;
setInterval(() => {
  const [employee, text] = LINES[line++ % LINES.length];
  const a = { thread_no: 12, employee, text, at: now() };
  (state.activity.t12 ??= []).push(a);
  emit("activity", a);
}, 4000);

const attachments = new Map();
// The screenshot the user pasted into thread #12 (fixtures: messages.t12).
attachments.set("img_seed", {
  ref: { id: "img_seed", name: "截图.png", content_type: "image/svg+xml", size: 0 },
  data: Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="240" height="180"><rect width="240" height="180" fill="#F7F6F3"/><rect x="16" y="16" width="208" height="56" rx="10" fill="#FFF9F4" stroke="#F0C9A8"/><rect x="30" y="32" width="96" height="9" rx="4" fill="#C8622A"/><rect x="30" y="48" width="160" height="7" rx="3" fill="#E2DFD8"/><rect x="16" y="86" width="64" height="24" rx="7" fill="#1F1E1C"/><rect x="88" y="86" width="64" height="24" rx="7" fill="#fff" stroke="#E2DFD8"/><rect x="16" y="124" width="208" height="40" rx="8" fill="none" stroke="#D93025" stroke-width="2"/></svg>`),
});

// Project members are shared by every thread (shared-members.md): each name once, at the
// thread of its current task; every live thread has its own lead.
const ROLE_SORT = { lead: 1, dev: 2, qa: 3, designer: 4 };
const MEMBER_TASK = { e12d2: "t6", e12q: "t4", e14d: "t2" };
function officeRoster() {
  const live = state.threads.filter((t) => ["running", "waiting", "review", "queued"].includes(t.status)).sort((a, b) => a.no - b.no);
  const leads = live.flatMap((t) => t.members.filter((m) => m.tier === "lead").map((m) => ({ ...m, thread_no: t.no, sort: 1, instance: 1 })));
  const staff = new Map();
  for (const t of live)
    for (const m of t.members)
      if (m.tier === "member" && !staff.has(m.name))
        staff.set(m.name, { ...m, thread_no: t.no, sort: ROLE_SORT[m.role] ?? 9, instance: Number(m.name.split("-").pop()) || 1, status: { ...m.status, task_id: MEMBER_TASK[m.id] ?? "" } });
  return [{ ...state.coordinator, thread_no: null, sort: 0, instance: 1 }, ...leads, ...staff.values()];
}

function officeSnapshot() {
  return {
    layout_id: "default",
    layout_version: "1",
    threads: state.threads.filter((t) => t.status !== "done"),
    employees: officeRoster(),
    board: {
      claimable: 5,
      by_thread: { 12: { pending: 2 }, 13: { pending: 1 }, 14: { pending: 2 } },
      queue: [
        { thread_no: 12, id: "t7", role: "dev", assignee: "", locked: false, reserved_for: "dev-1" },
        { thread_no: 12, id: "t8", role: "qa", assignee: "", locked: true, reserved_for: "" },
        { thread_no: 13, id: "t3", role: "dev", assignee: "", locked: false, reserved_for: "" },
        { thread_no: 14, id: "t3", role: "designer", assignee: "", locked: false, reserved_for: "" },
        { thread_no: 14, id: "t4", role: "dev", assignee: "", locked: true, reserved_for: "" },
      ],
    },
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
  const images = (b.attachments ?? []).map((id) => attachments.get(id)?.ref).filter(Boolean);
  const meta = images.length ? { attachments: images } : {};
  const m = { id: String(Date.now()), role: "user", author: "liu", kind: b.interrupt ? "interrupt" : "chat", content: b.content ?? "", meta, created_at: now() };
  (state.messages[cid] ??= []).push(m);
  emit("message", { conversation_id: cid, message: m });
  setTimeout(() => {
    const said = images.length ? `${images.length} 张图片${b.content ? `和「${b.content}」` : ""}` : `「${b.content}」`;
    const reply = { id: String(Date.now() + 1), role: "leader", author: cid === "c0" ? "leader" : threadById(cid)?.members[0]?.name ?? "lead", kind: "chat", content: `收到 ${said}。我来安排。`, meta: {}, created_at: now() };
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
  const items = q.detail?.questions ?? [];
  const answers = Array.isArray(b.answers) ? items.map((it, i) => ({ question: it.question, header: it.header, selected: b.answers[i]?.selected ?? [], ...(b.answers[i]?.other ? { other: b.answers[i].other } : {}) })) : null;
  const text = answers ? answers.map((a) => [...a.selected, ...(a.other ? [a.other] : [])].join("、")).join("\n") : b.answer;
  Object.assign(q, { status: "answered", answer: text, answered_at: now(), decided_by: "liu", ...(answers ? { detail: { ...q.detail, answers } } : {}) });
  const t = threadById(cid);
  if (t) {
    t.counts.waiting = Math.max(0, t.counts.waiting - 1);
    emit("thread", { thread: threadOut(t) });
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
    const p = { ...state.projects[3], id: `p${Date.now()}`, name: b.name, coordinator_model: b.coordinator_model ?? "", thread_model: b.thread_model ?? "", thread_codex_model: b.thread_codex_model ?? "", repo: b.repo ?? null, branch: b.branch ?? "main", git_connection_id: b.git_connection_id ?? null, pinned: false, archived: false, waiting: 0, created_at: now(), updated_at: now() };
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
  ["GET", /^\/api\/projects\/([^/]+)\/threads$/, (req, res, [pid]) => json(res, 200, pid === "p1" ? state.threads.map(threadOut) : [])],
  ["GET", /^\/api\/projects\/([^/]+)\/waiting$/, (req, res, [pid]) => json(res, 200, pid === "p1" ? waiting() : { questions: [], reviews: [] })],
  ["GET", /^\/api\/projects\/([^/]+)\/overview$/, (req, res, [pid]) => {
    const p = state.projects.find((x) => x.id === pid);
    json(res, 200, { project: p, threads: pid === "p1" ? state.threads : [], waiting: pid === "p1" ? waiting() : { questions: [], reviews: [] }, recent_deliverables: state.deliverables.slice(0, 3), memory: memoryList().slice(0, 6), usage_7d: { cost_usd_est: 12.4, by_day: [] }, nodes: [] });
  }],
  ["GET", /^\/api\/projects\/([^/]+)\/deliverables$/, (req, res, _m, url) => {
    const kind = url.searchParams.get("kind");
    const q = url.searchParams.get("q") ?? "";
    // no_images=true: leave out hosted images (render=image), like the real server.
    const noImages = url.searchParams.get("no_images") === "true";
    const shown = (d) => !(noImages && d.detail?.hosted && d.detail?.render === "image");
    json(res, 200, { ...page(state.deliverables.filter((d) => (!kind || d.kind === kind) && d.title.includes(q) && shown(d)).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))), storage_bytes: storageBytes() });
  }],
  ["POST", /^\/api\/projects\/([^/]+)\/library$/, async (req, res, [pid]) => {
    const b = await body(req);
    const size = Math.floor((b.data?.length ?? 0) * 0.75);
    const name = String(b.name).split(/[\\/]/).pop();
    if (/\.(png|jpe?g|gif|webp|svg)$/i.test(name)) {
      // Same as the real server: 422 validation on the name field.
      const detail = `截图和图片（含 SVG）不是交付物：${name}。可以把图片直接内联进 HTML（data: 地址或内嵌 SVG）再上传，或作为附件发在讨论串消息里（支持 html、md、pdf、txt、json、csv）`;
      return json(res, 422, { detail, code: "validation", fields: [{ path: "name", message: detail }] });
    }
    const d = { id: `d${Date.now()}`, project_id: pid, thread_no: null, kind: /\.html?$/i.test(name) ? "design" : "doc", title: b.title || name, url: "", detail: { hosted: true, entry: name, size, version: 1, files: 1, by: "user" }, published_by: "user", created_at: now(), updated_at: now() };
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
  ["GET", /^\/api\/projects\/([^/]+)\/memory$/, (req, res) => json(res, 200, memoryList())],
  ["GET", /^\/api\/projects\/([^/]+)\/memory\/([^/]+)$/, (req, res, [, , name]) => {
    const f = state.memory.find((x) => x.name === decodeURIComponent(name));
    return f ? json(res, 200, f) : json(res, 404, { title: "没有这个记忆文件" });
  }],
  ["PUT", /^\/api\/projects\/([^/]+)\/memory\/([^/]+)$/, async (req, res, [, , raw]) => {
    const b = await body(req);
    const name = decodeURIComponent(raw);
    const f = state.memory.find((x) => x.name === name) ?? { name, created_at: now() };
    Object.assign(f, { description: name === "MEMORY.md" ? "" : b.description ?? "", content: b.content ?? "", updated_by: "user", updated_at: now() });
    if (!state.memory.includes(f)) state.memory.push(f);
    json(res, 200, f);
  }],
  ["DELETE", /^\/api\/projects\/([^/]+)\/memory\/([^/]+)$/, (req, res, [, , name]) => {
    state.memory = state.memory.filter((x) => x.name !== decodeURIComponent(name));
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
  ["GET", /^\/api\/conversations\/([^/]+)\/messages$/, (req, res, [cid]) => json(res, 200, page(state.messages[cid] ?? []))],
  ["POST", /^\/api\/conversations\/([^/]+)\/messages$/, (req, res, [cid]) => sendMessage(res, cid, req)],
  ["GET", /^\/api\/conversations\/([^/]+)\/model$/, (req, res, [cid]) => json(res, 200, conversationModel(cid))],
  // Same refusal as the server (orchestrator.ErrFixedModel → 409 model_fixed); the mock keeps no member overrides.
  ["PUT", /^\/api\/conversations\/([^/]+)\/model$/, (req, res) => fail(res, 409, "统筹和组长不能用 /model 切换模型，请在岗位配置或项目设置里改", "model_fixed")],
  ["POST", /^\/api\/conversations\/([^/]+)\/interrupt$/, (req, res) => json(res, 202)],
  ["GET", /^\/api\/conversations\/([^/]+)\/questions$/, (req, res, [cid]) => json(res, 200, state.questions[cid] ?? [])],
  ["POST", /^\/api\/conversations\/([^/]+)\/questions\/([^/]+)\/answer$/, (req, res, [cid, qid]) => answer(res, cid, qid, req)],
  ["GET", /^\/api\/threads\/([^/]+)$/, (req, res, [tid]) => json(res, 200, threadById(tid))],
  ["POST", /^\/api\/threads\/([^/]+)\/(archive|unarchive)$/, (req, res, [tid, op]) => {
    const t = threadById(tid);
    if (op === "archive") Object.assign(t, { archived_from: t.status, status: "archived" });
    else Object.assign(t, { status: ["done", "failed"].includes(t.archived_from) ? t.archived_from : "paused" });
    emit("thread", { thread: threadOut(t) });
    json(res, 200, t);
  }],
  ["DELETE", /^\/api\/threads\/([^/]+)$/, (req, res, [tid]) => {
    const t = threadById(tid);
    state.threads = state.threads.filter((x) => x.id !== tid);
    state.deliverables.forEach((d) => d.thread_no === t.no && (d.thread_no = null));
    emit("thread", { deleted: { id: t.id, no: t.no } });
    json(res, 204);
  }],
  ["POST", /^\/api\/projects\/([^/]+)\/attachments$/, async (req, res) => {
    const b = await body(req);
    const data = Buffer.from(b.data ?? "", "base64");
    const type = data[0] === 0x89 ? "image/png" : data[0] === 0xff ? "image/jpeg" : data.subarray(0, 3).toString() === "GIF" ? "image/gif" : data.subarray(8, 12).toString() === "WEBP" ? "image/webp" : "";
    if (!type) return fail(res, 422, "只支持 PNG、JPG、GIF、WebP 图片", "validation");
    const id = `img_${Date.now()}${Math.floor(Math.random() * 1000)}`;
    const ref = { id, name: b.name, content_type: type, size: data.length };
    attachments.set(id, { ref, data });
    json(res, 201, { ...ref, sha256: "", created_at: now(), url: `/api/attachments/${id}` });
  }],
  ["GET", /^\/api\/attachments\/([^/]+)$/, (req, res, [id]) => {
    const a = attachments.get(id);
    if (!a) return fail(res, 404, "没有这张图片");
    res.writeHead(200, { "Content-Type": a.ref.content_type });
    res.end(a.data);
  }],
  ["GET", /^\/api\/deliverables\/([^/]+)\/files$/, (req, res, [id]) => {
    const d = state.deliverables.find((x) => x.id === id);
    if (!d?.detail.hosted) return fail(res, 404, "没有文件");
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="240"><rect width="320" height="240" fill="#F4EFE8"/><rect x="24" y="28" width="272" height="64" rx="12" fill="#FFF9F4" stroke="#F0C9A8"/><rect x="40" y="48" width="120" height="10" rx="5" fill="#C8622A"/><rect x="40" y="66" width="200" height="8" rx="4" fill="#E2DFD8"/><rect x="24" y="108" width="80" height="28" rx="8" fill="#1F1E1C"/><rect x="112" y="108" width="80" height="28" rx="8" fill="#fff" stroke="#E2DFD8"/></svg>`;
    const html = `<!doctype html><meta charset="utf-8"><body style="margin:0;font-family:system-ui;background:#FBF8F4"><div style="padding:56px 64px"><div style="width:120px;height:18px;border-radius:9px;background:#C8622A"></div><h1 style="font-size:64px;margin:40px 0 16px;color:#1F1E1C">让 AI 团队替你交付</h1><p style="font-size:26px;color:#6B6862;margin:0 0 36px">在你自己的机器上运行 Claude Code 员工</p><div style="display:flex;gap:20px"><span style="padding:18px 34px;border-radius:14px;background:#1F1E1C;color:#fff;font-size:24px">开始使用</span><span style="padding:18px 34px;border-radius:14px;border:2px solid #E2DFD8;font-size:24px">看演示</span></div><div style="display:flex;gap:24px;margin-top:64px">${[1, 2, 3].map(() => '<div style="flex:1;height:220px;border-radius:20px;background:#fff;border:2px solid #EFEDE8"></div>').join("")}</div></div></body>`;
    const md = `# ${d.title.replace(/\.md$/, "")}\n\n控制台、节点和员工三部分组成。控制台负责项目、讨论串和交付物；节点在你自己的机器上运行员工。\n\n## 组成\n\n- server：Go，提供 API 和事件流\n- node：在机器上拉起 Claude Code\n- web：Vue 控制台\n`;
    const url = d.detail.render === "html" ? `data:text/html;base64,${Buffer.from(html).toString("base64")}` : d.detail.render === "markdown" ? `data:text/markdown;charset=utf-8;base64,${Buffer.from(md).toString("base64")}` : `data:image/svg+xml;base64,${Buffer.from(svg).toString("base64")}`;
    json(res, 200, { deliverable_id: id, version: 1, versions: [{ version: 1, created_at: d.created_at, by: d.published_by, files: 1, size: d.detail.size }], entry: d.detail.entry, render: d.detail.render ?? "image", files: [], base_url: "", entry_url: url, expires_at: ago(-120) });
  }],
  ["PATCH", /^\/api\/threads\/([^/]+)$/, async (req, res, [tid]) => {
    const t = threadById(tid);
    const b = await body(req);
    Object.assign(t, b.status ? { status: b.status } : {}, b.auto_approve !== undefined ? { auto_approve: b.auto_approve } : {}, b.title ? { title: b.title } : {});
    if (b.pinned !== undefined) t.pinned_at = b.pinned ? now() : null;
    emit("thread", { thread: threadOut(t) });
    json(res, 200, t);
  }],
  ["PUT", /^\/api\/threads\/([^/]+)\/reminder$/, async (req, res, [tid]) => {
    const t = threadById(tid);
    Object.assign(t, { remind_at: (await body(req)).at, reminded_at: null });
    emit("thread", { thread: threadOut(t) });
    json(res, 200, threadOut(t));
  }],
  ["DELETE", /^\/api\/threads\/([^/]+)\/reminder$/, (req, res, [tid]) => {
    const t = threadById(tid);
    Object.assign(t, { remind_at: null, reminded_at: null });
    emit("thread", { thread: threadOut(t) });
    json(res, 200, threadOut(t));
  }],
  ["POST", /^\/api\/threads\/([^/]+)\/feedback$/, async (req, res, [tid]) => {
    const t = threadById(tid);
    const b = await body(req);
    const f = { id: `fb_${Date.now()}`, user_id: "u1", user_email: "admin@prophey.ai", project_id: t.project_id, thread_id: t.id, thread_no: t.no,
      thread_title: t.title, text: b.text, content: b.include_content ? `#${t.no} ${t.title}\n\n[对话内容]` : "", handled_at: null, handled_by: "", created_at: now() };
    state.feedback.unshift(f);
    json(res, 201, f);
  }],
  ["GET", /^\/api\/admin\/feedback$/, (req, res) => json(res, 200, state.feedback)],
  ["PATCH", /^\/api\/admin\/feedback\/([^/]+)$/, async (req, res, [id]) => {
    const f = state.feedback.find((x) => x.id === id);
    const b = await body(req);
    Object.assign(f, b.handled ? { handled_at: now(), handled_by: "admin@prophey.ai" } : { handled_at: null, handled_by: "" });
    json(res, 200, f);
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
    const t = threadById(tid);
    t.counts.tasks_open = (state.tasks[tid] ?? []).filter((x) => !["done", "failed", "cancelled"].includes(x.status)).length;
    emit("task", { thread_no: t.no, task, counts: t.counts });
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
    // Accept is asynchronous like the server: answer with pr_state accepting, merge in the background.
    // The first try of a thread fails (to show merge_error and 重试合并), a retry merges.
    if (op === "accept") {
      Object.assign(t, { pr_state: "accepting", merge_error: null });
      const fail = !mergeTried.has(tid);
      mergeTried.add(tid);
      setTimeout(() => {
        if (fail) Object.assign(t, { pr_state: "open", merge_error: "与基准分支 develop 有冲突，请让组长先解决冲突" });
        else Object.assign(t, { status: "done", pr_state: "merged", merge_error: null });
        emit("thread", { thread: threadOut(t) });
      }, Number(process.env.MOCK_MERGE_MS ?? 3000));
    }
    if (op === "reject") Object.assign(t, { status: "running", merge_error: null });
    if (op === "open-pr") Object.assign(t, { status: "review", pr_number: 42, pr_url: "https://github.com/touwaeriol/agent-teams/pull/42" });
    emit("thread", { thread: threadOut(t) });
    json(res, 200, threadOut(t));
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
    // Every tier keeps its typed model (trimmed), as the server does.
    state.roles = b.roles.map((r, i) => ({ ...r, model: (r.model ?? "").trim(), id: r.id || `r${Date.now()}${i}` }));
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

/** Memory files without content, MEMORY.md first (GET /memory, the overview box). */
function memoryList() {
  const brief = ({ name, description, content, updated_by, updated_at }) => ({ name, description, size: content.length, updated_by, updated_at });
  return [...state.memory].sort((a, b) => (a.name === "MEMORY.md" ? -1 : b.name === "MEMORY.md" ? 1 : a.name.localeCompare(b.name))).map(brief);
}
