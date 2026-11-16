// Pure logic of the ported screens: thread panel / page, its views (任务 / 交付物 / 成员), project
// home, library, deliverable viewer and office page. Mirrors web/src/__tests__ (threads, logic, design).
import { describe, expect, it } from "vitest";
import type { EmployeeBrief, OfficeSnapshot, ThreadView } from "@/api/views";
import { saveView, storedView, typeName } from "@/components/app/deliverables/browser";
import { detailViewOf, threadClosed, typingLine } from "@/components/app/thread/detail";
import { openPrMode, prLines, queueLines } from "@/components/app/threadviews/delivery";
import { memberStatusLine } from "@/components/app/threadviews/members";
import { fileUrl, formatBytes, frameSandbox, isHosted, markdownUrl, renderLabel } from "@/lib/artifacts";
import { deliverableHref } from "@/lib/library";
import { approvalText, pickedVersion, versionLabel, viewerSubtitle } from "@/pages/design/viewer";
import { threadPath } from "@/pages/main/logic";
import { actionTarget, boardItems, hhmmss, lastHour, liveThreads, recentEvents, scopes, whoColor } from "@/pages/office/logic";
import { cardLook, filterThreads, prText, reviewText, teamSummary, waitingFrom, waitingText } from "@/pages/overview/logic";
import { question, thread } from "./fixtures";

function member(name: string, state: string | null, patch: Partial<EmployeeBrief> = {}): EmployeeBrief {
  return {
    id: `e-${name}`, name, tier: name.startsWith("lead") ? "lead" : "member", role: "dev", title: "", runtime: "claude", node: null,
    look: { color: "#000", outfit: "tee", accessory: "none", hair: null }, status: state ? { state, bubble: "", since: "2026-11-14T10:00:00Z" } : null, ...patch,
  } as EmployeeBrief;
}

describe("thread panel", () => {
  it("deep links threads and the views over the chat", () => {
    expect(threadPath("p1", 12, "deliverables")).toBe("/projects/p1/threads/12/deliverables");
    expect(detailViewOf("members")).toBe("members");
    expect(detailViewOf("progress")).toBeNull();
    expect(detailViewOf("full")).toBeNull();
    expect(detailViewOf(undefined)).toBeNull();
  });
  it("shows one quiet line while the thread is going", () => {
    const t = (status: ThreadView["status"], lead: string | null) => ({ status, members: [member("lead-1", lead)] });
    expect(typingLine(t("running", "typing"))).toEqual({ text: "组长正在回复…", spin: true });
    expect(typingLine(t("running", "idle"))).toEqual({ text: "讨论串进行中", spin: true });
    expect(typingLine(t("paused", "idle"))).toEqual({ text: "讨论串已暂停", spin: false });
    expect(typingLine(t("queued", null))).toEqual({ text: "排队中，等节点空出来", spin: true });
    expect(typingLine(t("waiting", "idle"))).toBeNull();
    expect(typingLine(t("done", null))).toBeNull();
  });
  it("closes the message box once the thread has finished", () => {
    expect(threadClosed({ status: "done" })).toBe(true);
    expect(threadClosed({ status: "archived" })).toBe(true);
    expect(threadClosed({ status: "review" })).toBe(false);
  });
});

describe("交付物 view", () => {
  const main = (status: "todo" | "doing" | "done") => ({ id: "m", title: "x", status, pos: 0 }) as ThreadView["main_tasks"][number];
  it("offers 开 PR directly only when every main task is done", () => {
    expect(openPrMode(thread(1, { main_tasks: [main("done")] }))).toBe("open");
    expect(openPrMode(thread(1, { main_tasks: [main("done"), main("doing")] }))).toBe("confirm");
    expect(openPrMode(thread(1, { main_tasks: [] }))).toBe("confirm");
    expect(openPrMode(thread(1, { pr_url: "https://x/pull/1" }))).toBe("none");
    expect(openPrMode(thread(1, { status: "done" }))).toBe("none");
  });
  it("lists the merge queue and a placeholder PR line", () => {
    const t = thread(12, { branch: "thread/12" });
    const q = queueLines(t, [{ id: "mq", head_branch: "thread/12/dev-1", employee: "dev-1", task_id: "t1", error: "conflict", status: "merged", repo: "a/b" }]);
    expect(q[0]).toMatchObject({ branch: "a/b · thread/12/dev-1 → thread/12", what: "dev-1 · t1 · conflict" });
    expect(prLines(t)).toEqual([expect.objectContaining({ branch: "thread/12 → 基准分支", what: "讨论串 PR" })]);
    expect(prLines(thread(12, { branch: "", })).at(0)?.branch).toBe("讨论串分支 → 基准分支");
    const opened = prLines(thread(12, { pr_number: 41, pr_url: "https://github.com/a/b/pull/41", pr_state: "open" }));
    expect(opened[0]).toMatchObject({ what: "PR #41", href: "https://github.com/a/b/pull/41" });
  });
  it("member status line", () => {
    expect(memberStatusLine("在跑", { state: "running", bubble: "", since: "s", task_id: "t5" } as never, () => "2 分")).toBe("在跑 · 2 分 · 任务 t5");
    expect(memberStatusLine("空闲", null, () => "x")).toBe("空闲");
  });
});

describe("project home", () => {
  const list = [thread(1), thread(2, { status: "review" }), thread(3, { status: "done" }), thread(4, { status: "failed" }), thread(5, { status: "archived" })];
  it("filters by kind and hides archived", () => {
    expect(filterThreads(list, "all").map((t) => t.no)).toEqual([1, 2, 3, 4]);
    expect(filterThreads(list, "done").map((t) => t.no)).toEqual([3, 4]);
    expect(filterThreads(list, "review").map((t) => t.no)).toEqual([2]);
  });
  it("cards and the waiting box", () => {
    expect(teamSummary([{ tier: "lead", title: "组长", role: "lead", count: 1 }, { tier: "member", title: "开发", role: "dev", count: 2 }] as never)).toBe("组长 + 2 开发");
    expect(prText(thread(1))).toBe("PR 未创建");
    expect(prText(thread(1, { pr_number: 4, status: "done" }))).toBe("PR #4 已合并");
    expect(cardLook(thread(3, { status: "done" }))).toEqual({ tone: "#2E7D4F", label: "已完成" });
    expect(cardLook(thread(4, { status: "failed" })).label).toBe("失败");
    expect(waitingFrom(question("q", { kind: "approval" }))).toBe("#12 · 权限请求");
    expect(waitingFrom(question("q", { thread_no: null, asker: "lead-1" }))).toBe("统筹 · 组长提问");
    expect(waitingFrom(question("q", { asker: "dev-2" }))).toBe("#12 · dev-2 提问");
    expect(waitingText(question("q", { text: "选哪个？" }))).toBe("选哪个？");
    expect(reviewText({ pr_number: 41, title: "菜单" })).toBe("PR #41 菜单，等你验收");
  });
});

describe("library and viewer", () => {
  it("remembers list or grid", () => {
    expect(storedView()).toBe("list");
    expect(typeName("doc")).toBe("文档");
    expect(typeName("??")).toBe("全部类型");
    expect(() => saveView("grid")).not.toThrow();
  });
  it("never lets a page served from the console keep its origin", () => {
    expect(frameSandbox(false)).not.toContain("allow-same-origin");
    expect(frameSandbox(true)).toContain("allow-same-origin");
    const hosted = { id: "d", kind: "doc", url: "", detail: { hosted: true } };
    expect(isHosted(hosted)).toBe(true);
    expect(deliverableHref("p", hosted)).toEqual({ to: "/projects/p/deliverables/d" });
    expect(deliverableHref("p", { id: "d", kind: "doc", url: "https://x.test/a", detail: {} })).toEqual({ href: "https://x.test/a" });
  });
  it("builds file URLs and rewrites Markdown links safely", () => {
    const base = "/files/dl_1.2.99.abc/";
    expect(fileUrl(base, "assets/my logo.svg")).toBe("/files/dl_1.2.99.abc/assets/my%20logo.svg");
    const md = markdownUrl(base);
    expect(md("img/a.png")).toBe("/files/dl_1.2.99.abc/img/a.png");
    expect(md("../../api/me")).toBe("");
    expect(md("javascript:alert(1)")).toBe("");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(renderLabel("markdown")).toBe("Markdown 文档");
  });
  it("versions, subtitle and the approval message", () => {
    const files = { version: 2, versions: [{ version: 3 }, { version: 2 }] } as never;
    expect(pickedVersion(files, 3)).toBe(0);
    expect(pickedVersion(files, 2)).toBe(2);
    expect(versionLabel({ version: 3, published_by: "dev-2", created_at: new Date().toISOString() }, 3)).toMatch(/^第 3 版（最新） · dev-2 · /);
    expect(viewerSubtitle(undefined, "", new Date().toISOString())).not.toContain("做的");
    expect(viewerSubtitle({ no: 12, title: "T" }, "dev-2", new Date().toISOString())).toMatch(/^#12 T · dev-2 做的 · /);
    expect(approvalText("设计稿", "卡片", 3)).toBe("设计稿「卡片」第 3 版通过，按这个做。");
    expect(approvalText("文档", "说明", undefined)).toBe("文档「说明」通过，按这个做。");
  });
});

describe("office page", () => {
  it("board items in scope", () => {
    const items = boardItems([thread(11, { status: "review" })], [question("q", { kind: "approval" })], 2, null);
    expect(items.map((i) => i.state)).toEqual(["可领", "等你", "待验收"]);
    expect(boardItems([thread(11, { status: "review" })], [question("q")], 2, 11).map((i) => i.state)).toEqual(["待验收"]);
  });
  it("scopes, clicks and the event list", () => {
    const threads = liveThreads([thread(1), thread(2, { status: "done" }), thread(3, { status: "archived" })]);
    expect(scopes(threads)).toEqual([[null, "全部讨论串"], [1, "#1"]]);
    expect(actionTarget("p", { type: "openThread", threadNo: 4 }, null)).toBe("/projects/p/threads/4");
    expect(actionTarget("p", { type: "openTaskPool" }, null)).toBeNull();
    expect(actionTarget("p", { type: "openTaskPool" }, 7)).toBe("/projects/p/threads/7");
    expect(actionTarget("p", { type: "openNode", nodeId: "n" }, null)).toBe("/settings/nodes");
    const roster = new Map([["a", { thread_no: 1, tier: "member", role: "qa" }], ["b", { thread_no: null, tier: "coordinator", role: "x" }]] as never) as Map<string, OfficeSnapshot["employees"][number]>;
    const ev = (seq: number, employee: string) => ({ seq, employee, at: 0, status: { state: "idle" } }) as never;
    expect(recentEvents([ev(1, "a"), ev(2, "b"), ev(3, "a")], roster, null).map((e) => e.seq)).toEqual([3, 2, 1]);
    expect(recentEvents([ev(1, "a"), ev(2, "b")], roster, 1).map((e) => e.seq)).toEqual([1]);
    expect(whoColor(roster.get("a"))).toBe("#8FD3A2");
    expect(whoColor(roster.get("b"))).toBe("#B49CF2");
    expect(whoColor(undefined)).toBe("#7AA8FF");
    expect(hhmmss(new Date(2026, 0, 1, 7, 5, 9).getTime())).toBe("07:05:09");
    const r = lastHour(Date.parse("2026-11-14T10:00:00Z"));
    expect(r).toEqual({ from: "2026-11-14T09:00:00.000Z", to: "2026-11-14T10:00:00.000Z" });
  });
});
