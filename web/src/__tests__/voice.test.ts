// @vitest-environment jsdom
// Voice input: transcript insertion, the recording state machine (mocked transport, mic and
// meter), preferences, and the composer showing the mic only when voice is configured.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Composer from "@/components/app/Composer.vue";
import { MAX_UTTERANCE_MS, SETTLE_MS, SILENCE_MS, VoiceController, type VoiceDeps, type VoiceState } from "@/lib/voice/controller";
import { ConnectError, type RealtimeEvent, type TransportHandlers, type VoiceSessionInfo } from "@/lib/voice/openai";
import { filterLanguages, languageLabel, prefsKey, readPrefs, writePrefs } from "@/lib/voice/prefs";
import { joiner, Transcript } from "@/lib/voice/transcript";

describe("Transcript", () => {
  it("inserts at the cursor, grey partials, finals replace them", () => {
    const t = new Transcript();
    t.begin("你好世界", 2);
    t.delta("a", "今天");
    expect(t.value).toBe("你好今天世界");
    expect(t.parts).toEqual([
      { text: "你好", partial: false },
      { text: "今天", partial: true },
      { text: "世界", partial: false },
    ]);
    expect(t.caret).toBe(4);
    t.complete("a", "今天天气");
    t.delta("b", "不错");
    expect(t.value).toBe("你好今天天气不错世界");
    expect(t.pending).toBe(true);
    t.complete("b", "不错。");
    expect(t.pending).toBe(false);
    expect(t.parts.every((p) => !p.partial)).toBe(true);
  });
  it("replaces a selection and spaces Latin words", () => {
    const t = new Transcript();
    t.begin("Please fix THIS now", 11, 15);
    t.complete("a", " the bug");
    expect(t.value).toBe("Please fix the bug now");
    t.begin("Hi", 2);
    t.complete("x", "there");
    t.complete("y", "friend.");
    expect(t.value).toBe("Hi there friend.");
    expect(joiner("中文", "abc")).toBe("");
  });
  it("keeps spoken order by committed item, drops failed items and cancelled partials", () => {
    const t = new Transcript();
    t.begin("");
    t.committed("a");
    t.committed("b");
    t.complete("b", "second");
    t.complete("a", "first");
    expect(t.value).toBe("first second");
    t.delta("c", "maybe");
    t.drop("b");
    t.dropPartials();
    expect(t.value).toBe("first");
  });
});

interface Fake {
  deps: VoiceDeps;
  state: VoiceState;
  sent: Record<string, unknown>[];
  handlers: () => TransportHandlers;
  level: (l: number) => void;
  mints: string[];
  closed: () => number;
  micStopped: () => number;
  texts: string[];
  emit: (e: RealtimeEvent) => void;
}

const session = (over: Partial<VoiceSessionInfo> = {}): VoiceSessionInfo => ({
  provider: "openai",
  transport: "webrtc",
  connect_url: "https://api.openai.com/v1/realtime/calls",
  client_secret: "ek_1",
  expires_at: Math.floor(Date.now() / 1000) + 120,
  model: "gpt-realtime-whisper",
  language: "auto",
  client_commits: true,
  ...over,
});

function fake(opts: { connect?: (n: number) => void; mic?: () => never; mintAt?: (n: number) => Partial<VoiceSessionInfo> } = {}): Fake {
  const sent: Record<string, unknown>[] = [];
  let h: TransportHandlers | null = null;
  let onLevel: (l: number) => void = () => {};
  const mints: string[] = [];
  let closed = 0;
  let micStopped = 0;
  let connects = 0;
  const texts: string[] = [];
  const state: VoiceState = { phase: "idle", level: 0, error: "" };
  const stream = { getTracks: () => [{ stop: () => micStopped++ }], getAudioTracks: () => [] } as unknown as MediaStream;
  const deps: VoiceDeps = {
    mint: async (language) => (mints.push(language), session(opts.mintAt?.(mints.length) ?? {})),
    openMic: async () => (opts.mic ? opts.mic() : stream),
    connect: async (_s, _st, handlers) => {
      opts.connect?.(++connects);
      h = handlers;
      return { send: (e) => sent.push(e), close: () => closed++ };
    },
    meter: (_s, cb) => ((onLevel = cb), () => {}),
    now: () => Date.now(),
    onText: (t) => texts.push(t.value),
    tailMs: 0,
  };
  return {
    deps,
    state,
    sent,
    handlers: () => h as TransportHandlers,
    level: (l) => onLevel(l),
    mints,
    closed: () => closed,
    micStopped: () => micStopped,
    texts,
    emit: (e) => (h as TransportHandlers).onEvent(e),
  };
}

const startOpts = { text: "", selStart: 0, selEnd: 0, language: "zh", deviceId: "" };

describe("VoiceController", () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] }));
  afterEach(() => vi.useRealTimers());

  it("connects, renders deltas then finals, commits after silence and on stop", async () => {
    const f = fake();
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    expect(f.state.phase).toBe("recording");
    expect(f.mints).toEqual(["zh"]);
    f.level(0.3);
    f.emit({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "你好" });
    expect(c.transcript.parts).toEqual([{ text: "你好", partial: true }]);
    vi.advanceTimersByTime(SILENCE_MS);
    f.level(0.01);
    expect(f.sent).toEqual([{ type: "input_audio_buffer.commit" }]);
    f.emit({ type: "input_audio_buffer.committed", item_id: "i1" });
    f.emit({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "你好，" });
    f.level(0.4);
    f.emit({ type: "conversation.item.input_audio_transcription.delta", item_id: "i2", delta: "再见" });
    const stopping = c.stop();
    await vi.advanceTimersByTimeAsync(0);
    expect(f.state.phase).toBe("stopping");
    expect(f.sent).toHaveLength(2);
    f.emit({ type: "conversation.item.input_audio_transcription.completed", item_id: "i2", transcript: "再见。" });
    await stopping;
    expect(f.state.phase).toBe("idle");
    expect(c.transcript.value).toBe("你好，再见。");
    expect(f.closed()).toBe(1);
    expect(f.micStopped()).toBe(1);
  });

  it("commits long utterances without a pause", async () => {
    const f = fake();
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    f.level(0.5);
    vi.advanceTimersByTime(MAX_UTTERANCE_MS);
    f.level(0.5);
    expect(f.sent).toHaveLength(1);
    c.cancel();
  });

  it("does not commit for server-VAD models", async () => {
    const f = fake({ mintAt: () => ({ client_commits: false }) });
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    f.level(0.5);
    vi.advanceTimersByTime(SILENCE_MS * 2);
    f.level(0);
    const p = c.stop();
    await vi.advanceTimersByTimeAsync(0);
    await p;
    expect(f.sent).toEqual([]);
  });

  it("keeps partial text when the last final never comes", async () => {
    const f = fake();
    const c = new VoiceController(f.state, f.deps);
    await c.start({ ...startOpts, text: "说：" , selStart: 3, selEnd: 3 });
    f.emit({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "未完" });
    const p = c.stop();
    await vi.advanceTimersByTimeAsync(SETTLE_MS + 10);
    await p;
    expect(c.transcript.value).toBe("说：未完");
    expect(c.transcript.pending).toBe(false);
    expect(f.state.phase).toBe("idle");
  });

  it("cancel drops grey text", async () => {
    const f = fake();
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    f.emit({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "丢掉" });
    c.cancel();
    expect(c.transcript.value).toBe("");
    expect(f.texts.at(-1)).toBe("");
    expect(f.state.phase).toBe("idle");
  });

  it("re-mints once when the client secret is rejected (expired)", async () => {
    const f = fake({ connect: (n) => { if (n === 1) throw new ConnectError(401, "expired"); } });
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    expect(f.mints).toHaveLength(2);
    expect(f.state.phase).toBe("recording");
    c.cancel();
  });

  it("re-mints a secret that is already expired before connecting", async () => {
    const f = fake({ mintAt: (n) => (n === 1 ? { expires_at: 1 } : {}) });
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    expect(f.mints).toHaveLength(2);
    c.cancel();
  });

  it("reports permission denied, network errors and dropped connections", async () => {
    const denied = fake({ mic: () => { throw Object.assign(new Error("x"), { name: "NotAllowedError" }); } });
    await new VoiceController(denied.state, denied.deps).start(startOpts);
    expect(denied.state).toMatchObject({ phase: "error" });
    expect(denied.state.error).toContain("麦克风");

    const noDevice = fake({ mic: () => { throw Object.assign(new Error("x"), { name: "NotFoundError" }); } });
    await new VoiceController(noDevice.state, noDevice.deps).start(startOpts);
    expect(noDevice.state.error).toBe("没有找到麦克风");

    const down = fake({ connect: () => { throw new ConnectError(0, "连不上语音服务"); } });
    await new VoiceController(down.state, down.deps).start(startOpts);
    expect(down.state).toMatchObject({ phase: "error", error: "连不上语音服务" });
    expect(down.micStopped()).toBe(1);

    const f = fake();
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    f.emit({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "留下" });
    f.handlers().onClose();
    expect(f.state).toMatchObject({ phase: "error", error: "和语音服务的连接断开了" });
    expect(c.transcript.value).toBe("留下");
    c.reset();
    expect(f.state.phase).toBe("idle");
  });

  it("mints while the microphone opens (WebRTC needs the track, so it connects after)", async () => {
    const f = fake();
    let micOpen: (s: MediaStream) => void = () => {};
    const stream = { getTracks: () => [{ stop() {} }], getAudioTracks: () => [] } as unknown as MediaStream;
    f.deps.openMic = () => new Promise((r) => (micOpen = r));
    let gotStream: MediaStream | null = null;
    const connect = f.deps.connect;
    f.deps.connect = async (s, mic, h) => ((gotStream = await mic), connect(s, mic, h));
    const c = new VoiceController(f.state, f.deps);
    const started = c.start(startOpts);
    await vi.advanceTimersByTimeAsync(0);
    expect(f.mints).toEqual(["zh"]);
    expect(f.state.phase).toBe("connecting");
    micOpen(stream);
    await started;
    expect(gotStream).toBe(stream);
    expect(f.state.phase).toBe("recording");
    c.cancel();
  });

  it("ignores empty commits and surfaces other errors as notices", async () => {
    const f = fake();
    const notices: string[] = [];
    f.deps.onNotice = (m) => notices.push(m);
    const c = new VoiceController(f.state, f.deps);
    await c.start(startOpts);
    f.emit({ type: "error", error: { code: "input_audio_buffer_commit_empty", message: "buffer too small" } });
    f.emit({ type: "conversation.item.input_audio_transcription.failed", item_id: "i9" });
    expect(notices).toEqual(["有一段语音没有识别出来"]);
    expect(f.state.phase).toBe("recording");
    c.cancel();
  });
});

describe("voice prefs", () => {
  beforeEach(() => window.localStorage.clear());
  afterEach(() => vi.restoreAllMocks());
  it("persist per user and survive broken storage", () => {
    expect(readPrefs("u1")).toEqual({ deviceId: "", language: "auto", pushToTalk: false });
    writePrefs("u1", { deviceId: "mic-2", language: "ja", pushToTalk: true });
    expect(readPrefs("u1")).toEqual({ deviceId: "mic-2", language: "ja", pushToTalk: true });
    expect(readPrefs("u2").language).toBe("auto");
    window.localStorage.setItem(prefsKey("u3"), "{not json");
    expect(readPrefs("u3").pushToTalk).toBe(false);
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(readPrefs("u1").language).toBe("auto");
    expect(() => writePrefs("u1", { deviceId: "", language: "en", pushToTalk: false })).not.toThrow();
  });
  it("searches languages by code, own name and Chinese name", () => {
    const list = [
      { code: "zh", name: "中文", zh: "中文" },
      { code: "en", name: "English", zh: "英语" },
      { code: "ja", name: "日本語", zh: "日语" },
    ];
    expect(filterLanguages(list, "").length).toBe(3);
    expect(filterLanguages(list, "eng").map((l) => l.code)).toEqual(["en"]);
    expect(filterLanguages(list, "日").map((l) => l.code)).toEqual(["ja"]);
    expect(filterLanguages(list, "ja").map((l) => l.code)).toEqual(["ja"]);
    expect(languageLabel(list, "auto")).toBe("自动");
    expect(languageLabel(list, "en")).toBe("English");
  });
});

describe("Composer mic button", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });
  const mountComposer = (enabled: boolean, variant: "large" | "inline" = "large") => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input instanceof Request ? input.url : input);
      const body = url.includes("/api/voice/config")
        ? { enabled, languages: enabled ? [{ code: "zh", name: "中文", zh: "中文" }] : [] }
        : { id: "u1", email: "a@b.c", role: "user" };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    return mount(Composer, {
      props: { label: "x", placeholder: "x", onSend: vi.fn(), variant },
      global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }]] as never[] },
      attachTo: document.body,
    });
  };
  it("shows the mic only when voice is configured", async () => {
    const on = mountComposer(true);
    await flushPromises();
    expect(on.find("[data-testid=voice-mic]").exists()).toBe(true);
    const inline = mountComposer(true, "inline");
    await flushPromises();
    expect(inline.find("[data-testid=voice-mic]").exists()).toBe(true);
    const off = mountComposer(false);
    await flushPromises();
    expect(off.find("[data-testid=voice-mic]").exists()).toBe(false);
  });
});
