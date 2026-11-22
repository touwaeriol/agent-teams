// @vitest-environment jsdom
// Subthreads and talk between threads: the list nests a subthread under its parent, relay cards
// read like "已向 #7 发送 1 条消息" / "来自 #3 的 1 条消息" and link to the other thread.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import type { Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type { RelayMeta, ThreadView } from "@/api/views";
import RelayCard from "@/components/app/chatcards/RelayCard.vue";
import ThreadList from "@/components/app/ThreadList.vue";
import { chatBlocks, threadChat } from "@/lib/chat";
import { relayHasBody, relayHeader, relayThreadNo } from "@/lib/relay";
import { groupThreads, nestSubthreads, rowNest, subthreadsOf } from "@/lib/threadList";
import { message, thread } from "./fixtures";

function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[];
}
const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });
afterEach(() => {
  document.body.innerHTML = "";
});

const paused = { status: "paused", members: [{ id: "e", name: "lead", tier: "lead", status: { state: "idle" } }] } as unknown as Partial<ThreadView>;

describe("subthreads in the list", () => {
  it("puts a subthread right after its parent in the same group, in number order", () => {
    const items = [thread(5), thread(3, { parent_no: 1 }), thread(1), thread(2, { parent_no: 1 }), thread(4, { parent_no: 9 })];
    expect(nestSubthreads(items).map((t) => t.no)).toEqual([5, 1, 2, 3, 4]);
    expect(rowNest(items[1], items)).toEqual({ nested: true, parentElsewhere: null, children: 0 });
    expect(rowNest(items[2], items)).toEqual({ nested: false, parentElsewhere: null, children: 2 });
    expect(rowNest(items[4], items)).toEqual({ nested: false, parentElsewhere: 9, children: 0 });
    expect(subthreadsOf(items, 1).map((t) => t.no)).toEqual([2, 3]);
  });
  it("groupThreads nests within each group only", () => {
    const groups = groupThreads([thread(1, paused), thread(2, { ...paused, parent_no: 1 }), thread(3, { status: "done", parent_no: 1 })]);
    expect(groups.map((g) => [g.id, g.items.map((t) => t.no)])).toEqual([["idle", [1, 2]], ["done", [3]]]);
  });
  it("indents nested rows, tags a subthread whose parent is elsewhere, and folds the subthreads", async () => {
    const w = mountWith(ThreadList, { threads: [thread(1, paused), thread(2, { ...paused, parent_no: 1 }), thread(3, { status: "done", parent_no: 1 })] });
    expect(w.get("[data-group=idle] [data-subthread='1']").classes()).toContain("ml-3");
    expect(w.get("[data-group=done] [data-parent-tag]").text()).toBe("#1 的子讨论串");
    const toggle = w.get("[data-subs-toggle]");
    expect(toggle.text()).toBe("1 个子讨论串");
    await toggle.trigger("click");
    expect(w.find("[data-group=idle] [data-subthread='1']").exists()).toBe(false);
    await w.get("[data-subs-toggle]").trigger("click");
    expect(w.find("[data-group=idle] [data-subthread='1']").exists()).toBe(true);
  });
});

describe("relay cards", () => {
  const r = (p: Partial<RelayMeta>): RelayMeta => ({ dir: "in", kind: "message", peer: "thread", no: 3, title: "登录", ...p });
  it("headers read like the app's notices", () => {
    expect(relayHeader(r({ dir: "out", no: 7, title: "缓存" }))).toBe("已向 #7 缓存 发送 1 条消息");
    expect(relayHeader(r({}))).toBe("来自 #3 登录 的 1 条消息");
    expect(relayHeader(r({ peer: "coordinator", no: undefined }))).toBe("来自 项目统筹 的 1 条消息");
    expect(relayHeader(r({ dir: "out", peer: "coordinator", no: undefined }))).toBe("已向 项目统筹 发送 1 条消息");
    expect(relayHeader(r({ kind: "report", no: 7, title: "缓存", status: "done" }))).toBe("子讨论串 #7 缓存 · 已完成");
    expect(relayHeader(r({ kind: "opened", dir: "out", no: 7, title: "缓存" }))).toBe("开了子讨论串 #7 缓存");
    expect(relayHeader(r({ kind: "opened", no: 1, sub_no: 7, sub_title: "缓存" }))).toBe("#1 开了子讨论串 #7 缓存");
    expect(relayThreadNo(r({ kind: "opened", no: 1, sub_no: 7 }))).toBe(7);
    expect(relayThreadNo(r({ peer: "coordinator", no: undefined }))).toBeNull();
  });
  it("are chat cards: shown in the thread chat, unfold to the text with a link to the other thread", async () => {
    const m = message("9", { role: "system", kind: "relay", content: "推荐 Valkey", meta: { relay: r({}) } });
    expect(threadChat([m])).toHaveLength(1);
    expect(chatBlocks([m])[0].kind).toBe("card");
    expect(relayHasBody(m)).toBe(true);
    const w = mountWith(RelayCard, { message: m, pid: "p1" });
    expect(w.text()).toContain("来自 #3 登录 的 1 条消息");
    expect(w.text()).not.toContain("推荐 Valkey");
    await w.get("button").trigger("click");
    expect(w.text()).toContain("推荐 Valkey");
    expect(w.get("a").attributes("href")).toBe("/projects/p1/threads/3");
  });
});
