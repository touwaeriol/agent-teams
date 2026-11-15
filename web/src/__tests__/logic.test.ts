import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { isWrite, readCookie, toApiError } from "@/api/client";
import { appendMessage, applyProjectEvent, applyUserEvent, layoutTickKey, statusLogKey, upsert, type StatusFrame } from "@/api/events";
import { flattenMessages, keys, type MessagePages } from "@/api/queries";
import { backoff, streamUrl } from "@/api/sse";
import type { OfficeSnapshot, QuestionView, TaskView, ThreadView } from "@/api/views";
import { normalizeRepo, repoPayload } from "@/components/app/repo";
import { teamChips } from "@/components/app/roles";
import { summarizeNodes } from "@/components/shell/nodes";
import { matchesProject } from "@/components/shell/ProjectSidebar";
import { chatBlocks } from "@/lib/chat";
import { boardSummary, pickChecks, replayEvents, toOfficeEvents, toWidgetData } from "@/lib/office";
import { countText, isReadOnly, newRole, nextRoleKey, previewTags, roleProblems, ruleText, skipHint } from "@/lib/roles";
import { boardItems } from "@/pages/Office";
import { filterThreads, teamSummary } from "@/pages/Overview";
import { recentProject } from "@/pages/Projects";
import { memorySource } from "@/pages/ProjectSettings";
import { runtimeLine } from "@/pages/settings/Nodes";
import { sortRepos } from "@/pages/settings/GitHub";
import { deliverableHref } from "@/pages/Deliverables";
import { message, question, task, thread } from "./fixtures";

describe("client", () => {
  it("reads cookies and classifies writes", () => {
    expect(readCookie("a=1; agent_teams_csrf=x%3Dy; b=2", "agent_teams_csrf")).toBe("x=y");
    expect(readCookie("a=1", "nope")).toBe("");
    expect(isWrite("post")).toBe(true);
    expect(isWrite("GET")).toBe(false);
  });
  it("turns error bodies into ApiError", () => {
    const e = toApiError(422, { detail: "校验失败", code: "invalid", fields: [{ path: "name", message: "必填" }] });
    expect([e.status, e.message, e.code, e.fields.length]).toEqual([422, "校验失败：必填", "invalid", 1]);
    expect(toApiError(500, undefined, "Server Error").message).toBe("Server Error");
    expect(toApiError(502, "bad gateway").message).toBe("bad gateway");
    expect(toApiError(503, null).message).toBe("请求失败（503）");
  });
});

describe("sse helpers", () => {
  it("streamUrl adds last_event_id and params", () => {
    expect(streamUrl("/api/x", null)).toBe("/api/x");
    expect(streamUrl("/api/x", "42", { topics: "status" })).toBe("/api/x?topics=status&last_event_id=42");
    expect(streamUrl("/api/x?a=1", "7")).toBe("/api/x?a=1&last_event_id=7");
  });
  it("backoff doubles up to 30 s", () => {
    expect([0, 1, 2, 10].map(backoff)).toEqual([1000, 2000, 4000, 30000]);
  });
});

function seeded() {
  const qc = new QueryClient();
  qc.setQueryData(keys.threads("p1"), [thread(12, { members: [{ id: "e1", name: "dev-1", tier: "member", role: "dev", title: "", runtime: "claude", node: null, look: { color: "", outfit: "", accessory: "", hair: null }, status: null }] })]);
  qc.setQueryData(keys.coordinator("p1"), { conversation_id: "c0" });
  return qc;
}

describe("events → cache", () => {
  it("upsert replaces in place or appends", () => {
    expect(upsert([{ id: "a", v: 1 }], { id: "a", v: 2 })).toEqual([{ id: "a", v: 2 }]);
    expect(upsert(undefined, { id: "b" })).toEqual([{ id: "b" }]);
  });
  it("appends messages to the newest page once", () => {
    const data: MessagePages = { pages: [{ items: [message("1")], next_cursor: "c" }, { items: [message("0")], next_cursor: null }], pageParams: [undefined, "c"] };
    const next = appendMessage(data, message("2"))!;
    expect(flattenMessages(next).map((m) => m.id)).toEqual(["0", "1", "2"]);
    expect(appendMessage(next, message("2"))).toBe(next);
    expect(appendMessage(undefined, message("3"))).toBeUndefined();
  });
  it("thread events upsert the list sorted by number", () => {
    const qc = seeded();
    applyProjectEvent(qc, "p1", "thread", { thread: thread(13) });
    applyProjectEvent(qc, "p1", "thread", { thread: thread(12, { title: "新" }) });
    const list = qc.getQueryData<ThreadView[]>(keys.threads("p1"))!;
    expect(list.map((t) => [t.no, t.title])).toEqual([[13, "T13"], [12, "新"]]);
  });
  it("questions go to the thread's or the coordinator's conversation", () => {
    const qc = seeded();
    qc.setQueryData(keys.questions("t12"), []);
    qc.setQueryData(keys.questions("c0"), []);
    applyProjectEvent(qc, "p1", "question", { thread_no: 12, question: question("q1") });
    applyProjectEvent(qc, "p1", "question", { question: question("q2", { thread_no: null }) });
    expect(qc.getQueryData<QuestionView[]>(keys.questions("t12"))!.map((q) => q.id)).toEqual(["q1"]);
    expect(qc.getQueryData<QuestionView[]>(keys.questions("c0"))!.map((q) => q.id)).toEqual(["q2"]);
  });
  it("tasks, main tasks, memory, ci, merge", () => {
    const qc = seeded();
    qc.setQueryData(keys.tasks("t12"), [task("a")]);
    applyProjectEvent(qc, "p1", "task", { thread_no: 12, task: task("a", { status: "done" }) });
    expect(qc.getQueryData<TaskView[]>(keys.tasks("t12"))![0].status).toBe("done");
    applyProjectEvent(qc, "p1", "main_task", { thread_no: 12, main_tasks: [{ id: "m", pos: 1, title: "x", status: "doing", auto_done: false, tasks_total: 1, tasks_done: 0 }] });
    expect(qc.getQueryData<ThreadView[]>(keys.threads("p1"))![0].main_tasks).toHaveLength(1);
    qc.setQueryData(keys.memory("p1"), [{ id: "m1", text: "a", source: "user", pinned: false, created_at: "" }]);
    applyProjectEvent(qc, "p1", "memory", { op: "delete", item: { id: "m1" } });
    expect(qc.getQueryData(keys.memory("p1"))).toEqual([]);
    qc.setQueryData<Partial<OfficeSnapshot>>(keys.office("p1"), { threads: [], merge_queue: [], ci: {} });
    applyProjectEvent(qc, "p1", "ci", { thread_no: 12, checks: [{ name: "verify" }] });
    applyProjectEvent(qc, "p1", "merge", { thread_no: 12, entry: { id: "mq" } });
    const office = qc.getQueryData<OfficeSnapshot>(keys.office("p1"))!;
    expect(office.ci["12"]).toHaveLength(1);
    expect(office.merge_queue).toHaveLength(1);
    expect(qc.getQueryData(keys.checks("t12"))).toHaveLength(1);
  });
  it("status frames update members and the office log; layout bumps the tick", () => {
    const qc = seeded();
    const frame: StatusFrame = { seq: 5, at: "", employee: { id: "e1" }, status: { state: "typing" } };
    applyProjectEvent(qc, "p1", "status", frame);
    expect(qc.getQueryData<ThreadView[]>(keys.threads("p1"))![0].members[0].status?.state).toBe("typing");
    expect(qc.getQueryData<StatusFrame[]>(statusLogKey("p1"))).toHaveLength(1);
    applyProjectEvent(qc, "p1", "layout", {});
    applyProjectEvent(qc, "p1", "layout", {});
    expect(qc.getQueryData(layoutTickKey("p1"))).toBe(2);
    applyProjectEvent(qc, "p1", "unknown", {});
  });
  it("user events invalidate and return notices", () => {
    const qc = new QueryClient();
    expect(applyUserEvent(qc, "notice", { text: "你好" })).toBe("你好");
    expect(applyUserEvent(qc, "project", {})).toBeUndefined();
  });
});

describe("chat blocks", () => {
  it("attaches opened-thread system lines to the speaker before them", () => {
    const blocks = chatBlocks([
      message("1", { role: "user" }),
      message("2", { meta: { opens_thread: 12 } }),
      message("3", { role: "system", meta: { opens_thread: 13 } }),
      message("4", { role: "error" }),
    ]);
    expect(blocks.map((b) => b.kind)).toEqual(["user", "speaker", "line"]);
    expect(blocks[1].kind === "speaker" && blocks[1].opens).toEqual([12, 13]);
  });
});

describe("office mapping", () => {
  it("filters status frames after the snapshot and sorts by seq", () => {
    const frames: StatusFrame[] = [3, 1, 2].map((seq) => ({ seq, at: "", employee: seq === 3 ? "e3" : { id: `e${seq}` }, status: { state: "idle" } }));
    expect(toOfficeEvents(frames, 1).map((e) => [e.seq, e.employee])).toEqual([[2, "e2"], [3, "e3"]]);
  });
  it("replay rows keep only status", () => {
    expect(replayEvents([{ id: 1, at: "", type: "status", data: { employee: { id: "a" }, status: { state: "idle" } } }, { id: 2, at: "", type: "task", data: {} }])).toHaveLength(1);
  });
  it("checks follow the focus, else the latest thread with checks", () => {
    const ci = { "11": [{ name: "a", status: "", conclusion: null, url: "", summary: "" }], "12": [] };
    expect(pickChecks(ci, null).map((c) => c.name)).toEqual(["a"]);
    expect(pickChecks(ci, 12)).toEqual([]);
  });
  it("widget data and board summary", () => {
    const snap = { board: { claimable: 3 }, nodes: [{ id: "n", name: "rc", online: true }], merge_queue: [], ci: {} } as unknown as OfficeSnapshot;
    expect(toWidgetData(snap, null).nodes![0]).toMatchObject({ capacity: 0, active: 0 });
    const threads = [thread(12, { counts: { ...thread(1).counts, tasks_done: 1, tasks_total: 4 } }), thread(13, { status: "done" })];
    expect(boardSummary(threads, null)).toEqual({ title: "全项目 · 1 个讨论串", count: "小任务 1 / 4", pct: "25%" });
    expect(boardSummary(threads, 12).title).toBe("#12 T12");
    const items = boardItems([thread(11, { status: "review" })], [question("q", { kind: "approval" })], 2, null);
    expect(items.map((i) => i.state)).toEqual(["可领", "等你", "待验收"]);
  });
});

describe("roles", () => {
  const base = newRole([], "#000");
  it("keys and defaults", () => {
    expect(nextRoleKey([{ role: "role-1" }])).toBe("role-2");
    expect(isReadOnly({ read_only: null, gate: "review" })).toBe(true);
    expect(isReadOnly({ read_only: false, gate: "review" })).toBe(false);
    expect(previewTags({ tier: "lead", role: "lead" })).toEqual(["lead-1", "lead-2"]);
    expect(countText({ tier: "member", count: 2, gate: "" })).toBe("每个讨论串 2 个");
  });
  it("rule and skip texts", () => {
    expect(ruleText({ ...base, runtime: "codex" })).toContain("workspace-write");
    expect(ruleText({ ...base, gate: "decision" })).toContain("只读");
    expect(skipHint({ skip_permissions: false, runtime: "claude" })).toContain("bypassPermissions");
  });
  it("validation mirrors PUT /api/roles", () => {
    const ok = [{ ...base, tier: "coordinator" as const, role: "coordinator" }, { ...base, tier: "lead" as const, role: "lead" }, { ...base, role: "dev" }];
    expect(roleProblems(ok)).toEqual([]);
    expect(roleProblems([...ok, { ...base, role: "dev" }, { ...base, role: "Bad", count: 9 }]).length).toBe(3);
    expect(roleProblems([ok[2]])).toContain("需要恰好一个组长");
  });
  it("team chips and summary", () => {
    const list = [{ ...base, tier: "lead" as const, title: "组长" }, { ...base, title: "开发", count: 2 }, { ...base, tier: "coordinator" as const }];
    expect(teamChips(list).map((c) => c.name)).toEqual(["组长", "开发"]);
    expect(teamSummary(list)).toBe("组长 + 2 开发");
  });
});

describe("page helpers", () => {
  it("repos", () => {
    expect(normalizeRepo("https://github.com/a/b.git")).toBe("a/b");
    expect(normalizeRepo(" a/b ")).toBe("a/b");
    expect(repoPayload({ git_connection_id: "", repo: "a/b", branch: "x" })).toEqual({ repo: null, branch: "", git_connection_id: null });
    expect(repoPayload({ git_connection_id: "g", repo: "a/b", branch: "main" })).toEqual({ repo: "a/b", branch: "main", git_connection_id: "g" });
    const r = (full_name: string, used: boolean, pushed: string) => ({ full_name, used_by_projects: used ? [{ id: "p", name: "p" }] : [], pushed_at: pushed, private: false, default_branch: "", description: "", html_url: "" });
    expect(sortRepos([r("a", false, "2026-02-01"), r("b", true, "2025-01-01"), r("c", false, "2026-03-01")]).map((x) => x.full_name)).toEqual(["b", "c", "a"]);
  });
  it("projects", () => {
    const p = (id: string, extra: object) => ({ id, name: id, description: "", repo: null, pinned: false, archived: false, updated_at: "2026-01-01T00:00:00Z", ...extra }) as never;
    expect(recentProject([p("a", { updated_at: "2026-02-01T00:00:00Z" }), p("b", { pinned: true }), p("c", { archived: true })])!).toMatchObject({ id: "b" });
    expect(recentProject([p("c", { archived: true })])).toMatchObject({ id: "c" });
    expect(matchesProject({ name: "Agent", description: "", repo: "x/sup2api" }, "SUP")).toBe(true);
    expect(matchesProject({ name: "Agent", description: "", repo: null }, "zz")).toBe(false);
  });
  it("overview filters", () => {
    const list = [thread(1), thread(2, { status: "review" }), thread(3, { status: "done" }), thread(4, { status: "archived" })];
    expect(filterThreads(list, "all")).toHaveLength(3);
    expect(filterThreads(list, "review").map((t) => t.no)).toEqual([2]);
  });
  it("nodes, memory, deliverables", () => {
    expect(runtimeLine("codex", undefined).ver).toBe("未安装");
    expect(runtimeLine("claude", { cli: "2.1", auth: "none" }).login).toBe("未登录");
    expect(runtimeLine("claude", { cli: "2.1", auth: "subscription" }).ok).toBe(true);
    const n = (online: boolean, active: number, max: number | null, cap?: number) => ({ online, active_sessions: active, max_active_sessions: max, capacity: { max_active_sessions: cap }, queued: 1 }) as never;
    expect(summarizeNodes([n(true, 2, 4), n(true, 1, null, 3), n(false, 0, 4)])).toEqual({ total: 3, online: 2, active: 3, slots: 7, queued: 2 });
    expect(memorySource({ source: "thread:12" })).toBe("#12 总结");
    expect(memorySource({ source: "user" })).toBe("你填写");
    expect(deliverableHref("p", { id: "d", kind: "design", url: "u" })).toEqual({ to: "/projects/p/deliverables/d" });
    expect(deliverableHref("p", { id: "d", kind: "pr", url: "u" })).toEqual({ href: "u" });
  });
});
