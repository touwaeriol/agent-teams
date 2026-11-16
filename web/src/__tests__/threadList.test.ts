// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DeliverableView, EmployeeBrief } from "@/api/views";
import { filterThreads, foldedKey, groupThreads, lastActivity, listGroupOf, loadFolded, NO_FILTER, rowLine, rowMark, saveFolded, shortAgo } from "@/lib/threadList";
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
  it("working or queued is 进行中, running/paused with nobody working is 空闲", () => {
    expect(listGroupOf(thread(1, { members: [member("typing")] }))).toBe("active");
    expect(listGroupOf(thread(1, { status: "queued" }))).toBe("active");
    expect(listGroupOf(thread(1, { members: [member("idle")] }))).toBe("idle");
    expect(listGroupOf(thread(1, { status: "paused", members: [member("typing")] }))).toBe("idle");
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
      thread(5, { members: [member("idle")] }),
    ]);
    expect(groups.map((g) => [g.id, g.label, g.items.map((t) => t.no)])).toEqual([
      ["waiting", "等你处理", [4]],
      ["idle", "空闲", [5]],
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
    expect(rowMark(thread(1), "idle")).toBe("idle");
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
    expect(rowLine(thread(1, { lead_last: { text: "\n第一行\n第二行", at: "" }, summary: "s" }))).toBe("第一行");
    expect(rowLine(thread(1, { summary: "总结" }))).toBe("总结");
    expect(rowLine(thread(1))).toBe("brief");
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
