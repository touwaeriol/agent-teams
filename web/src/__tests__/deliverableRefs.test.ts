// @vitest-environment jsdom
// Deliverable ids in message text become cards: which ids count, that only the ones the server
// returns for the project (its owner's) get a card, the request it sends, the coordinator chat,
// and the list of every deliverable a finished thread published.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type { DeliverableView, MessageView } from "@/api/views";
import ChatBlockView from "@/components/app/ChatBlockView.vue";
import ThreadChat from "@/components/app/thread/ThreadChat.vue";
import { chatBlocks } from "@/lib/chat";
import { deliverableIdsIn, MAX_MENTIONED, mentionedDeliverables } from "@/lib/deliverableRefs";
import CoordinatorChat from "@/pages/main/CoordinatorChat.vue";
import { message, thread } from "./fixtures";

const MINE = "dl_01m88nq9y1jrk26hq5amk1rtvs";
const MINE2 = "dl_01m88nq9y1jrk26hq5amk1rtvt";
const FOREIGN = "dl_01m77aaaaaaaaaaaaaaaaaaaaa";

function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }) }], router] as never[];
}
const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });

function deliverable(id: string, patch: Partial<DeliverableView> = {}): DeliverableView {
  return {
    id, project_id: "p1", thread_no: 55, kind: "doc", title: `文档 ${id.slice(-2)}`, url: "", published_by: "netsec-1",
    detail: { hosted: true, entry: "report.md", render: "markdown" }, created_at: "2026-11-24T10:00:00Z", updated_at: "2026-11-24T10:00:00Z", ...patch,
  } as DeliverableView;
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** A fake server: the project's deliverables are MINE and MINE2 (FOREIGN belongs to someone else, never returned). */
function fakeServer(messages: MessageView[] = []) {
  const own = [deliverable(MINE, { title: "安全审计报告" }), deliverable(MINE2, { title: "落地页", kind: "design", published_by: "lead-55", detail: { hosted: true, entry: "index.html", render: "html" } })];
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL((input as Request).url, "http://x");
    if (/\/messages$/.test(url.pathname)) return json({ items: messages.slice().reverse(), next_cursor: null });
    if (/\/questions$/.test(url.pathname)) return json([]);
    if (url.pathname === "/api/projects/p1/deliverables") {
      const ids = url.searchParams.get("ids")?.split(",");
      const no = Number(url.searchParams.get("thread") ?? 0);
      const items = own.filter((d) => (!ids || ids.includes(d.id)) && (!no || d.thread_no === no));
      return json({ items, next_cursor: null, storage_bytes: 0 });
    }
    return json({ items: [], next_cursor: null });
  });
}

const deliverableCalls = (fetch: ReturnType<typeof fakeServer>) =>
  fetch.mock.calls.map((c) => new URL((c[0] as Request).url, "http://x")).filter((u) => u.pathname.endsWith("/deliverables"));

afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("deliverableIdsIn", () => {
  it("finds well-formed ids once each, in order, in code and prose", () => {
    const text = `交付物 \`${MINE}\`，另见 ${MINE2}。再提一次 ${MINE}（${FOREIGN}）`;
    expect(deliverableIdsIn(text)).toEqual([MINE, MINE2, FOREIGN]);
  });
  it("ignores malformed ids and ids glued to other words", () => {
    expect(deliverableIdsIn("dl_123 dl_01M88NQ9Y1JRK26HQ5AMK1RTVS dl_01m88nq9y1jrk26hq5amk1rtvsx xdl_01m88nq9y1jrk26hq5amk1rtvs dl_01m88nq9y1jrk26hq5amk1rtvu")).toEqual([]);
    expect(deliverableIdsIn(`/deliverables/${MINE}?v=2`)).toEqual([MINE]);
    expect(deliverableIdsIn(undefined)).toEqual([]);
  });
  it("takes ids at the start, the end and with one separator between them (no lookbehind)", () => {
    expect(deliverableIdsIn(`${MINE},${MINE2}`)).toEqual([MINE, MINE2]);
    expect(deliverableIdsIn(`交付物${MINE}和${MINE2}`)).toEqual([MINE, MINE2]);
    expect(deliverableIdsIn(`_${MINE} 9${MINE2}`)).toEqual([]);
  });
  it("stops at the limit", () => {
    const many = Array.from({ length: 30 }, (_, i) => `dl_01m88nq9y1jrk26hq5amk1r${String(i).padStart(3, "0")}`).join(" ");
    expect(deliverableIdsIn(many)).toHaveLength(MAX_MENTIONED);
  });
});

describe("mentionedDeliverables", () => {
  it("keeps only what was asked for and returned, in mention order", () => {
    const found = [deliverable(MINE2), deliverable(MINE), deliverable("dl_01m99zzzzzzzzzzzzzzzzzzzzz")];
    expect(mentionedDeliverables([MINE, FOREIGN, MINE2], found).map((d) => d.id)).toEqual([MINE, MINE2]);
    expect(mentionedDeliverables([MINE], undefined)).toEqual([]);
  });
});

describe("cards in a message", () => {
  it("draws a card for the project's deliverables and leaves a foreign id as text", async () => {
    const fetch = fakeServer();
    const [block] = chatBlocks([message("9", { content: `汇报完毕，交付物 \`${MINE}\`；参考 \`${FOREIGN}\`。` })]);
    const w = mountWith(ChatBlockView, { block, threads: [], speakerColor: "#000", pid: "p1" });
    await flushPromises();
    const [call] = deliverableCalls(fetch);
    expect(call.searchParams.get("ids")).toBe(`${MINE},${FOREIGN}`);
    const chips = w.findAll("[data-deliverable-chip]");
    expect(chips.map((c) => c.attributes("data-id"))).toEqual([MINE]);
    expect(chips[0].text()).toContain("安全审计报告");
    expect(chips[0].text()).toContain("netsec-1 发布");
    expect(chips[0].attributes("href")).toBe(`/projects/p1/deliverables/${MINE}`);
    // the foreign id is still in the text, and nothing about it was rendered
    expect(w.text()).toContain(FOREIGN);
    expect(w.findAll("[data-message-deliverables]")).toHaveLength(1);
  });
  it("asks nothing and draws nothing without ids or without a project", async () => {
    const fetch = fakeServer();
    const [plain] = chatBlocks([message("9", { content: "没有交付物" })]);
    mountWith(ChatBlockView, { block: plain, threads: [], speakerColor: "#000", pid: "p1" });
    const [noPid] = chatBlocks([message("10", { content: MINE })]);
    const w = mountWith(ChatBlockView, { block: noPid, threads: [], speakerColor: "#000", pid: "" });
    await flushPromises();
    expect(deliverableCalls(fetch)).toEqual([]);
    expect(w.find("[data-deliverable-chip]").exists()).toBe(false);
  });
  it("draws nothing when the server returns none (no access, deleted)", async () => {
    fakeServer();
    const [block] = chatBlocks([message("9", { content: `看 ${FOREIGN}` })]);
    const w = mountWith(ChatBlockView, { block, threads: [], speakerColor: "#000", pid: "p1" });
    await flushPromises();
    expect(w.find("[data-deliverable-chip]").exists()).toBe(false);
    expect(w.text()).toContain(FOREIGN);
  });
});

describe("coordinator chat", () => {
  it("draws cards for ids in the coordinator's messages", async () => {
    fakeServer([message("1", { role: "user", content: "#55 做完了吗" }), message("2", { content: `#55 已结束，报告 ${MINE}，设计 ${MINE2}，旧的 ${FOREIGN}` })]);
    const w = mountWith(CoordinatorChat, { pid: "p1", coordinator: { conversation_id: "cc1", employee: { name: "leader" } }, threads: [] });
    await flushPromises();
    await flushPromises();
    expect(w.findAll("[data-deliverable-chip]").map((c) => c.attributes("data-id"))).toEqual([MINE, MINE2]);
  });
});

describe("a finished thread's deliverables", () => {
  it("lists every deliverable of the thread (members' too) once it has ended", async () => {
    const fetch = fakeServer([message("3", { content: "最终汇报" })]);
    const done = mountWith(ThreadChat, { pid: "p1", thread: thread(55, { status: "done" }), compact: false });
    await flushPromises();
    await flushPromises();
    const call = deliverableCalls(fetch).find((u) => u.searchParams.get("thread"));
    expect(call?.searchParams.get("thread")).toBe("55");
    const list = done.find("[data-thread-deliverables]");
    expect(list.text()).toContain("本讨论串的交付物 · 2");
    expect(list.findAll("[data-deliverable-chip]").map((c) => c.text())).toEqual([expect.stringContaining("安全审计报告"), expect.stringContaining("落地页")]);
  });
  it("does not list them while the thread is still running", async () => {
    fakeServer([message("3", { content: "进行中" })]);
    const running = mountWith(ThreadChat, { pid: "p1", thread: thread(55, { status: "running" }), compact: false });
    await flushPromises();
    expect(running.find("[data-thread-deliverables]").exists()).toBe(false);
  });
});
