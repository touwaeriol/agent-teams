// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DeliverableView, EmployeeBrief } from "@/api/views";
import {
  deliverableCount,
  filterThreads,
  foldedKey,
  groupThreads,
  isCompactGroup,
  lastActivity,
  listGroupOf,
  loadFolded,
  mainProgress,
  NO_FILTER,
  rowLine,
  rowMark,
  saveFolded,
  shortAgo,
  threadMark,
  threadNodes,
} from "@/lib/threadList";
import { question, thread } from "./fixtures";

const counts = (c: Partial<ReturnType<typeof thread>["counts"]>) => ({ ...thread(1).counts, ...c });
const member = (state: string): EmployeeBrief => ({ id: state, name: `m-${state}`, tier: "member", status: { state } }) as unknown as EmployeeBrief;
const deliverable = (id: string): DeliverableView => ({ id, project_id: "p1", thread_no: 1, kind: "design", title: id, url: "", detail: {}, published_by: "dev", created_at: "", updated_at: "" });

beforeEach(() => window.localStorage.clear());

describe("listGroupOf", () => {
  it("waiting on the user wins over working", () => {
    expect(listGroupOf(thread(1, { status: "review" }))).toBe("waiting");
    expect(listGroupOf(thread(1, { members: [member("typing")], counts: counts({ approvals_open: 1, waiting: 1 }) }))).toBe("waiting");
    expect(listGroupOf(thread(1, { members: [member("typing")] }), new Map([[1, question("q", { thread_no: 1 })]]))).toBe("waiting");
  });
  it("working, open small tasks, queued or waiting on checks is 进行中; paused is 已暂停 (the old 空闲 group)", () => {
    expect(listGroupOf(thread(1, { members: [member("typing")] }))).toBe("active");
    expect(listGroupOf(thread(1, { status: "queued" }))).toBe("active");
    expect(listGroupOf(thread(1, { members: [member("idle")], counts: counts({ tasks_open: 1 }) }))).toBe("active");
    expect(listGroupOf(thread(1, { status: "review", merge_policy: "pr" }))).toBe("active");
    expect(listGroupOf(thread(1, { status: "review", merge_policy: "auto" }))).toBe("active");
    expect(listGroupOf(thread(1, { status: "review", merge_policy: "auto", merge_error: "冲突" }))).toBe("waiting");
    expect(listGroupOf(thread(1, { status: "paused", members: [member("typing")] }))).toBe("idle");
  });
  it("running with nobody working and no small task left: the lead replied, 等你处理", () => {
    expect(listGroupOf(thread(1, { members: [member("idle")] }))).toBe("waiting");
    // A shared member typing for another thread does not make this one active.
    expect(listGroupOf(thread(1, { members: [{ ...member("typing"), busy_elsewhere: true, thread_no: 2 }] }))).toBe("waiting");
    // ... but its task here still waits for it: 进行中.
    expect(listGroupOf(thread(1, { members: [{ ...member("typing"), busy_elsewhere: true, thread_no: 2 }], counts: counts({ tasks_open: 1 }) }))).toBe("active");
    // Older servers without tasks_open: everything not done counts as open.
    const old = counts({ tasks_total: 2, tasks_done: 1 });
    delete old.tasks_open;
    expect(listGroupOf(thread(1, { counts: old }))).toBe("active");
  });
  it("done and failed are 已完成, archived its own", () => {
    expect(listGroupOf(thread(1, { status: "done" }))).toBe("done");
    expect(listGroupOf(thread(1, { status: "failed" }))).toBe("done");
    expect(listGroupOf(thread(1, { status: "archived" }))).toBe("archived");
  });
});

describe("groupThreads", () => {
  it("puts each thread in one group in a fixed order, hides empty groups, newest activity first", () => {
    const groups = groupThreads([
      thread(1, { status: "done", updated_at: "2026-11-01T00:00:00Z" }),
      thread(2, { status: "done", updated_at: "2026-11-01T00:00:00Z", last_activity_at: "2026-11-03T00:00:00Z" }),
      thread(3, { status: "archived" }),
      thread(4, { status: "review" }),
      thread(5, { status: "paused" }),
    ]);
    expect(groups.map((g) => [g.id, g.label, g.items.map((t) => t.no)])).toEqual([
      ["waiting", "等你处理", [4]],
      ["idle", "已暂停", [5]],
      ["done", "已完成", [2, 1]],
      ["archived", "已归档", [3]],
    ]);
  });
  it("row marks follow the group and failure", () => {
    expect(rowMark(thread(1, { status: "failed" }), "done")).toBe("failed");
    expect(rowMark(thread(1, { status: "done" }), "done")).toBe("done");
    expect(rowMark(thread(1, { status: "queued" }), "active")).toBe("queued");
    expect(rowMark(thread(1), "active")).toBe("working");
    expect(rowMark(thread(1), "waiting")).toBe("wait");
    expect(rowMark(thread(1, { status: "paused" }), "idle")).toBe("paused");
  });
});

describe("rows", () => {
  it("lastActivity uses the server's time, else the later of update and lead reply", () => {
    expect(lastActivity(thread(1, { last_activity_at: "2026-11-15T00:00:00Z" }))).toBe("2026-11-15T00:00:00Z");
    expect(lastActivity(thread(1, { lead_last: { text: "x", at: "2026-11-15T00:00:00Z" } }))).toBe("2026-11-15T00:00:00Z");
    expect(lastActivity(thread(1, { lead_last: { text: "x", at: "2026-11-01T00:00:00Z" } }))).toBe("2026-11-14T10:00:00Z");
  });
  it("rowLine: waiting reason, else the lead's first line, the summary, the brief", () => {
    expect(rowLine(thread(1), question("q", { asker: "lead-1", text: "选哪个" }))).toBe("组长问：选哪个");
    expect(rowLine(thread(1, { counts: counts({ approvals_open: 2, waiting: 2 }) }))).toBe("等你审批 2 条命令");
    const going = counts({ tasks_open: 1, waiting: 0 });
    expect(rowLine(thread(1, { members: [member("typing")], lead_last: { text: "\n第一行\n第二行", at: "" }, summary: "s" }))).toBe("第一行");
    expect(rowLine(thread(1, { members: [member("typing")], summary: "总结" }))).toBe("总结");
    expect(rowLine(thread(1, { members: [member("typing")] }))).toBe("brief");
    // Nobody working, small tasks open: says so.
    expect(rowLine(thread(1, { counts: going, lead_last: { text: "派好了", at: "" } }))).toBe("等成员领取任务");
    // The lead replied and stopped.
    expect(rowLine(thread(1, { lead_last: { text: "\n做完了\n细节", at: "" } }))).toBe("组长已回复：做完了");
    expect(rowLine(thread(1))).toBe("组长已回复，等你回复");
  });
  it("shortAgo", () => {
    const now = Date.parse("2026-11-16T12:00:00Z");
    expect(shortAgo("2026-11-16T11:59:40Z", now)).toBe("刚刚");
    expect(shortAgo("2026-11-16T11:09:00Z", now)).toBe("51分钟");
    expect(shortAgo("2026-11-16T09:00:00Z", now)).toBe("3小时");
    expect(shortAgo("2026-11-15T11:00:00Z", now)).toBe("1天");
    expect(shortAgo("2026-09-02T12:00:00Z", now)).toMatch(/^9月[12]日$/);
    expect(shortAgo("bad", now)).toBe("");
  });
});

describe("filterThreads", () => {
  const list = [
    thread(1, { title: "部署前端", status: "done", latest_deliverable: deliverable("d1") }),
    thread(2, { title: "接口", summary: "已上线 Deploy", members: [member("typing")] }),
    thread(3, { title: "其它", lead_last: { text: "部署说明", at: "" } }),
  ];
  const nos = (f: Partial<typeof NO_FILTER>) => filterThreads(list, { ...NO_FILTER, ...f }).map((t) => t.no);
  it("searches number, title, summary and the lead's message, ignoring case", () => {
    expect(nos({ q: "部署" })).toEqual([1, 3]);
    expect(nos({ q: "deploy" })).toEqual([2]);
    expect(nos({ q: "#2" })).toEqual([2]);
    expect(nos({ q: "  " })).toEqual([1, 2, 3]);
  });
  it("filters by group and by having a deliverable", () => {
    expect(nos({ groups: ["active", "done"] })).toEqual([1, 2]);
    expect(nos({ withDeliverable: true })).toEqual([1]);
    expect(nos({ groups: ["active"], withDeliverable: true })).toEqual([]);
  });
});

describe("folded sections", () => {
  it("archived starts folded; changes are saved per user and read back", () => {
    expect(loadFolded("u1")).toEqual({ archived: true });
    saveFolded("u1", { archived: false, done: true, deliverables: true });
    expect(loadFolded("u1")).toEqual({ archived: false, done: true, deliverables: true });
    expect(loadFolded("u2")).toEqual({ archived: true });
    saveFolded("u1", { archived: true, done: false });
    expect(window.localStorage.getItem(foldedKey("u1"))).toBeNull();
  });
  it("survives broken or unavailable storage", () => {
    window.localStorage.setItem(foldedKey("u1"), "{not json");
    expect(loadFolded("u1")).toEqual({ archived: true });
    window.localStorage.setItem(foldedKey("u1"), JSON.stringify({ done: "yes", idle: true }));
    expect(loadFolded("u1")).toEqual({ archived: true, idle: true });
    const get = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(loadFolded("u1")).toEqual({ archived: true });
    expect(() => saveFolded("u1", { done: true })).not.toThrow();
    get.mockRestore();
    set.mockRestore();
  });
});

describe("list layout (Claude app style)", () => {
  it("orders 等你处理 → 进行中 → 已暂停 → 已完成 → 已归档 whatever the input order", () => {
    const ids = groupThreads([
      thread(1, { status: "archived" }),
      thread(2, { status: "done" }),
      thread(3, { status: "paused" }),
      thread(4, { members: [member("typing")] }),
      thread(5, { status: "review" }),
    ]).map((g) => g.id);
    expect(ids).toEqual(["waiting", "active", "idle", "done", "archived"]);
  });
  it("已暂停, 已完成 and 已归档 rows are compact; 等你处理 and 进行中 stay rich", () => {
    expect((["waiting", "active", "idle", "done", "archived"] as const).map(isCompactGroup)).toEqual([false, false, true, true, true]);
  });
  it("the header mark follows the list: spinner working, orange waiting, dashed paused, ✓ done, ✕ failed", () => {
    expect(threadMark(thread(1, { members: [member("typing")] }))).toBe("working");
    expect(threadMark(thread(1, { status: "review" }))).toBe("wait");
    expect(threadMark(thread(1, { members: [member("idle")] }))).toBe("wait");
    expect(threadMark(thread(1, { status: "paused" }))).toBe("paused");
    expect(threadMark(thread(1, { status: "done" }))).toBe("done");
    expect(threadMark(thread(1, { status: "failed" }))).toBe("failed");
    expect(threadMark(thread(1, { members: [member("typing")] }), new Map([[1, question("q", { thread_no: 1 })]]))).toBe("wait");
  });
});

describe("mainProgress", () => {
  const step = (status: "todo" | "doing" | "done") => ({ id: status + Math.random(), pos: 1, title: "s", status, auto_done: false, tasks_total: 0, tasks_done: 0 });
  it("counts done main tasks from the plan, else the counters; none without a plan", () => {
    expect(mainProgress(thread(1, { main_tasks: [step("done"), step("done"), step("doing"), step("todo"), step("todo"), step("todo"), step("todo")] }))).toEqual({ done: 2, total: 7, label: "2/7" });
    expect(mainProgress(thread(1, { counts: counts({ main_total: 4, main_done: 1 }) }))?.label).toBe("1/4");
    expect(mainProgress(thread(1))).toBeNull();
  });
});

describe("deliverableCount", () => {
  it("uses the server's count, falling back to the newest deliverable", () => {
    expect(deliverableCount(thread(1, { counts: counts({ deliverables: 3 }) }))).toBe(3);
    expect(deliverableCount(thread(1, { latest_deliverable: deliverable("d") }))).toBe(1);
    expect(deliverableCount(thread(1))).toBe(0);
  });
});

describe("threadNodes", () => {
  const on = (id: string, online: boolean) => ({ ...member("idle"), id: `e-${id}-${online}`, node: { id, name: id, online } }) as EmployeeBrief;
  it("dedupes the members' nodes and says whether they are online", () => {
    expect(threadNodes(thread(1, { members: [on("rc", true), on("rc", true), on("cvp", true)] }))).toMatchObject({ state: "online", label: "rc 在线 · cvp 在线" });
    expect(threadNodes(thread(1, { members: [on("rc", true), on("cvp", false)] })).state).toBe("partial");
    expect(threadNodes(thread(1, { members: [on("cvp", false)] })).state).toBe("offline");
    expect(threadNodes(thread(1, { members: [member("idle")] }))).toMatchObject({ state: "none", label: "还没有分配节点" });
  });
});
