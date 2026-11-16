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
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Markdown from "@/components/Markdown.vue";
import { useDraft } from "@/lib/persist";
import { question, thread } from "./fixtures";

function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[];
}

const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });

beforeEach(() => window.localStorage.clear());
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
    expect(onSend).toHaveBeenCalledWith("hello", []);
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
    const key = { value: "draft:a" };
    const Host = defineComponent({ setup: () => ((draft = useDraft(() => key.value)), () => h("div")) });
    mount(Host);
    draft.value = "typed";
    vi.advanceTimersByTime(300);
    expect(window.localStorage.getItem("draft:a")).toBe("typed");
    mount(Host);
    expect(draft.value).toBe("typed");
    draft.value = "";
    expect(window.localStorage.getItem("draft:a")).toBeNull();
    vi.useRealTimers();
  });
});

describe("attachments", () => {
  it("uploads picked images, refuses others, reports ids when done", async () => {
    URL.createObjectURL = vi.fn(() => "blob:x");
    URL.revokeObjectURL = vi.fn();
    let a!: ReturnType<typeof useAttachments>;
    let finish!: (v: { id: string }) => void;
    const uploader = vi.fn(() => new Promise<{ id: string }>((r) => (finish = r)));
    const Host = defineComponent({ setup: () => ((a = useAttachments("p1", uploader)), () => h("div")) });
    mountWith(Host);
    a.add([new File(["x"], "a.png", { type: "image/png" }), new File(["x"], "b.txt", { type: "text/plain" })]);
    expect(a.items.value).toHaveLength(1);
    expect(a.ids()).toBeNull();
    expect(a.uploading.value).toBe(true);
    finish({ id: "att1" });
    await flushPromises();
    expect(a.ids()).toEqual(["att1"]);
    expect(uploader).toHaveBeenCalledWith("p1", expect.any(File));
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
});

describe("ThreadList", () => {
  it("groups threads and folds archived ones", async () => {
    const w = mountWith(ThreadList, {
      threads: [thread(1), thread(2, { status: "done" }), thread(3, { status: "archived", summary: "旧的" })],
    });
    expect(w.text()).toContain("#1 T1");
    expect(w.text()).toContain("#2 T2");
    expect(w.text()).not.toContain("#3 T3");
    await w.get("button[aria-expanded=false]").trigger("click");
    expect(w.text()).toContain("#3 T3");
    await w.findAll("button").find((b) => b.text().includes("#1 T1"))!.trigger("click");
    expect(w.emitted("open")).toEqual([[1]]);
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
});
