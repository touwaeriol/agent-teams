// Ported from web/src/__tests__/logic.test.ts (client, events, chat parts): the same API and
// cache logic must behave identically on vue-query.
import { QueryClient } from "@tanstack/vue-query";
import { describe, expect, it, vi } from "vitest";
import { isWrite, readCookie, toApiError } from "@/api/client";
import { appendMessage, applyProjectEvent, applyUserEvent, layoutTickKey, OFFICE_REFRESH_MS, statusLogKey, upsert, type StatusFrame } from "@/api/events";
import { flattenMessages, keys, type MessagePages } from "@/api/queries";
import { backoff, streamUrl } from "@/api/sse";
import type { OfficeSnapshot, QuestionView, TaskView, ThreadView } from "@/api/views";
import { summarizeNodes } from "@/components/shell/nodes";
import { matchesProject } from "@/components/shell/projects";
import { chatBlocks, interleave, threadChat, threadTalk } from "@/lib/chat";
import { recentProject } from "@/pages/projects";
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
    qc.setQueryData(keys.memory("p1"), [{ name: "MEMORY.md", description: "", size: 1, updated_by: "user", updated_at: "" }]);
    qc.setQueryData(keys.memoryFile("p1", "MEMORY.md"), { name: "MEMORY.md", content: "x" });
    applyProjectEvent(qc, "p1", "memory", { op: "write", name: "MEMORY.md" });
    expect(qc.getQueryState(keys.memory("p1"))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(keys.memoryFile("p1", "MEMORY.md"))?.isInvalidated).toBe(true);
    qc.setQueryData<Partial<OfficeSnapshot>>(keys.office("p1"), { threads: [], merge_queue: [], ci: {} });
    applyProjectEvent(qc, "p1", "ci", { thread_no: 12, checks: [{ name: "verify" }] });
    applyProjectEvent(qc, "p1", "merge", { thread_no: 12, entry: { id: "mq" } });
    const office = qc.getQueryData<OfficeSnapshot>(keys.office("p1"))!;
    expect(office.ci["12"]).toHaveLength(1);
    expect(office.merge_queue).toHaveLength(1);
    expect(qc.getQueryData(keys.checks("t12"))).toHaveLength(1);
  });
  it("task events refetch the office once per window, per project", () => {
    vi.useFakeTimers();
    try {
      const qc = seeded();
      const spy = vi.spyOn(qc, "invalidateQueries");
      const officeCalls = (pid: string) => spy.mock.calls.filter(([f]) => JSON.stringify(f?.queryKey) === JSON.stringify(keys.office(pid))).length;
      for (let i = 0; i < 20; i++) applyProjectEvent(qc, "p1", "task", { thread_no: 12, task: task(`t${i}`) });
      applyProjectEvent(qc, "p2", "node", {});
      expect(officeCalls("p1")).toBe(0);
      vi.advanceTimersByTime(OFFICE_REFRESH_MS);
      expect(officeCalls("p1")).toBe(1);
      expect(officeCalls("p2")).toBe(1);
      applyProjectEvent(qc, "p1", "task", { thread_no: 12, task: task("late") });
      vi.advanceTimersByTime(OFFICE_REFRESH_MS);
      expect(officeCalls("p1")).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });
  it("status frames update members and the office log; layout bumps the tick", () => {
    const qc = seeded();
    const frame: StatusFrame = { seq: 5, at: "", employee: { id: "e1" }, status: { state: "typing" } };
    applyProjectEvent(qc, "p1", "status", frame);
    expect(qc.getQueryData<ThreadView[]>(keys.threads("p1"))![0].members[0].status?.state).toBe("typing");
    expect(qc.getQueryData<StatusFrame[]>(statusLogKey("p1"))).toHaveLength(1);
    const member = () => qc.getQueryData<ThreadView[]>(keys.threads("p1"))![0].members[0];
    applyProjectEvent(qc, "p1", "status", { ...frame, seq: 6, employee: { id: "e1", thread_no: 3 }, status: { state: "typing", task_id: "t1" } });
    expect(member()).toMatchObject({ thread_no: 3, busy_elsewhere: true });
    applyProjectEvent(qc, "p1", "status", { ...frame, seq: 7, employee: { id: "e1", thread_no: 3 }, status: { state: "idle" } });
    expect(member()).toMatchObject({ thread_no: 3, busy_elsewhere: false });
    applyProjectEvent(qc, "p1", "status", { ...frame, seq: 8, employee: { id: "e1", thread_no: 12 }, status: { state: "typing", task_id: "t2" } });
    expect(member().busy_elsewhere).toBe(false);
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
  it("thread chat keeps only you, the lead and errors", () => {
    const roles = ["user", "leader", "employee", "system", "error"] as const;
    expect(roles.filter((role) => threadTalk(message("x", { role })))).toEqual(["user", "leader", "error"]);
  });
  it("thread chat hides platform checks and the lead's short reply to them", () => {
    const check = (id: string) => message(id, { role: "system", kind: "note", content: "（平台）节点守护进程重启后检查", meta: { stalled: true } });
    const shown = threadChat([
      message("1", { role: "user", content: "我手动关闭线程" }),
      message("2", { content: "好的" }),
      check("3"),
      message("4", { content: "请在页面上关掉它" }),
      message("5", { content: "后面的汇报照常显示" }),
      check("6"),
      message("7", { role: "user", content: "还在吗" }),
      message("8", { content: "在" }),
      check("9"),
      message("10", { content: "全部完成。".repeat(60) }),
    ]).map((m) => m.id);
    expect(shown).toEqual(["1", "2", "5", "7", "8", "10"]);
  });
  it("puts cards between the messages at the time they were asked", () => {
    const at = (min: number) => new Date(Date.UTC(2026, 10, 15, 10, min)).toISOString();
    const blocks = chatBlocks([message("1", { role: "user", created_at: at(1) }), message("2", { created_at: at(5) })]);
    const order = interleave(blocks, [
      { key: "late", at: at(9), node: null },
      { key: "mid", at: at(3), node: null },
      { key: "tie", at: at(5), node: null },
    ]).map((it) => (it.kind === "block" ? it.block.message.id : it.key));
    expect(order).toEqual(["1", "mid", "2", "tie", "late"]);
  });
});

describe("shell", () => {
  it("projects", () => {
    const p = (id: string, extra: object) => ({ id, name: id, description: "", repos: [], pinned: false, archived: false, updated_at: "2026-01-01T00:00:00Z", ...extra }) as never;
    expect(recentProject([p("a", { updated_at: "2026-02-01T00:00:00Z" }), p("b", { pinned: true }), p("c", { archived: true })])!).toMatchObject({ id: "b" });
    expect(recentProject([p("c", { archived: true })])).toMatchObject({ id: "c" });
    expect(matchesProject({ name: "Agent", description: "", repos: [{ repo: "x/web", git_connection_id: "g" }, { repo: "x/sup2api", git_connection_id: "g" }] }, "SUP")).toBe(true);
    expect(matchesProject({ name: "Agent", description: "", repos: [] }, "zz")).toBe(false);
  });
  it("nodes", () => {
    const n = (online: boolean, active: number, max: number | null, cap?: number) => ({ online, active_sessions: active, max_active_sessions: max, capacity: { max_active_sessions: cap }, queued: 1 }) as never;
    expect(summarizeNodes([n(true, 2, 4), n(true, 1, null, 3), n(false, 0, 4)])).toEqual({ total: 3, online: 2, active: 3, slots: 7, queued: 2 });
  });
});
