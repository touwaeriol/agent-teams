// @vitest-environment jsdom
// Forwarding project-chat messages to a thread: the selection, the picker (search, groups,
// archived threads, 新建讨论串, the request it sends) and the forwarded copy in a thread chat.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import ChatBlockView from "@/components/app/ChatBlockView.vue";
import ForwardBar from "@/components/app/forward/ForwardBar.vue";
import ForwardDialog from "@/components/app/forward/ForwardDialog.vue";
import { chatBlocks } from "@/lib/chat";
import { backLink, forwardLine, MAX_FORWARD, pickerGroups, selection, startSelecting, stopSelecting, titleFrom, toggle, toggled } from "@/lib/forward";
import { message, thread } from "./fixtures";

function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[];
}
const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });

afterEach(() => {
  vi.restoreAllMocks();
  stopSelecting();
  document.body.innerHTML = "";
});

const copy = (id: string, index: number, count: number, extra: Record<string, unknown> = {}) =>
  message(id, {
    role: "user",
    content: `原话 ${id}`,
    meta: { forwarded_from: { conversation_id: "c1", message_id: 40 + index, message_ids: [40, 41].slice(0, count), count, index, by: "user", ...extra } },
  });

describe("selection", () => {
  it("toggles ids in order and stops at the limit", () => {
    expect(toggled([5, 2].sort(), 3)).toEqual([2, 3, 5]);
    expect(toggled([2, 3], 2)).toEqual([3]);
    const full = Array.from({ length: MAX_FORWARD }, (_, i) => i + 1);
    expect(toggled(full, 99)).toBe(full);
    startSelecting("c1", 7);
    toggle(3);
    expect(selection).toMatchObject({ cid: "c1", ids: [3, 7], active: true });
    stopSelecting();
    expect(selection.active).toBe(false);
  });
  it("selects a bubble by clicking it while selecting; offers 转发到讨论串 otherwise", async () => {
    const [block] = chatBlocks([message("12", { role: "user", content: "看截图" })]);
    const idle = mountWith(ChatBlockView, { block, threads: [], speakerColor: "#000", pid: "p1", forwardable: true });
    await idle.get("button[aria-label=转发到讨论串]").trigger("click");
    expect(idle.emitted("forward")).toEqual([[12]]);
    const picking = mountWith(ChatBlockView, { block, threads: [], speakerColor: "#000", pid: "p1", forwardable: true, selecting: true, selected: false });
    expect(picking.find("button[aria-label=转发到讨论串]").exists()).toBe(false);
    await picking.get("#msg-12").trigger("click");
    await picking.get("[role=checkbox]").trigger("click");
    expect(picking.emitted("toggle")).toEqual([[12], [12]]);
  });
  it("the bar counts, forwards and cancels (also with Esc)", async () => {
    const w = mountWith(ForwardBar, { count: 2 });
    expect(w.text()).toContain("已选 2 条");
    const [fwd, cancel] = w.findAll("button");
    await fwd.trigger("click");
    await cancel.trigger("click");
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(w.emitted("forward")).toHaveLength(1);
    expect(w.emitted("cancel")).toHaveLength(2);
  });
});

describe("picker", () => {
  const threads = [thread(1, { title: "登录页" }), thread(2, { title: "暗色模式", status: "done" }), thread(3, { title: "旧需求", status: "archived" })];
  it("searches by number or title and groups like the thread list", () => {
    expect(pickerGroups(threads, "").map((g) => g.id)).toEqual(["idle", "done", "archived"]);
    expect(pickerGroups(threads, "#2").flatMap((g) => g.items.map((t) => t.no))).toEqual([2]);
    expect(pickerGroups(threads, "登录").flatMap((g) => g.items.map((t) => t.no))).toEqual([1]);
    expect(titleFrom([{ content: "\n  第一行\n第二行" }])).toBe("第一行");
  });
  it("forwards to the chosen thread; archived threads cannot be picked", async () => {
    document.cookie = "agent_teams_csrf=tok";
    const body = { thread_no: 2, thread_id: "t2", title: "暗色模式", count: 2, created: false, duplicate: false, messages: [] };
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
    const w = mountWith(ForwardDialog, { open: true, cid: "c1", ids: [40, 41], messages: [], threads });
    await nextTick();
    const options = [...document.body.querySelectorAll<HTMLButtonElement>("[role=option]")];
    expect(options.map((o) => o.textContent?.trim())).toEqual(["新建讨论串", "#1 登录页", "✓#2 暗色模式", "#3 旧需求"]);
    expect(options[3].disabled).toBe(true);
    options[2].click();
    await nextTick();
    document.body.querySelector<HTMLFormElement>("form")!.requestSubmit();
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect(req.url).toContain("/api/conversations/c1/forward");
    expect(req.headers.get("Idempotency-Key")).toBeTruthy();
    expect(await req.json()).toEqual({ message_ids: [40, 41], thread_no: 2 });
    expect(w.emitted("done")?.[0]).toEqual([body]);
  });
  it("新建讨论串 sends the title (defaulting to the first line)", async () => {
    document.cookie = "agent_teams_csrf=tok";
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ thread_no: 4, created: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    const w = mountWith(ForwardDialog, { open: false, cid: "c1", ids: [40], messages: [message("40", { role: "user", content: "做个暗色模式\n细节见图" })], threads });
    await w.setProps({ open: true });
    await nextTick();
    document.body.querySelector<HTMLButtonElement>("[role=option]")!.click();
    await nextTick();
    expect(document.body.querySelector<HTMLInputElement>("input[aria-label=新讨论串的标题]")!.value).toBe("做个暗色模式");
    document.body.querySelector<HTMLFormElement>("form")!.requestSubmit();
    await flushPromises();
    expect(await (fetch.mock.calls[0][0] as Request).json()).toEqual({ message_ids: [40], new_thread: { title: "做个暗色模式" } });
  });
});

describe("forwarded copies in a thread chat", () => {
  it("draw as the user's bubbles; the last one carries the grey line back to the project chat", () => {
    const blocks = chatBlocks([copy("90", 0, 2), copy("91", 1, 2, { note: "补充" })]);
    expect(blocks.map((b) => b.kind)).toEqual(["user", "user"]);
    expect(forwardLine(blocks[0].message)).toBeNull();
    expect(forwardLine(blocks[1].message)).toEqual({ text: "已从项目对话转发 2 条消息", note: "补充", anchor: 40 });
    const w = mountWith(ChatBlockView, { block: blocks[1], threads: [], speakerColor: "#000", pid: "p1" });
    const link = w.get("[data-forward-line] a");
    expect(link.text()).toBe("已从项目对话转发 2 条消息");
    expect(link.attributes("href")).toBe("/projects/p1?msg=40");
    expect(w.text()).toContain("附言：补充");
    expect(w.find("button[aria-label=转发到讨论串]").exists()).toBe(false);
  });
  it("links keep the thread panel on the board, and leave the full page", () => {
    expect(backLink("/projects/p1/threads/3", "p1", 40)).toEqual({ path: "/projects/p1/threads/3", query: { msg: "40" } });
    expect(backLink("/projects/p1/threads/3/tasks", "p1", 40).path).toBe("/projects/p1/threads/3/tasks");
    expect(backLink("/projects/p1/threads/3/full", "p1", 40).path).toBe("/projects/p1");
    expect(forwardLine(copy("1", 0, 1, { by: "coordinator" }))?.text).toBe("已从项目对话转发 1 条消息（统筹转发）");
  });
  it("the project-chat note opens the thread", async () => {
    const [block] = chatBlocks([message("5", { role: "system", kind: "note", content: "已转发 2 条消息到 #3 · 登录页", meta: { forwarded_to: { no: 3, count: 2, created: false, by: "user", message_ids: [1, 2] } } })]);
    const w = mountWith(ChatBlockView, { block, threads: [], speakerColor: "#000", pid: "p1" });
    await w.get("button").trigger("click");
    expect(w.emitted("openThread")).toEqual([[3]]);
  });
});
