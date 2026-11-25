// @vitest-environment jsdom
// Component behaviour on Vue: composer keys and drafts, image attachments, confirmations,
// question cards answering through the API client, thread list grouping, Markdown rendering.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { useAttachments } from "@/components/app/attachments";
import AskCard from "@/components/app/chatcards/AskCard.vue";
import Composer from "@/components/app/Composer.vue";
import ThreadList from "@/components/app/ThreadList.vue";
import ThreadMark from "@/components/app/ThreadMark.vue";
import ThreadMenu from "@/components/app/ThreadMenu.vue";
import TypingLine from "@/components/app/thread/TypingLine.vue";
import { menuItems } from "@/components/app/threadMenu";
import IconMenu from "@/components/common/IconMenu.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Markdown from "@/components/Markdown.vue";
import { resetDraftMemory, useDraft } from "@/lib/persist";
import { attachmentsField } from "@/lib/images";
import { toast } from "@/lib/toast";
import { threadMark } from "@/lib/threadList";
import { cardLook } from "@/pages/overview/logic";
import type { ThreadView } from "@/api/views";
import { question, thread } from "./fixtures";

function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[];
}

const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });

beforeEach(() => {
  window.localStorage.clear();
  resetDraftMemory();
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("Composer", () => {
  it("sends on Enter, keeps Shift+Enter and IME composition, clears after", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    const w = mountWith(Composer, { label: "给 leader 发消息", placeholder: "说", onSend, draftKey: "draft:c1" });
    const box = w.get("textarea");
    await box.setValue("  hello ");
    await box.trigger("keydown", { key: "Enter", shiftKey: true });
    await box.trigger("keydown", { key: "Enter", isComposing: true });
    expect(onSend).not.toHaveBeenCalled();
    await box.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(onSend).toHaveBeenCalledWith("hello", [], [], []);
    expect((box.element as HTMLTextAreaElement).value).toBe("");
  });
  it("puts the text back when sending fails", async () => {
    const onSend = vi.fn().mockRejectedValue(new Error("no"));
    const w = mountWith(Composer, { label: "x", placeholder: "x", onSend, variant: "inline" });
    const input = w.get("input");
    await input.setValue("keep me");
    await w.get("form").trigger("submit");
    await flushPromises();
    expect((input.element as HTMLInputElement).value).toBe("keep me");
  });
  it("disables send while empty", () => {
    const w = mountWith(Composer, { label: "x", placeholder: "x", onSend: vi.fn() });
    expect(w.get("button[type=submit]").attributes("disabled")).toBeDefined();
  });
});

describe("drafts", () => {
  it("survive remounts per key and follow a changing key", async () => {
    vi.useFakeTimers();
    let draft!: ReturnType<typeof useDraft>;
    const key = { value: "draft:v2:u1:p1:a" };
    const Host = defineComponent({ setup: () => ((draft = useDraft(() => key.value)), () => h("div")) });
    mount(Host);
    draft.value = "typed";
    vi.advanceTimersByTime(300);
    expect(JSON.parse(window.localStorage.getItem("draft:v2:u1:p1:a") ?? "{}").t).toBe("typed");
    mount(Host);
    expect(draft.value).toBe("typed");
    draft.value = "";
    expect(window.localStorage.getItem("draft:v2:u1:p1:a")).toBeNull();
    vi.useRealTimers();
  });
});

describe("attachments", () => {
  it("uploads picked files of any type, reports ids when done", async () => {
    URL.createObjectURL = vi.fn(() => "blob:x");
    URL.revokeObjectURL = vi.fn();
    let a!: ReturnType<typeof useAttachments>;
    const pending: Array<(v: { id: string }) => void> = [];
    const finish = (v: { id: string }) => pending.forEach((r, i) => r({ id: `${v.id}-${i}` }));
    const uploader = vi.fn(() => new Promise<{ id: string }>((r) => pending.push(r)));
    const Host = defineComponent({ setup: () => ((a = useAttachments("p1", uploader)), () => h("div")) });
    mountWith(Host);
    a.add([new File(["x"], "a.png", { type: "image/png" }), new File(["x"], "b.txt", { type: "text/plain" }), new File(["x"], "c.exe", { type: "" })]);
    expect(a.items.value.map((x) => x.kind)).toEqual(["image", "text", "file"]);
    expect(a.items.value[1]!.preview).toBe("");
    expect(a.ids()).toBeNull();
    expect(a.uploading.value).toBe(true);
    finish({ id: "att1" });
    await flushPromises();
    expect(a.ids()).toEqual(["att1-0", "att1-1", "att1-2"]);
    expect(uploader).toHaveBeenCalledWith("p1", expect.any(File), expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });

  it("refuses empty (0 byte) files with a reason", () => {
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined);
    let a!: ReturnType<typeof useAttachments>;
    const uploader = vi.fn(async () => ({ id: "x" }));
    mountWith(defineComponent({ setup: () => ((a = useAttachments("p1", uploader)), () => h("div")) }));
    a.add([new File([], "empty.txt", { type: "text/plain" })]);
    expect(a.items.value).toEqual([]);
    expect(uploader).not.toHaveBeenCalled();
    expect(err).toHaveBeenCalledWith("empty.txt 是空文件（0 字节），不能作为附件");
  });

  it("removing an item after the box was remounted still cancels its upload", async () => {
    URL.createObjectURL = vi.fn(() => "blob:x");
    URL.revokeObjectURL = vi.fn();
    const signals: AbortSignal[] = [];
    const uploader = vi.fn((_p: string, _f: File, h?: { signal?: AbortSignal }) => (signals.push(h!.signal!), new Promise<{ id: string }>(() => undefined)));
    let a!: ReturnType<typeof useAttachments>;
    const Host = defineComponent({ setup: () => ((a = useAttachments("p1", uploader, () => "draft:v2:u1:p1:remount")), () => h("div")) });
    const first = mountWith(Host);
    a.add([new File(["x"], "big.zip")]);
    first.unmount();
    mountWith(Host);
    expect(a.items.value).toHaveLength(1);
    a.remove(a.items.value[0]!.key);
    expect(signals[0]!.aborted).toBe(true);
  });

  it("puts attachments into a request body only when there are some", () => {
    expect(attachmentsField([])).toEqual({});
    expect(attachmentsField(null)).toEqual({});
    expect(attachmentsField(["img_1"])).toEqual({ attachments: ["img_1"] });
  });
});

describe("ConfirmAction", () => {
  it("asks first, then confirms", async () => {
    const w = mountWith(ConfirmAction, { title: "删除项目？", description: "不能恢复" });
    await w.get("button").trigger("click");
    await nextTick();
    expect(document.body.textContent).toContain("删除项目？");
    const confirm = [...document.body.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "删除").at(-1)!;
    confirm.click();
    await nextTick();
    expect(w.emitted("confirm")).toHaveLength(1);
  });
});

describe("ThreadMenu 重新打开", () => {
  it("offers it for a done or failed thread only", () => {
    expect(["done", "failed"].every((status) => menuItems({ status } as never).reopen)).toBe(true);
    expect(["running", "waiting", "queued", "paused", "review", "archived"].some((status) => menuItems({ status } as never).reopen)).toBe(false);
  });
  it("reopens the thread through the API without asking", async () => {
    document.cookie = "agent_teams_csrf=tok";
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    const w = mountWith(ThreadMenu, { thread: thread(3, { id: "th_3", status: "done" }) });
    w.findComponent(IconMenu).vm.$emit("command", "reopen");
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect(req.method).toBe("POST");
    expect(req.url).toContain("/api/threads/th_3/reopen");
  });
});

describe("ThreadMenu 标记为完成", () => {
  it("offers it for a thread still going, not for a finished one or one in review", () => {
    expect(["running", "waiting", "queued", "paused"].every((status) => menuItems({ status } as never).complete)).toBe(true);
    expect(["review", "done", "failed", "archived"].some((status) => menuItems({ status } as never).complete)).toBe(false);
  });
  it("asks first, then closes the thread through the API", async () => {
    document.cookie = "agent_teams_csrf=tok";
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    const w = mountWith(ThreadMenu, { thread: thread(2, { id: "th_2", status: "running" }) });
    w.findComponent(IconMenu).vm.$emit("command", "complete");
    await nextTick();
    expect(document.body.textContent).toContain("把 #2 标记为完成？");
    expect(fetch).not.toHaveBeenCalled();
    const confirm = [...document.body.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "标记为完成").at(-1)!;
    confirm.click();
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect(req.method).toBe("POST");
    expect(req.url).toContain("/api/threads/th_2/complete");
  });
});

describe("AskCard", () => {
  it("answers a one-choice question on click through the API", async () => {
    document.cookie = "agent_teams_csrf=tok";
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    const q = question("q1", { text: "用哪个？", options: ["A 方案", "B 方案"] });
    const w = mountWith(AskCard, { cid: "c1", pid: "p1", question: q });
    expect(w.text()).toContain("用哪个？");
    await w.findAll("button[role=radio]")[1].trigger("click");
    await flushPromises();
    const req = fetch.mock.calls[0][0] as Request;
    expect(req.url).toContain("/api/conversations/c1/questions/q1/answer");
    expect(req.headers.get("X-CSRF-Token")).toBe("tok");
    expect(req.headers.get("Idempotency-Key")).toBeTruthy();
    expect(await req.json()).toMatchObject({ answer: "B 方案" });
  });
  it("shows the chosen row once answered", () => {
    const q = question("q1", { text: "用哪个？", options: ["A 方案", "B 方案"], status: "answered", answer: "B 方案" });
    const w = mountWith(AskCard, { cid: "c1", pid: "p1", question: q });
    expect(w.find("[aria-label='已选']").exists()).toBe(true);
    expect(w.text()).toContain("B 方案");
    expect(w.findAll("button[role=radio]")).toHaveLength(0);
  });
  it("lets long links wrap (at-wrap) in open and answered cards without touching the text", () => {
    const url = `https://claude.com/cai/oauth/authorize?state=${"a1".repeat(80)}`;
    for (const extra of [{}, { status: "answered", answer: url }] as const) {
      const w = mountWith(AskCard, { cid: "c1", pid: "p1", question: question("q1", { text: `打开 ${url}`, options: [url, "B"], ...extra }) });
      expect(w.get("section").classes()).toContain("at-wrap");
      expect(w.text()).toContain(url);
      expect(w.html()).not.toMatch(/<wbr|\u200b|\u00ad/);
    }
  });
});

describe("ThreadList", () => {
  const idle = { members: [{ id: "e", name: "lead", tier: "lead", status: { state: "idle" } }] } as unknown as Partial<ThreadView>;
  const paused = { ...idle, status: "paused" } as Partial<ThreadView>;
  it("groups threads into chips with counts; archived starts folded and folding is reported", async () => {
    const w = mountWith(ThreadList, {
      threads: [thread(1, paused), thread(2, { status: "done" }), thread(4, { status: "failed" }), thread(3, { status: "archived", summary: "旧的" })],
    });
    const chips = w.findAll("section[data-group] > button").map((b) => b.text());
    expect(chips).toEqual(["已暂停 · 1", "已完成 · 2", "已归档 · 1"]);
    expect(w.find("[data-group=idle] [data-mark=paused]").exists()).toBe(true);
    expect(w.find("[data-group=done] [data-mark=failed]").exists()).toBe(true);
    expect(w.text()).toContain("#1 T1");
    expect(w.text()).not.toContain("#3 T3");
    await w.get("[data-group=archived] > button").trigger("click");
    expect(w.emitted("fold")).toEqual([["archived", false]]);
    await w.setProps({ folded: { archived: false, done: true } });
    expect(w.text()).toContain("#3 T3");
    expect(w.text()).not.toContain("#2 T2");
    expect(w.get("[data-group=done] > button").attributes("aria-expanded")).toBe("false");
    await w.findAll("button").find((b) => b.text().includes("#1 T1"))!.trigger("click");
    expect(w.emitted("open")).toEqual([[1]]);
  });
  it("searching opens folded groups and says when nothing matches", async () => {
    const w = mountWith(ThreadList, { threads: [thread(1, { status: "done", title: "部署" }), thread(2, { status: "done" })], folded: { done: true } });
    expect(w.text()).not.toContain("#1 部署");
    await w.setProps({ filter: { q: "部署", groups: [], withDeliverable: false } });
    expect(w.text()).toContain("#1 部署");
    expect(w.text()).not.toContain("#2 T2");
    await w.setProps({ filter: { q: "没有", groups: [], withDeliverable: false } });
    expect(w.text()).toContain("没有符合条件的讨论串");
  });
  it("rich rows (进行中) tag the plan and the deliverables; compact rows (已暂停) are one line with the last activity", () => {
    const d = { id: "d1", project_id: "p1", thread_no: 1, kind: "doc", title: "说明", url: "https://x.dev/a", detail: {}, published_by: "dev", created_at: "", updated_at: "" };
    const working = { members: [{ id: "e", name: "lead", tier: "lead", node: { id: "n1", name: "rc", online: true }, status: { state: "typing" } }] } as unknown as Partial<ThreadView>;
    const w = mountWith(ThreadList, {
      threads: [
        thread(2, { ...working, latest_deliverable: d, counts: { ...thread(2).counts, deliverables: 3, main_total: 7, main_done: 2 } }),
        thread(1, { ...paused, latest_deliverable: d, last_activity_at: new Date(Date.now() - 51 * 60_000).toISOString() }),
      ],
    });
    const rich = w.get("[data-group=active] [data-row]");
    expect(rich.attributes("data-compact")).toBeUndefined();
    expect(rich.get("[data-mark]").attributes("data-mark")).toBe("working");
    expect(rich.get("[data-node]").attributes("data-node")).toBe("online");
    expect(rich.get("[data-tags]").text()).toContain("2/7");
    expect(rich.get("[data-deliverables]").text()).toBe("3 个交付物");
    expect(rich.get("[data-deliverables]").attributes("href")).toBe("/projects/p1/threads/2/deliverables");
    expect(rich.find("a[href='https://x.dev/a']").exists()).toBe(false);
    const compact = w.get("[data-group=idle] [data-row]");
    expect(compact.attributes("data-compact")).toBe("");
    expect(compact.find("[data-tags]").exists()).toBe(false);
    expect(compact.find("[data-node]").exists()).toBe(false);
    expect(compact.findAll("button span.truncate").map((s) => s.text())).toEqual(["#1 T1"]);
    expect(compact.get("time").text()).toBe("51分钟");
  });
  it("the #20 case (running, lead idle, no task left): list row, header mark, bottom line and card agree on 等你处理", () => {
    const t20 = thread(20, { ...idle, counts: { ...thread(20).counts, tasks_total: 2, tasks_done: 2, tasks_open: 0 }, lead_last: { text: "改好了，你看看", at: "" } });
    const list = mountWith(ThreadList, { threads: [t20] });
    expect(list.findAll("section[data-group] > button").map((b) => b.text())).toEqual(["等你处理 · 1"]);
    const row = list.get("[data-group=waiting] [data-row]");
    expect(row.get("[data-mark]").attributes("aria-label")).toBe("等你处理");
    expect(row.text()).toContain("组长已回复：改好了，你看看");
    const header = mountWith(ThreadMark, { thread: t20, mark: threadMark(t20) });
    expect(header.get("[data-mark]").attributes()).toMatchObject({ "data-mark": "wait", "aria-label": "等你处理" });
    const bottom = mountWith(TypingLine, { thread: t20 });
    expect(bottom.text()).toBe("组长已回复，等你回复");
    expect(bottom.text()).not.toContain("讨论串进行中");
    expect(mountWith(TypingLine, { thread: t20, session: true }).text()).toBe("员工已回复，等你回复");
    expect(bottom.find(".at-spin").exists()).toBe(false);
    expect(cardLook(t20).label).toBe("等你处理");
  });
  it("says what to do when there are none", () => {
    expect(mountWith(ThreadList, { threads: [] }).text()).toContain("还没有讨论串");
  });
});

describe("Markdown", () => {
  it("renders GFM with the prose classes and merges the caller's", () => {
    const w = mount(Markdown, { props: { source: "**hi** see https://a.dev/x。好", class: "text-sm leading-7" } });
    expect(w.find("strong").text()).toBe("hi");
    expect(w.find("a").attributes("href")).toBe("https://a.dev/x");
    expect(w.classes()).toContain("prose");
    expect(w.classes()).toContain("leading-7");
    expect(w.classes()).not.toContain("leading-6");
  });
  it("wraps long links only through CSS: the text and href stay exactly the link", () => {
    const url = "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9&redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback&state=f3a9c1e7b5d2a8c4e6f0";
    const w = mount(Markdown, { props: { source: `看 ${url}\n\n\`${"x".repeat(120)}\`` } });
    expect(w.classes()).toContain("at-wrap");
    expect(w.find("a").text()).toBe(url);
    expect(w.find("a").attributes("href")).toBe(url);
    expect(w.html()).not.toMatch(/<wbr|\u200b|\u00ad|\u2060/);
  });
});
