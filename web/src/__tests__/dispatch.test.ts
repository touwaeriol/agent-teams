// @vitest-environment jsdom
// The first message of a thread chat: the brief the thread was opened with (Markdown, folded when
// long), the user's words it came from (text, thumbnails, link back to the project chat), and the
// "组长正在阅读任务…" line until the lead's first message.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type { MessageView, ThreadDispatch, ThreadView } from "@/api/views";
import DispatchMessage from "@/components/app/thread/DispatchMessage.vue";
import Markdown from "@/components/Markdown.vue";
import { briefFoldable, dispatchSpeaker, leadReading } from "@/components/app/thread/detail";
import ThreadChat from "@/components/app/thread/ThreadChat.vue";
import { message, thread } from "./fixtures";

async function plugins(path = "/projects/p1/threads/12") {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  await router.push(path);
  return [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[];
}

afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const longBrief = ["## 目标", "", ...Array.from({ length: 10 }, (_, i) => `- 第 ${i + 1} 条要求`)].join("\n");
const BRIEF = "把 **登录页** 改成暗色";
const dispatch = (patch: Partial<ThreadDispatch> = {}): ThreadDispatch => ({
  opened_by: "coordinator",
  at: "2026-11-17T10:00:00Z",
  source: null,
  ...patch,
});
const source = {
  message_id: 41,
  content: "登录页太亮了，照这张图改",
  attachments: [{ id: "att1", name: "截图.png", content_type: "image/png", size: 10 }],
  created_at: "2026-11-17T09:59:00Z",
};

const mountDispatch = async (d: ThreadDispatch, path?: string, brief = BRIEF) =>
  mount(DispatchMessage, { props: { brief, dispatch: d, pid: "p1", compact: true }, global: { plugins: await plugins(path) } });

describe("helpers", () => {
  it("names who handed over the brief", () => {
    expect(dispatchSpeaker("coordinator")).toBe("统筹 → 组长");
    expect(dispatchSpeaker("user")).toBe("你 → 组长");
  });
  it("folds briefs longer than six lines or one long paragraph", () => {
    expect(briefFoldable("一行")).toBe(false);
    expect(briefFoldable(longBrief)).toBe(true);
    expect(briefFoldable("字".repeat(400))).toBe(true);
  });
  it("the lead reads until its first message (and not while older pages are unloaded)", () => {
    expect(leadReading([], false)).toBe(true);
    expect(leadReading([{ role: "user" }, { role: "system" }], false)).toBe(true);
    expect(leadReading([{ role: "user" }, { role: "leader" }], false)).toBe(false);
    expect(leadReading([], true)).toBe(false);
  });
});

describe("DispatchMessage", () => {
  it("renders the brief as Markdown under 统筹 → 组长", async () => {
    const w = await mountDispatch(dispatch());
    expect(w.text()).toContain("统筹 → 组长");
    expect(w.get("[data-brief] strong").text()).toBe("登录页");
    expect(w.find("[data-brief-toggle]").exists()).toBe(false);
    expect(w.get("[data-brief]").attributes("style") ?? "").not.toContain("max-height");
  });
  it("keeps the brief's single line endings as line breaks; other Markdown still soft-wraps", async () => {
    const lines = "目标：登录页改暗色\n约束：只改 web/\n验收：亮暗两套截图";
    const w = await mountDispatch(dispatch(), undefined, lines);
    const p = w.get("[data-brief] p");
    expect(p.findAll("br")).toHaveLength(2);
    expect(p.element.innerHTML.replace(/\n/g, "")).toBe("目标：登录页改暗色<br>约束：只改 web/<br>验收：亮暗两套截图");
    // Code blocks keep their own lines (no <br> inside).
    const code = await mountDispatch(dispatch(), undefined, "看这里：\n下一行\n```\na\nb\n```");
    expect(code.findAll("[data-brief] pre br")).toHaveLength(0);
    expect(code.findAll("[data-brief] p br")).toHaveLength(1);
    // Everywhere else a single line ending stays a soft wrap.
    const plain = mount(Markdown, { props: { source: lines } });
    expect(plain.findAll("br")).toHaveLength(0);
    expect(plain.get("p").text()).toBe("目标：登录页改暗色\n约束：只改 web/\n验收：亮暗两套截图");
  });
  it("folds by rendered lines: 6 one-line items fit, the 7th folds to 6 lines", async () => {
    const six = Array.from({ length: 6 }, (_, i) => `第 ${i + 1} 行`).join("\n");
    const fit = await mountDispatch(dispatch(), undefined, six);
    expect(fit.findAll("[data-brief] br")).toHaveLength(5);
    expect(fit.find("[data-brief-toggle]").exists()).toBe(false);
    const seven = await mountDispatch(dispatch(), undefined, `${six}\n第 7 行`);
    expect(seven.findAll("[data-brief] br")).toHaveLength(6);
    expect(seven.get("[data-brief]").attributes("style")).toContain(`max-height: ${6 * 26}px`);
  });
  it("says 你 → 组长 when the user opened the thread", async () => {
    const w = await mountDispatch(dispatch({ opened_by: "user" }));
    expect(w.text()).toContain("你 → 组长");
  });
  it("folds a long brief and toggles 展开 / 收起", async () => {
    const w = await mountDispatch(dispatch(), undefined, longBrief);
    const toggle = w.get("[data-brief-toggle]");
    expect(toggle.text()).toBe("展开");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(w.get("[data-brief]").attributes("style")).toContain("max-height: 156px");
    await toggle.trigger("click");
    expect(toggle.text()).toBe("收起");
    expect(w.get("[data-brief]").attributes("style") ?? "").not.toContain("max-height");
    await toggle.trigger("click");
    expect(toggle.text()).toBe("展开");
    expect(w.get("[data-brief]").attributes("style")).toContain("max-height");
  });
  it("quotes the user's words with thumbnails and a link back to the project chat", async () => {
    const w = await mountDispatch(dispatch({ source }));
    const quote = w.get("[data-dispatch-source]");
    expect(quote.text()).toContain("登录页太亮了，照这张图改");
    const img = quote.get("img");
    expect(img.attributes("src")).toBe("/api/attachments/att1");
    expect(img.element.closest("a")?.getAttribute("href")).toBe("/api/attachments/att1");
    // On the board the thread panel stays open; the project chat scrolls to ?msg=41.
    expect(quote.get("[data-dispatch-back]").attributes("href")).toBe("/projects/p1/threads/12?msg=41");
  });
  it("from the full-page thread the link goes back to the board", async () => {
    const w = await mountDispatch(dispatch({ source }), "/projects/p1/threads/12/full");
    expect(w.get("[data-dispatch-back]").attributes("href")).toBe("/projects/p1?msg=41");
  });
  it("shows no quote without a source", async () => {
    const w = await mountDispatch(dispatch());
    expect(w.find("[data-dispatch-source]").exists()).toBe(false);
    expect(w.find("[data-dispatch-back]").exists()).toBe(false);
    expect(w.find("img").exists()).toBe(false);
  });
});

describe("ThreadChat", () => {
  function serve(messages: MessageView[], questions: unknown[] = []) {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = (input as Request).url;
      const body = url.includes("/messages") ? { items: messages, next_cursor: null } : url.includes("/questions") ? questions : [];
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
  }
  async function mountChat(t: ThreadView) {
    const w = mount(ThreadChat, { props: { pid: "p1", thread: t, compact: true }, global: { plugins: await plugins() } });
    await flushPromises();
    await nextTick();
    return w;
  }
  const EMPTY = "还没有对话";

  it("shows the brief (thread.brief) first and 组长正在阅读任务… under it while the lead has not spoken", async () => {
    serve([message("1", { role: "user", content: "补充一句" })]);
    const w = await mountChat(thread(12, { brief: BRIEF, dispatch: dispatch() }));
    const html = w.html();
    expect(w.get("[data-dispatch] [data-brief] strong").text()).toBe("登录页");
    expect(w.get("[data-lead-reading]").text()).toBe("组长正在阅读任务…");
    expect(html.indexOf("data-dispatch")).toBeLessThan(html.indexOf("补充一句"));
    expect(html.indexOf("补充一句")).toBeLessThan(html.indexOf("data-lead-reading"));
    expect(w.text()).not.toContain(EMPTY);
  });
  it("a queued thread reads too", async () => {
    serve([]);
    const w = await mountChat(thread(12, { status: "queued", brief: BRIEF, dispatch: dispatch() }));
    expect(w.find("[data-lead-reading]").exists()).toBe(true);
  });
  it("drops the reading line once the lead's first message is there", async () => {
    serve([message("2", { role: "leader", content: "收到，开始拆任务。" })]);
    const w = await mountChat(thread(12, { brief: BRIEF, dispatch: dispatch() }));
    expect(w.find("[data-dispatch]").exists()).toBe(true);
    expect(w.find("[data-lead-reading]").exists()).toBe(false);
    expect(w.text()).toContain("收到，开始拆任务。");
  });
  it("no reading line once the lead asked (a question card)", async () => {
    const q = { id: "q1", thread_no: 12, asker: "lead-12", task_id: null, audience: "user", kind: "choice", text: "用哪个方案？", options: [], detail: {}, blocking: true, answer: null, status: "open", decided_by: null, created_at: "2026-11-17T10:01:00Z", answered_at: null };
    serve([], [q]);
    const w = await mountChat(thread(12, { brief: BRIEF, dispatch: dispatch() }));
    expect(w.text()).toContain("用哪个方案？");
    expect(w.find("[data-lead-reading]").exists()).toBe(false);
  });
  it.each(["waiting", "review", "paused", "done"] as const)("no reading line while %s", async (status) => {
    serve([]);
    const w = await mountChat(thread(12, { status, brief: BRIEF, dispatch: dispatch() }));
    expect(w.find("[data-dispatch]").exists()).toBe(true);
    expect(w.find("[data-lead-reading]").exists()).toBe(false);
    expect(w.text()).not.toContain(EMPTY);
  });
  it("an older thread with a dispatch shows it", async () => {
    serve([message("2", { role: "leader", content: "已完成" })]);
    const w = await mountChat(thread(9, { status: "done", brief: "旧任务", dispatch: dispatch() }));
    expect(w.get("[data-dispatch]").text()).toContain("旧任务");
  });
  it("an older thread with no dispatch and no messages keeps the empty state", async () => {
    serve([]);
    const w = await mountChat(thread(8, { status: "done", brief: "旧任务", dispatch: null }));
    expect(w.find("[data-dispatch]").exists()).toBe(false);
    expect(w.text()).toContain(EMPTY);
  });
  it("a forwarded thread (dispatch null) shows no brief; while running it reads instead of the empty state", async () => {
    serve([message("1", { role: "user", content: "转发来的原话" })]);
    const w = await mountChat(thread(8, { brief: "x", dispatch: null }));
    expect(w.find("[data-dispatch]").exists()).toBe(false);
    expect(w.find("[data-lead-reading]").exists()).toBe(true);
    expect(w.text()).not.toContain(EMPTY);
  });
});
