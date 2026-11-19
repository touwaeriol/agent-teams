// @vitest-environment jsdom
// Sending from the message boxes: one message is sent once however fast Enter / the button are
// hit; the box empties at once (text, images, @ references) so what is typed or attached while
// it is sending is kept; a failed send puts the message back in front of it. A second stop of a
// voice recording waits for the first, so a send never goes out ahead of the last words. The
// lead's box keeps its unsent text and images when the thread is resolved, archived, reopened.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { useAttachments, type Attachments } from "@/components/app/attachments";
import Composer from "@/components/app/Composer.vue";
import LeadComposer from "@/components/app/thread/LeadComposer.vue";
import { resetDraftMemory } from "@/lib/persist";
import { VoiceController, type VoiceDeps, type VoiceState } from "@/lib/voice/controller";
import type { TransportHandlers, VoiceSessionInfo } from "@/lib/voice/openai";
import { thread } from "./fixtures";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function stubApi(onSend: () => Promise<Response> | Response = () => json({ id: "m1" })) {
  const sends: { content: string; attachments?: string[] }[] = [];
  let n = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input instanceof Request ? input : new Request(String(input));
    const path = new URL(req.url, "http://x").pathname;
    if (path === "/api/me") return json({ id: "u1", email: "u1@x", name: "u1" });
    if (req.method === "POST" && path.endsWith("/attachments")) return json({ id: `att${++n}` });
    if (req.method === "POST" && path.endsWith("/messages")) {
      sends.push(await req.clone().json());
      return onSend();
    }
    if (path.endsWith("/messages")) return json({ items: [], next_cursor: null });
    return json({});
  });
  return sends;
}

let client: QueryClient;
const plugins = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: client }], router, ElementPlus] as never[];
};
const png = (name = "a.png") => new File(["x"], name, { type: "image/png" });
const boxOf = (w: VueWrapper) => w.get<HTMLTextAreaElement>("textarea");
const thumbs = (w: VueWrapper) => w.findAll("[aria-label=附件] img").map((i) => i.attributes("alt"));
const enter = (w: VueWrapper) => boxOf(w).trigger("keydown", { key: "Enter" });

/** A Composer with keyed attachments, as the coordinator and lead boxes use it. */
function mountBox(onSend: (text: string, images: string[]) => Promise<unknown>) {
  let a!: Attachments;
  const Host = defineComponent({
    setup() {
      a = useAttachments(() => "p1", async (_pid, f) => ({ id: `id-${f.name}` }), () => "draft:v2:u1:p1:c9");
      return () => h(Composer, { label: "x", placeholder: "x", onSend, attachments: a, draftKey: "draft:v2:u1:p1:c9" });
    },
  });
  const w = mount(Host, { global: { plugins: plugins() }, attachTo: document.body });
  return { w, a: () => a };
}

/** A send that stays open until settled by hand. */
function gate() {
  let settle!: (ok: boolean) => void;
  const calls: [string, string[]][] = [];
  const onSend = vi.fn((text: string, images: string[]) => {
    calls.push([text, images]);
    return new Promise((resolve, reject) => (settle = (ok) => (ok ? resolve({}) : reject(new Error("x")))));
  });
  return { onSend, calls, settle: (ok: boolean) => settle(ok) };
}

beforeEach(() => {
  window.localStorage.clear();
  resetDraftMemory();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  URL.createObjectURL = vi.fn((f: Blob) => `blob:${(f as File).name}`);
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("sending fast", () => {
  it("Enter twice (and the button) send a message with images once", async () => {
    stubApi();
    const g = gate();
    const { w, a } = mountBox(g.onSend);
    await flushPromises();
    await boxOf(w).setValue("看这张图");
    a().add([png()]);
    await flushPromises();
    void enter(w);
    void enter(w);
    void w.get("form").trigger("submit");
    await flushPromises();
    expect(g.calls).toEqual([["看这张图", ["id-a.png"]]]);
    // Nothing is left in the box to be sent a second time.
    expect(boxOf(w).element.value).toBe("");
    expect(thumbs(w)).toEqual([]);
    g.settle(true);
    await flushPromises();
    void enter(w);
    await flushPromises();
    expect(g.onSend).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:a.png");
  });

  it("keeps what is typed and attached while sending; a failure puts the message back in front", async () => {
    stubApi();
    const g = gate();
    const { w, a } = mountBox(g.onSend);
    await flushPromises();
    await boxOf(w).setValue("第一条");
    a().add([png("a.png")]);
    await flushPromises();
    void enter(w);
    await flushPromises();
    await boxOf(w).setValue("接着写的");
    a().add([png("b.png")]);
    await flushPromises();
    g.settle(false);
    await flushPromises();
    expect(boxOf(w).element.value).toBe("第一条\n接着写的");
    expect(thumbs(w)).toEqual(["a.png", "b.png"]);
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  });

  it("a successful send leaves what was typed meanwhile alone", async () => {
    stubApi();
    const g = gate();
    const { w } = mountBox(g.onSend);
    await flushPromises();
    await boxOf(w).setValue("第一条");
    void enter(w);
    await flushPromises();
    await boxOf(w).setValue("第二条还没发");
    g.settle(true);
    await flushPromises();
    expect(boxOf(w).element.value).toBe("第二条还没发");
  });

  it("the lead's box: a double Enter posts one message", async () => {
    let release!: () => void;
    const sends = stubApi(() => new Promise((r) => (release = () => r(json({ id: "m1" })))));
    const w = mount(LeadComposer, { props: { pid: "p1", thread: thread(1) }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    await boxOf(w).setValue("只发一次");
    void enter(w);
    void enter(w);
    await flushPromises();
    release();
    await flushPromises();
    expect(sends.map((s) => s.content)).toEqual(["只发一次"]);
    expect(boxOf(w).element.value).toBe("");
  });
});

describe("voice stop while stopping", () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] }));

  it("a second stop waits for the last words of the first", async () => {
    let handlers!: TransportHandlers;
    const state: VoiceState = { phase: "idle", level: 0, error: "" };
    const stream = { getTracks: () => [{ stop: () => {} }], getAudioTracks: () => [] } as unknown as MediaStream;
    const session: VoiceSessionInfo = {
      provider: "openai", transport: "webrtc", connect_url: "x", client_secret: "ek", expires_at: Math.floor(Date.now() / 1000) + 120, model: "m", language: "auto", client_commits: true,
    };
    const deps: VoiceDeps = {
      mint: async () => session,
      openMic: async () => stream,
      connect: async (_s, _st, hs) => ((handlers = hs), { send: () => {}, close: () => {} }),
      meter: () => () => {},
      now: () => Date.now(),
      onText: () => {},
      tailMs: 0,
    };
    const c = new VoiceController(state, deps);
    await c.start({ text: "", selStart: 0, selEnd: 0, language: "zh", deviceId: "" });
    handlers.onEvent({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "最后" });
    const first = c.stop();
    await vi.advanceTimersByTimeAsync(0);
    expect(state.phase).toBe("stopping");
    let secondDone = false;
    const second = c.stop().then(() => (secondDone = true));
    await vi.advanceTimersByTimeAsync(0);
    expect(secondDone).toBe(false);
    handlers.onEvent({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "最后一句。" });
    await first;
    await second;
    expect(secondDone).toBe(true);
    expect(c.transcript.value).toBe("最后一句。");
    expect(state.phase).toBe("idle");
  });
});

describe("the lead's box across thread status changes", () => {
  it("keeps unsent text and images when the thread is resolved, archived, unarchived and reopened", async () => {
    stubApi();
    const w = mount(LeadComposer, { props: { pid: "p1", thread: thread(1) }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    await boxOf(w).setValue("还没发");
    const paste = Object.assign(new Event("paste", { bubbles: true }), { clipboardData: { files: [png()] } });
    w.get("form").element.dispatchEvent(paste);
    await flushPromises();
    expect(thumbs(w)).toEqual(["a.png"]);
    for (const status of ["done", "archived", "running", "failed", "running"] as const) {
      await w.setProps({ thread: thread(1, { status }) });
      await flushPromises();
      if (status === "archived") {
        expect(w.find("textarea").exists()).toBe(false);
        continue;
      }
      expect(boxOf(w).element.value).toBe("还没发");
      expect(thumbs(w)).toEqual(["a.png"]);
    }
    // Leaving the thread and coming back (the panel remounts) keeps them too.
    w.unmount();
    const again = mount(LeadComposer, { props: { pid: "p1", thread: thread(1) }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    expect(boxOf(again).element.value).toBe("还没发");
    expect(thumbs(again)).toEqual(["a.png"]);
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  });

  it("an archived thread's note says the unsent draft is kept", async () => {
    stubApi();
    const w = mount(LeadComposer, { props: { pid: "p1", thread: thread(2) }, global: { plugins: plugins() }, attachTo: document.body });
    await flushPromises();
    await w.setProps({ thread: thread(2, { status: "archived" }) });
    expect(w.get("[data-testid=closed-note]").text()).not.toContain("未发送的内容会保留");
    await w.setProps({ thread: thread(2) });
    await boxOf(w).setValue("还没发");
    await w.setProps({ thread: thread(2, { status: "archived" }) });
    expect(w.get("[data-testid=closed-note]").text()).toContain("未发送的内容会保留");
  });
});
