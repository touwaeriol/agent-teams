// @vitest-environment jsdom
// Behaviour of the ported screens on Vue: thread panel header and closed composer, the views
// dialog tabs, the 全部放行 confirmation, the library's view toggle and rows, signed-text fetches.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import LibraryBrowser from "@/components/app/deliverables/LibraryBrowser.vue";
import ThreadDetail from "@/components/app/thread/ThreadDetail.vue";
import AutoApproveToggle from "@/components/app/threadviews/AutoApproveToggle.vue";
import MembersView from "@/components/app/threadviews/MembersView.vue";
import ThreadViewDialog from "@/components/app/threadviews/ThreadViewDialog.vue";
import { useText } from "@/pages/design/viewer";
import MobileOverview from "@/pages/main/MobileOverview.vue";
import PanelList from "@/pages/main/PanelList.vue";
import { foldedKey } from "@/lib/threadList";
import { resetSeenCache } from "@/lib/deliverableStrip";
import { thread } from "./fixtures";
import { resetListViews } from "@/lib/threadListView";

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

/** Answers GETs by URL fragment (first match), everything else with {}. */
function stubApi(routes: Record<string, unknown>) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = input instanceof Request ? input.url : String(input);
    const hit = Object.keys(routes).find((k) => url.includes(k));
    return json(hit ? routes[hit] : {});
  });
}

function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }) }], router] as never[];
}

const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });

beforeEach(() => {
  window.localStorage.clear();
  resetListViews();
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("ThreadDetail", () => {
  it("shows the capsule header, goes back, and keeps the message box of a done thread with the reopen hint", async () => {
    stubApi({ "/messages": { items: [], next_cursor: null }, "/questions": [] });
    const w = mountWith(ThreadDetail, { pid: "p1", thread: thread(12, { title: "员工通信", status: "done" }), view: null });
    await flushPromises();
    expect(w.get("[aria-current=page]").text()).toBe("#12 员工通信");
    expect(w.get("[data-testid=reopen-hint]").text()).toBe("发送消息会重新打开讨论串");
    expect(w.find("[data-testid=closed-note]").exists()).toBe(false);
    const box = document.body.querySelector<HTMLTextAreaElement>("textarea[placeholder^='回复组长']");
    expect(box).not.toBeNull();
    expect(box!.disabled).toBe(false);
    expect(w.get("[data-capsule=title] [data-mark]").attributes("data-mark")).toBe("done");
    await w.get("button[aria-label=返回讨论串列表]").trigger("click");
    expect(w.emitted("back")).toHaveLength(1);
  });
  it("header capsules: running spinner with the plan 2/7, node status, the ⋯ menu; waiting shows the orange mark; ✕ only when closable", async () => {
    stubApi({ "/messages": { items: [], next_cursor: null }, "/questions": [] });
    const step = (i: number, status: "todo" | "doing" | "done") => ({ id: `m${i}`, pos: i, title: `s${i}`, status, auto_done: false, tasks_total: 0, tasks_done: 0 });
    const plan = [step(1, "done"), step(2, "done"), step(3, "doing"), step(4, "todo"), step(5, "todo"), step(6, "todo"), step(7, "todo")];
    const lead = { id: "e1", name: "lead", tier: "lead", node: { id: "n1", name: "rc-erio", online: true }, status: { state: "typing" } };
    const dev = { id: "e2", name: "dev", tier: "member", node: { id: "n2", name: "cvp", online: false }, status: { state: "idle" } };
    const running = thread(12, { main_tasks: plan, members: [lead, dev] as never });
    const w = mountWith(ThreadDetail, { pid: "p1", thread: running, view: null });
    await flushPromises();
    const title = w.get("[data-capsule=title]");
    expect(title.get("[data-mark]").attributes("data-mark")).toBe("working");
    expect(title.find("[data-spin]").exists()).toBe(true);
    expect(title.get("[data-progress]").text()).toBe("2/7");
    const actions = w.get("[data-capsule=actions]");
    expect(actions.get("[data-node]").attributes("data-node")).toBe("partial");
    expect(actions.get("[data-node]").attributes("aria-label")).toBe("节点：rc-erio 在线 · cvp 离线");
    expect(actions.find("button[aria-label^='讨论串 #12']").exists()).toBe(true);
    expect(w.find("button[aria-label=关闭]").exists()).toBe(true);
    await w.setProps({ thread: thread(12, { status: "review", members: [] }), closable: false });
    expect(w.get("[data-capsule=title] [data-mark]").attributes("data-mark")).toBe("wait");
    expect(w.find("[data-capsule=title] [data-progress]").exists()).toBe(false);
    expect(w.get("[data-node]").attributes("data-node")).toBe("none");
    expect(w.find("button[aria-label=关闭]").exists()).toBe(false);
  });
  it("tells an archived thread's composer how to go on and unarchives in one click", async () => {
    const fetch = stubApi({ "/messages": { items: [], next_cursor: null }, "/questions": [] });
    const w = mountWith(ThreadDetail, { pid: "p1", thread: thread(12, { id: "th_12", status: "archived" }), view: null });
    await flushPromises();
    expect(w.get("[data-testid=closed-note]").text()).toContain("讨论串已归档，取消归档后再继续");
    expect(document.body.querySelector("textarea[placeholder^='回复组长']")).toBeNull();
    expect(w.find("[data-testid=reopen-hint]").exists()).toBe(false);
    const button = w.findAll("[data-testid=closed-note] button").find((b) => b.text() === "取消归档");
    await button!.trigger("click");
    await flushPromises();
    const urls = fetch.mock.calls.map(([input]) => (input instanceof Request ? `${input.method} ${input.url}` : String(input)));
    expect(urls.some((u) => u.startsWith("POST") && u.includes("/api/threads/th_12/unarchive"))).toBe(true);
  });
  it("offers the lead's message box while running", async () => {
    stubApi({ "/messages": { items: [], next_cursor: null }, "/questions": [] });
    const w = mountWith(ThreadDetail, { pid: "p1", thread: thread(12), view: null });
    await flushPromises();
    expect(document.body.querySelector("textarea[placeholder^='回复组长']")).not.toBeNull();
    expect(w.find("[data-testid=reopen-hint]").exists()).toBe(false);
  });
});

describe("ThreadViewDialog", () => {
  it("switches views with the tabs and closes to the chat", async () => {
    stubApi({ "/tasks": [], "/merge-queue": [], "/checks": [], "/deliverables": { items: [], storage_bytes: 0 }, "/roles": { roles: [] }, "/questions": [] });
    const w = mountWith(ThreadViewDialog, { pid: "p1", thread: thread(12), view: "tasks" });
    await flushPromises();
    const tabs = Array.from(document.body.querySelectorAll<HTMLButtonElement>("nav[aria-label=讨论串视图] button"));
    expect(tabs.map((b) => b.textContent?.trim())).toEqual(["任务", "交付物", "成员"]);
    expect(tabs[0].getAttribute("aria-pressed")).toBe("true");
    tabs[2].click();
    expect(w.emitted("view")?.[0]).toEqual(["members"]);
    document.body.querySelector<HTMLButtonElement>("button .sr-only")?.parentElement?.click();
    await flushPromises();
    expect(w.emitted("view")?.at(-1)).toEqual([null]);
  });
});

describe("MembersView", () => {
  it("marks a shared member working in another thread “在 #12 忙” without a stop button", async () => {
    stubApi({ "/roles": { roles: [] }, "/questions": [] });
    const base = { tier: "member", role: "dev", title: "开发", runtime: "claude", node: null, look: {} };
    const members = [
      { ...base, id: "a", name: "dev-1", thread_no: 3, status: { state: "typing", since: "2026-11-14T10:00:00Z", task_id: "t2" } },
      { ...base, id: "b", name: "dev-2", thread_no: 12, busy_elsewhere: true, status: { state: "typing", since: "2026-11-14T10:00:00Z", task_id: "t5" } },
    ];
    const w = mountWith(MembersView, { thread: thread(3, { members } as never) });
    await flushPromises();
    const rows = w.findAll(".rounded-lg.border");
    expect(rows[0].text()).toContain("任务 t2");
    expect(rows[0].find("button").exists()).toBe(true);
    expect(rows[1].text()).toContain("在 #12 忙");
    expect(rows[1].text()).not.toContain("t5");
    expect(rows[1].find("button").exists()).toBe(false);
  });
});

describe("AutoApproveToggle", () => {
  it("asks before turning it on and only then writes", async () => {
    document.cookie = "agent_teams_csrf=tok";
    const fetch = stubApi({});
    mountWith(AutoApproveToggle, { thread: thread(12) });
    const box = document.body.querySelector<HTMLInputElement>("input[type=checkbox]")!;
    box.click();
    await flushPromises();
    expect(box.checked).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
    const confirm = Array.from(document.body.querySelectorAll("button")).find((b) => b.textContent?.trim() === "全部放行")!;
    confirm.click();
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect(req.method).toBe("PATCH");
    expect(req.url).toContain("/api/threads/t12");
    expect(await req.json()).toEqual({ auto_approve: true });
  });
});

describe("LibraryBrowser", () => {
  const item = (id: string, title: string) => ({ id, project_id: "p1", thread_no: 12, kind: "doc", title, url: "", detail: { hosted: true, entry: `${title}.md`, size: 10 }, published_by: "dev-1", created_at: "2026-11-14T10:00:00Z", updated_at: "2026-11-14T10:00:00Z" });
  it("lists items, remembers grid vs list, and shows the storage line", async () => {
    stubApi({ "/deliverables": { items: [item("d1", "说明"), item("d2", "约定")], storage_bytes: 2048 } });
    const w = mountWith(LibraryBrowser, { pid: "p1", variant: "panel" });
    await flushPromises();
    expect(w.findAll("[role=row]")).toHaveLength(3);
    expect(w.text()).toContain("2 项");
    await w.get("button[aria-label=网格]").trigger("click");
    expect(window.localStorage.getItem("library.view")).toBe("grid");
    expect(w.find("[role=table]").exists()).toBe(false);
    await w.get("button[aria-label=关闭资料库]").trigger("click");
    expect(w.emitted("close")).toHaveLength(1);
  });
  it("says when nothing matches", async () => {
    stubApi({ "/deliverables": { items: [], storage_bytes: 0 } });
    const w = mountWith(LibraryBrowser, { pid: "p1", variant: "page" });
    await flushPromises();
    expect(w.text()).toContain("资料库还是空的");
  });
});

describe("PanelList", () => {
  const item = (id: string, title: string, no: number | null, kind = "doc") => ({ id, project_id: "p1", thread_no: no, kind, title, url: "", detail: { hosted: true, entry: `${title}.pdf`, render: "pdf" as const }, published_by: "dev-1", created_at: "2026-11-14T10:00:00Z", updated_at: "2026-11-14T10:00:00Z" });
  const link = (id: string, title: string, kind: string) => ({ ...item(id, title, null, kind), url: "https://x.dev/" + id, detail: {} });
  const threads = [thread(1, { title: "部署", status: "done" }), thread(2, { title: "接口", status: "done", latest_deliverable: item("d2", "接口文档", 2) })];
  it("shows the project's deliverables, remembers folds per user, searches, and opens the library", async () => {
    stubApi({ "/deliverables": { items: [item("d1", "落地页", null), item("d2", "接口文档", 2), link("d3", "PR #41", "pr"), link("d4", "外链文档", "doc")], storage_bytes: 0 }, "/waiting": { questions: [], reviews: [] } });
    const w = mount(PanelList, { props: { pid: "p1", threads, loading: false, userId: "u1" }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    const strip = w.get("section[aria-label=交付物]");
    expect(strip.text()).toContain("落地页");
    expect(strip.text()).toContain("查看全部");
    // PRs and plain links stay out of the strip
    expect(strip.text()).not.toContain("PR #41");
    expect(strip.text()).not.toContain("外链文档");
    await w.get("[data-group=done] > button").trigger("click");
    expect(JSON.parse(window.localStorage.getItem(foldedKey("u1"))!)).toEqual({ done: true });
    expect(w.text()).not.toContain("#1 部署");
    await strip.get("button[aria-expanded]").trigger("click");
    expect(JSON.parse(window.localStorage.getItem(foldedKey("u1"))!)).toEqual({ done: true, deliverables: true });
    expect(strip.text()).not.toContain("落地页");
    expect(strip.text()).toContain("查看全部"); // on the title line, also when folded

    const again = mount(PanelList, { props: { pid: "p1", threads, loading: false, userId: "u1" }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    expect(again.get("[data-group=done] > button").attributes("aria-expanded")).toBe("false");
    again.unmount();

    await w.get("button[aria-label=搜索讨论串]").trigger("click");
    await w.get("input[aria-label=搜索讨论串]").setValue("部署");
    expect(w.text()).toContain("#1 部署");
    expect(w.text()).not.toContain("#2 接口");
    expect(w.find("section[aria-label=交付物]").exists()).toBe(false);
  });
  it("tags deliverables updated since they were last opened as 新内容; opening one clears it", async () => {
    window.localStorage.setItem("deliverables.seen.p1", JSON.stringify({ since: "2026-11-14T09:00:00Z", seen: { d2: "2026-11-14T11:00:00Z" } }));
    resetSeenCache();
    stubApi({ "/deliverables": { items: [item("d1", "落地页", null), item("d2", "接口文档", 2)], storage_bytes: 0 }, "/waiting": { questions: [], reviews: [] } });
    const w = mount(PanelList, { props: { pid: "p1", threads, loading: false, userId: "u1" }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    const cards = w.findAll("section[aria-label=交付物] a");
    expect(cards.map((c) => c.text().includes("新内容"))).toEqual([true, false]);
    await cards[0].trigger("click");
    await flushPromises();
    expect(w.get("section[aria-label=交付物]").text()).not.toContain("新内容");
    expect(JSON.parse(window.localStorage.getItem("deliverables.seen.p1")!).seen.d1).toBeTruthy();
    w.unmount();
  });
  it("查看全部 opens the library panel", async () => {
    stubApi({ "/deliverables": { items: [item("d1", "落地页", null)], storage_bytes: 0 }, "/waiting": { questions: [], reviews: [] } });
    const [vq, router] = plugins() as unknown as [never, ReturnType<typeof createRouter>];
    const w = mount(PanelList, { props: { pid: "p1", threads, loading: false, userId: "u1" }, global: { plugins: [vq, router] as never[] }, attachTo: document.body });
    await flushPromises();
    await w.findAll("button").find((b) => b.text() === "查看全部")!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.panel).toBe("library");
  });
  it("in the phone overview (sheet) it has no header of its own and 查看全部 switches tabs", async () => {
    stubApi({ "/deliverables": { items: [item("d1", "落地页", null)], storage_bytes: 0 }, "/waiting": { questions: [], reviews: [] } });
    const w = mount(PanelList, { props: { pid: "p1", threads, loading: false, userId: "u1", sheet: true }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    expect(w.find("header").exists()).toBe(false);
    await w.findAll("button").find((b) => b.text() === "查看全部")!.trigger("click");
    expect(w.emitted("library")).toHaveLength(1);
  });
});

describe("MobileOverview", () => {
  it("segmented 讨论串 / 资料库 / 定时任务 with search on the right", async () => {
    stubApi({ "/deliverables": { items: [], storage_bytes: 0 }, "/waiting": { questions: [], reviews: [] }, "/schedules": [] });
    const threads = [thread(1, { title: "部署", status: "done" }), thread(2, { title: "接口", status: "review" })];
    const w = mountWith(MobileOverview, { pid: "p1", tab: "threads", threads, loading: false, userId: "u1" });
    await flushPromises();
    const tabs = w.findAll("[role=tab]");
    expect(tabs.map((t) => t.text())).toEqual(["讨论串", "资料库", "定时任务"]);
    expect(tabs[0].attributes("aria-selected")).toBe("true");
    expect(w.findAll("section[data-group]").map((s) => s.attributes("data-group"))).toEqual(["waiting", "done"]);
    expect(w.find("[data-group=done] [data-row][data-compact]").exists()).toBe(true);
    await w.get("header button[aria-label=搜索讨论串]").trigger("click");
    await w.get("input[aria-label=搜索讨论串]").setValue("部署");
    expect(w.text()).toContain("#1 部署");
    expect(w.text()).not.toContain("#2 接口");
    await tabs[2].trigger("click");
    expect(w.emitted("tab")).toEqual([["schedules"]]);
    await w.setProps({ tab: "schedules" });
    await flushPromises();
    expect(w.text()).toContain("还没有定时任务");
    expect(w.find("header button[aria-label^=搜索]").exists()).toBe(false);
    await w.setProps({ tab: "library" });
    await flushPromises();
    expect(w.text()).toContain("资料库还是空的");
    expect(w.find("input[placeholder=搜索资料库]").exists()).toBe(false);
    await w.get("header button[aria-label=搜索资料库]").trigger("click");
    expect(w.find("input[placeholder=搜索资料库]").exists()).toBe(true);
    await w.get("header button[aria-label=关闭]").trigger("click");
    expect(w.emitted("close")).toHaveLength(1);
  });
});

describe("useText", () => {
  it("fetches without cookies and reports failures", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response("# hi")).mockResolvedValueOnce(new Response("gone", { status: 410 }));
    let state!: ReturnType<typeof useText>;
    const url = { value: "https://files.test/a.md" };
    const Host = defineComponent({ props: { u: String }, setup: (p) => ((state = useText(() => p.u ?? "")), () => h("div")) });
    const w = mount(Host, { props: { u: url.value } });
    await flushPromises();
    expect(state.text.value).toBe("# hi");
    expect(fetch.mock.calls[0][1]).toMatchObject({ credentials: "omit", referrerPolicy: "no-referrer" });
    await w.setProps({ u: "https://files.test/b.md" });
    await flushPromises();
    expect(state.error.value).toBe(true);
    expect(state.text.value).toBeUndefined();
  });
});
