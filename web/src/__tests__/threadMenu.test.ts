// @vitest-environment jsdom
// The thread ⋯ menu (Claude-app layout): what it offers per status, pin / reminder / feedback
// through the API, 上一个 / 下一个 in list order, the keyboard shortcuts, and the list helpers
// behind them (pin order, fired reminders back in 进行中, reminder times and presets).
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, type Component } from "vue";
import { createMemoryHistory, createRouter, type Router } from "vue-router";
import ThreadMenu from "@/components/app/ThreadMenu.vue";
import { threadNav } from "@/components/app/thread/detail";
import { menuItems } from "@/components/app/threadMenu";
import IconMenu from "@/components/common/IconMenu.vue";
import { freshReminders } from "@/lib/reminders";
import { keyLabel, threadShortcut } from "@/lib/shortcuts";
import { groupThreads, listGroupOf, listOrder, neighbours, visibleOrder, NO_FILTER, reminderLabel, reminderPresets } from "@/lib/threadList";
import { thread } from "./fixtures";

let router: Router;
function plugins() {
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[];
}
const mounted: { unmount: () => void }[] = [];
const mountWith = (c: Component, props: Record<string, unknown> = {}) => {
  const w = mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });
  mounted.push(w);
  return w;
};
const ok = (body: unknown = {}) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
const menuText = () => [...document.body.querySelectorAll(".el-dropdown-menu__item")].map((e) => e.textContent?.replace(/\s+/g, " ").trim() ?? "");

async function openMenu(w: ReturnType<typeof mountWith>) {
  await w.get("button[aria-label^=讨论串]").trigger("click");
  await flushPromises();
}

beforeEach(() => {
  window.localStorage.clear();
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  mounted.splice(0).forEach((w) => {
    try {
      w.unmount();
    } catch {
      // already unmounted by the test
    }
  });
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("menuItems", () => {
  it("pin or unpin by pinned_at; reminders for any thread not archived; clear when one is set or fired", () => {
    expect(menuItems({ status: "running" })).toMatchObject({ pin: true, unpin: false, remind: true, clearReminder: false });
    expect(menuItems({ status: "done", pinned_at: "2026-11-16T01:00:00Z" })).toMatchObject({ pin: false, unpin: true, remind: true });
    expect(menuItems({ status: "archived" }).remind).toBe(false);
    expect(menuItems({ status: "running", remind_at: "2026-11-17T01:00:00Z" }).clearReminder).toBe(true);
    expect(menuItems({ status: "running", reminded_at: "2026-11-16T01:00:00Z" }).clearReminder).toBe(true);
  });
});

describe("ThreadMenu items", () => {
  it("a running thread in the header: navigation with shortcuts, links, views, rename, pin, reminder, 解决, pause, archive, feedback, delete", async () => {
    const w = mountWith(ThreadMenu, { thread: thread(2, { status: "running" }), withViews: true, fullHref: "/projects/p1/threads/2/full", nav: { prev: "/a", next: null } });
    await openMenu(w);
    const text = menuText();
    for (const label of ["上一个讨论串", "下一个讨论串", "在新窗口中打开", "全屏打开", "复制链接", "任务", "交付物", "成员", "重命名", "置顶", "添加提醒", "解决", "暂停", "归档", "发送反馈", "删除"])
      expect(text.some((t) => t.startsWith(label)), label).toBe(true);
    expect(text.some((t) => t.startsWith("重新打开") || t.startsWith("取消置顶") || t.startsWith("取消归档"))).toBe(false);
    const next = [...document.body.querySelectorAll(".el-dropdown-menu__item")].find((e) => e.textContent?.includes("下一个讨论串"))!;
    expect(next.getAttribute("aria-disabled")).toBe("true");
    expect(text.find((t) => t.startsWith("解决"))).toMatch(/Ctrl\+Alt\+Enter|⌃⌥↩/);
    expect(document.body.querySelector(".is-destructive")?.textContent).toContain("删除");
  });
  it("a row (no nav, no views); a pinned done thread offers 取消置顶 and 重新打开, not 解决", async () => {
    const w = mountWith(ThreadMenu, { thread: thread(3, { status: "done", pinned_at: "2026-11-16T01:00:00Z" }) });
    await openMenu(w);
    const text = menuText();
    expect(text.some((t) => t.startsWith("上一个讨论串") || t.startsWith("任务") || t.startsWith("全屏打开"))).toBe(false);
    for (const label of ["在新窗口中打开", "复制链接", "取消置顶", "重新打开", "归档", "发送反馈"]) expect(text.some((t) => t.startsWith(label)), label).toBe(true);
    expect(text.some((t) => t.startsWith("解决") || t.startsWith("暂停"))).toBe(false);
  });
  it("an archived thread: 取消归档, no reminder", async () => {
    const w = mountWith(ThreadMenu, { thread: thread(4, { status: "archived" }) });
    await openMenu(w);
    const text = menuText();
    expect(text.some((t) => t.startsWith("取消归档"))).toBe(true);
    expect(text.some((t) => t.startsWith("添加提醒") || t.startsWith("归档"))).toBe(false);
  });
});

describe("ThreadMenu actions", () => {
  it("置顶 patches pinned; a reminder preset puts the reminder; 复制链接 copies the thread link", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => ok(thread(2, { pinned_at: "2026-11-16T01:00:00Z", remind_at: "2026-11-17T01:00:00Z" })));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const w = mountWith(ThreadMenu, { thread: thread(2, { id: "th_2" }) });
    w.findComponent(IconMenu).vm.$emit("command", "pin");
    await flushPromises();
    let req = fetch.mock.calls[0][0] as Request;
    expect([req.method, req.url.endsWith("/api/threads/th_2"), await req.json()]).toEqual(["PATCH", true, { pinned: true }]);

    const before = Date.now();
    const sub = w.findAllComponents({ name: "ElDropdown" }).find((c) => c.props("trigger") === "hover")!;
    sub.vm.$emit("command", "hour");
    await flushPromises();
    req = fetch.mock.calls.at(-1)![0] as Request;
    const body = (await req.json()) as { at: string };
    expect([req.method, req.url.endsWith("/api/threads/th_2/reminder")]).toEqual(["PUT", true]);
    expect(Date.parse(body.at) - before).toBeGreaterThanOrEqual(3_600_000 - 1000);

    w.findComponent(IconMenu).vm.$emit("command", "copy");
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/projects/p1/threads/2`);
  });
  it("发送反馈 posts the text and the 包含讨论串内容 choice", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => ok({ id: "f1" }));
    const w = mountWith(ThreadMenu, { thread: thread(5, { id: "th_5" }) });
    w.findComponent(IconMenu).vm.$emit("command", "feedback");
    await flushPromises();
    const box = document.body.querySelector<HTMLTextAreaElement>("textarea[aria-label=反馈内容]")!;
    box.value = "太慢了";
    box.dispatchEvent(new Event("input"));
    document.body.querySelector<HTMLInputElement>(".el-checkbox input")!.click();
    await nextTick();
    document.body.querySelector<HTMLFormElement>(".at-dialog form")!.dispatchEvent(new Event("submit"));
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect(req.url).toContain("/api/threads/th_5/feedback");
    expect(await req.json()).toEqual({ text: "太慢了", include_content: true });
  });
});

describe("ThreadMenu shortcuts", () => {
  const key = (init: KeyboardEventInit, target: EventTarget = document.body) => target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));
  it("Alt+↑ / Alt+↓ go to the neighbours, not at an end, also while typing text", async () => {
    mountWith(ThreadMenu, { thread: thread(2), shortcuts: true, nav: { prev: "/projects/p1/threads/1", next: null } });
    await router.isReady();
    key({ key: "ArrowDown", altKey: true });
    await flushPromises();
    expect(router.currentRoute.value.path).toBe("/");
    const input = document.createElement("textarea");
    document.body.append(input);
    input.value = "草稿";
    key({ key: "ArrowUp", altKey: true }, input);
    await flushPromises();
    expect(router.currentRoute.value.path).toBe("/projects/p1/threads/1");
  });
  it("Ctrl+Alt+Enter asks to resolve an active thread and reopens a done one; rows without `shortcuts` ignore keys", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => ok({}));
    const row = mountWith(ThreadMenu, { thread: thread(9, { id: "th_9", status: "done" }) });
    key({ key: "Enter", altKey: true, ctrlKey: true });
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
    row.unmount();

    mountWith(ThreadMenu, { thread: thread(2), shortcuts: true });
    const e = new KeyboardEvent("keydown", { key: "Enter", altKey: true, ctrlKey: true, bubbles: true, cancelable: true });
    document.body.dispatchEvent(e);
    await nextTick();
    expect(e.defaultPrevented).toBe(true);
    expect(document.body.textContent).toContain("把 #2 标记为完成？");

    mountWith(ThreadMenu, { thread: thread(3, { id: "th_3", status: "done" }), shortcuts: true });
    key({ key: "Enter", altKey: true, ctrlKey: true });
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect([req.method, req.url.endsWith("/api/threads/th_3/reopen")]).toEqual(["POST", true]);
  });
  it("threadShortcut and its labels", () => {
    expect(threadShortcut(new KeyboardEvent("keydown", { key: "ArrowUp", altKey: true }))).toBe("prev");
    expect(threadShortcut(new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true }))).toBe("next");
    expect(threadShortcut(new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true, shiftKey: true }))).toBe(null);
    expect(threadShortcut(new KeyboardEvent("keydown", { key: "ArrowDown" }))).toBe(null);
    expect(threadShortcut(new KeyboardEvent("keydown", { key: "Enter", altKey: true, metaKey: true }))).toBe("resolve");
    expect(keyLabel("Alt+↑", false)).toBe("Alt+↑");
    expect(keyLabel("Alt+↑", true)).toBe("⌥↑");
    expect(keyLabel("Ctrl+Alt+Enter", true)).toBe("⌃⌥↩");
  });
});

describe("list order, pins and reminders", () => {
  const at = (min: number) => new Date(Date.parse("2026-11-16T10:00:00Z") + min * 60_000).toISOString();
  it("pinned threads come first in their group, newest pin first", () => {
    const list = [
      thread(1, { status: "done", updated_at: at(5) }),
      thread(2, { status: "done", updated_at: at(1), pinned_at: at(2) }),
      thread(3, { status: "done", updated_at: at(9) }),
      thread(4, { status: "done", updated_at: at(0), pinned_at: at(3) }),
    ];
    expect(groupThreads(list)[0].items.map((t) => t.no)).toEqual([4, 2, 3, 1]);
  });
  it("a fired reminder brings a thread back to 进行中, unless archived", () => {
    expect(listGroupOf(thread(1, { status: "done", reminded_at: at(0) }))).toBe("active");
    expect(listGroupOf(thread(1, { status: "archived", reminded_at: at(0) }))).toBe("archived");
  });
  it("prev / next follow the shown list: groups, filters, folds; no wrapping", () => {
    const list = [
      thread(1, { status: "done", updated_at: at(1) }),
      thread(2, { status: "running", counts: { ...thread(2).counts, waiting: 1 }, updated_at: at(2) }),
      thread(3, { status: "done", updated_at: at(3) }),
      thread(4, { status: "paused", updated_at: at(4) }),
    ];
    const order = listOrder(list, 4, NO_FILTER);
    expect(order).toEqual([2, 3, 1, 4]);
    expect(neighbours(order, 2)).toEqual({ prev: null, next: 3 });
    expect(neighbours(order, 4)).toEqual({ prev: 1, next: null });
    expect(listOrder(list, 4, { ...NO_FILTER, groups: ["done", "paused"] })).toEqual([3, 1, 4]);
    expect(listOrder(list, 4, NO_FILTER, undefined, { done: true })).toEqual([2, 4]);
    expect(listOrder(list, 3, NO_FILTER, undefined, { done: true })).toEqual([2, 3, 1, 4]); // the open one is folded away
    expect(listOrder(list, 1, { ...NO_FILTER, q: "T4" })).toEqual([2, 3, 1, 4]); // filtered out: whole list
    expect(threadNav([2, 4, 3], 4, (n) => `/t/${n}`)).toEqual({ prev: "/t/2", next: "/t/3" });
    expect(visibleOrder(list, NO_FILTER)).toEqual([2, 3, 1, 4]); // Alt+↓ from the list opens 2, Alt+↑ opens 4
    expect(visibleOrder(list, NO_FILTER, undefined, { done: true })).toEqual([2, 4]);
  });
  it("reminder labels and presets in local time", () => {
    const now = new Date(2026, 10, 16, 10, 30).getTime(); // a Monday
    expect(reminderLabel(new Date(2026, 10, 16, 18, 0).toISOString(), now)).toBe("今天 18:00");
    expect(reminderLabel(new Date(2026, 10, 17, 9, 0).toISOString(), now)).toBe("明天 09:00");
    expect(reminderLabel(new Date(2026, 10, 20, 9, 5).toISOString(), now)).toBe("周五 09:05");
    expect(reminderLabel(new Date(2026, 10, 30, 9, 0).toISOString(), now)).toBe("11月30日 09:00");
    const p = Object.fromEntries(reminderPresets(new Date(now)).map((x) => [x.id, x.at.getTime()]));
    expect(p.hour).toBe(now + 3_600_000);
    expect(p.later).toBe(new Date(2026, 10, 16, 18, 0).getTime());
    expect(p.tomorrow).toBe(new Date(2026, 10, 17, 9, 0).getTime());
    expect(p.monday).toBe(new Date(2026, 10, 23, 9, 0).getTime());
    const evening = reminderPresets(new Date(2026, 10, 18, 16, 0)); // Wednesday afternoon
    expect(evening.find((x) => x.id === "later")!.at.getTime()).toBe(new Date(2026, 10, 18, 19, 0).getTime());
    expect(evening.find((x) => x.id === "monday")!.at.getTime()).toBe(new Date(2026, 10, 23, 9, 0).getTime());
  });
  it("each fired reminder is announced once; ones from before the page opened stay quiet", () => {
    const seen = new Set<string>();
    const since = Date.parse(at(0));
    const list = [thread(1, { reminded_at: at(-5) }), thread(2, { reminded_at: at(1) }), thread(3)];
    expect(freshReminders(list, seen, since).map((t) => t.no)).toEqual([2]);
    expect(freshReminders(list, seen, since)).toEqual([]);
    expect(freshReminders([thread(2, { reminded_at: at(30) })], seen, since).map((t) => t.no)).toEqual([2]);
  });
});
