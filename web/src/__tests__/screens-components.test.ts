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
import ThreadViewDialog from "@/components/app/threadviews/ThreadViewDialog.vue";
import { useText } from "@/pages/design/viewer";
import { thread } from "./fixtures";

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

beforeEach(() => window.localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("ThreadDetail", () => {
  it("shows the breadcrumb, goes back, and keeps the message box of a done thread with the reopen hint", async () => {
    stubApi({ "/messages": { items: [], next_cursor: null }, "/questions": [] });
    const w = mountWith(ThreadDetail, { pid: "p1", thread: thread(12, { title: "员工通信", status: "done" }), view: null });
    await flushPromises();
    expect(w.get("[aria-current=page]").text()).toBe("#12 员工通信");
    expect(w.get("[data-testid=reopen-hint]").text()).toBe("发送消息会重新打开讨论串");
    expect(w.find("[data-testid=closed-note]").exists()).toBe(false);
    const box = document.body.querySelector<HTMLTextAreaElement>("textarea[placeholder^='回复组长']");
    expect(box).not.toBeNull();
    expect(box!.disabled).toBe(false);
    await w.get("nav[aria-label=位置] button").trigger("click");
    expect(w.emitted("back")).toHaveLength(1);
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
