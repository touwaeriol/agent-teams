// @vitest-environment jsdom
// Voice relay: PCM framing/downsampling math, the WebSocket transport against a fake socket,
// connect timeouts, and the mic menu's device list (Chrome pseudo devices, duplicates, no labels).
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { reactive, ref } from "vue";
import type { VoiceInput } from "@/components/app/voice/useVoiceInput";
import VoiceButton from "@/components/app/voice/VoiceButton.vue";
import VoiceMenu from "@/components/app/voice/VoiceMenu.vue";
import { normalizeMics, selectedMic } from "@/lib/voice/audio";
import { VoiceController, type VoiceDeps, type VoiceState } from "@/lib/voice/controller";
import type { Transport, TransportHandlers } from "@/lib/voice/openai";
import { Framer, OUT_RATE, Resampler, toPcm16, type Capture } from "@/lib/voice/pcm";
import { makeConnectRelay, RELAY_PROTOCOL, relaySession, relayURL, type RelayDeps } from "@/lib/voice/relay";

describe("PCM", () => {
  it("halves 48 kHz by averaging pairs, the same however the input is split", () => {
    const input = Float32Array.from({ length: 4800 }, (_, i) => Math.sin(i / 7));
    const whole = new Resampler(48_000).push(input);
    expect(whole.length).toBe(2400);
    expect(whole[0]).toBeCloseTo((input[0] + input[1]) / 2, 6);
    expect(whole[100]).toBeCloseTo((input[200] + input[201]) / 2, 6);
    const r = new Resampler(48_000);
    const parts = [r.push(input.subarray(0, 127)), r.push(input.subarray(127, 1000)), r.push(input.subarray(1000))];
    const joined = Float32Array.from(parts.flatMap((p) => [...p]));
    expect(joined.length).toBe(2400);
    joined.forEach((v, i) => expect(v).toBeCloseTo(whole[i], 6));
  });

  it("keeps the rate exact for 44.1 kHz and 16 kHz and passes 24 kHz through", () => {
    const r44 = new Resampler(44_100);
    let n = 0;
    for (let i = 0; i < 100; i++) n += r44.push(new Float32Array(441).fill(0.5)).length; // one second in 10 ms blocks
    expect(Math.abs(n - OUT_RATE)).toBeLessThanOrEqual(1);
    const dc = new Resampler(44_100).push(new Float32Array(4410).fill(0.25));
    expect(dc.every((v) => Math.abs(v - 0.25) < 1e-6)).toBe(true);
    const up = new Resampler(16_000).push(Float32Array.from([0, 1, 0, 1, 0, 1, 0]));
    expect([...up.slice(0, 4)].map((v) => +v.toFixed(3))).toEqual([0, 0.667, 0.667, 0]);
    const same = Float32Array.from([0.1, 0.2, 0.3]);
    expect([...new Resampler(24_000).push(same)]).toEqual([...same]);
  });

  it("converts to clamped PCM16 and frames 100 ms little-endian", () => {
    expect([...toPcm16(Float32Array.from([0, 1, -1, 2, -2, 0.5]))]).toEqual([0, 32767, -32768, 32767, -32768, 16384]);
    const frames: ArrayBuffer[] = [];
    const f = new Framer((b) => frames.push(b));
    f.push(Int16Array.from({ length: 5000 }, (_, i) => i - 2500));
    expect(frames.map((b) => b.byteLength)).toEqual([4800, 4800]);
    const v = new DataView(frames[0]);
    expect(v.getInt16(0, true)).toBe(-2500);
    expect(new Uint8Array(frames[0])[2]).toBe((-2499 & 0xff) >>> 0);
    expect(v.getInt16(2, true)).toBe(-2499);
    f.flush();
    expect(frames.at(-1)?.byteLength).toBe(400);
    f.flush();
    expect(frames).toHaveLength(3);
  });
});

/** A WebSocket the test drives: records sends, lets the "server" send and close. */
class FakeSocket {
  static last: FakeSocket | null = null;
  binaryType = "blob";
  readyState = 0;
  sent: (string | ArrayBuffer)[] = [];
  closed = false;
  onmessage: ((m: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(
    public url: string,
    public protocols: string[],
  ) {
    FakeSocket.last = this;
  }
  send(d: string | ArrayBuffer) {
    this.sent.push(d);
  }
  close() {
    this.closed = true;
    this.readyState = 3;
  }
  // server side
  open() {
    this.readyState = 1;
  }
  server(msg: object) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
  drop() {
    this.readyState = 3;
    this.onclose?.();
  }
  json() {
    return this.sent.filter((d): d is string => typeof d === "string").map((d) => JSON.parse(d) as { type: string });
  }
}

function relayFixture(timeoutMs = 10_000) {
  let onFrame: (b: ArrayBuffer) => void = () => {};
  const capture = { flushes: 0, stopped: 0 };
  const deps: RelayDeps = {
    WebSocket: FakeSocket as unknown as RelayDeps["WebSocket"],
    capture: async (_s, cb) => {
      onFrame = cb;
      return {
        flush: async () => {
          capture.flushes++;
          onFrame(new Int16Array([7, 8]).buffer); // the buffered tail goes out before the commit
        },
        stop: () => capture.stopped++,
      } satisfies Capture;
    },
    url: (lang) => `wss://example.test/api/voice/stream?language=${lang}`,
    csrf: () => "tok123",
    timeoutMs,
  };
  const events: unknown[] = [];
  const closes: (string | undefined)[] = [];
  const h: TransportHandlers = { onEvent: (e) => events.push(e), onClose: (m) => closes.push(m) };
  const connect = makeConnectRelay(() => deps);
  const start = () => connect(relaySession("zh"), {} as MediaStream, h);
  return { start, events, closes, capture, frame: (b: ArrayBuffer) => onFrame(b) };
}

describe("relay transport", () => {
  afterEach(() => vi.useRealTimers());

  it("builds the URL", () => {
    expect(relayURL("zh", { protocol: "https:", host: "agent.example" })).toBe("wss://agent.example/api/voice/stream?language=zh");
    expect(relayURL("", { protocol: "http:", host: "localhost:5173" })).toBe("ws://localhost:5173/api/voice/stream?language=auto");
  });

  it("waits for ready, streams frames, flushes before commit, relays events, stops on close", async () => {
    const f = relayFixture();
    const p = f.start();
    const ws = FakeSocket.last as FakeSocket;
    expect(ws.protocols).toEqual([RELAY_PROTOCOL, "csrf.tok123"]);
    expect(ws.url).toContain("language=zh");
    expect(ws.binaryType).toBe("arraybuffer");
    ws.open();
    ws.server({ type: "conversation.item.input_audio_transcription.delta", item_id: "early", delta: "x" }); // before ready: ignored
    ws.server({ type: "ready", model: "gpt-realtime-whisper", client_commits: true, max_seconds: 300 });
    const t: Transport = await p;
    expect(t.clientCommits).toBe(true);
    expect(t.maxSeconds).toBe(300);
    f.frame(new ArrayBuffer(4800));
    t.send({ type: "input_audio_buffer.commit" });
    await flushPromises();
    expect(f.capture.flushes).toBe(1);
    expect(ws.sent.map((d) => (typeof d === "string" ? d : d.byteLength))).toEqual([4800, 4, '{"type":"commit"}']);
    ws.server({ type: "input_audio_buffer.committed", item_id: "i1" });
    ws.server({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "好" });
    expect(f.events).toEqual([
      { type: "input_audio_buffer.committed", item_id: "i1" },
      { type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "好" },
    ]);
    t.send({ type: "input_audio_buffer.commit" }); // the final commit on stop…
    t.close(); // …goes out before stop
    await flushPromises();
    expect(ws.json().slice(-2)).toEqual([{ type: "commit" }, { type: "stop" }]);
    expect(ws.closed).toBe(true);
    expect(f.capture.stopped).toBe(1);
    expect(f.closes).toEqual([]); // close() is not a drop
  });

  it("fails with the server's reason, or a clear one, instead of spinning", async () => {
    const busy = relayFixture();
    const p1 = busy.start();
    FakeSocket.last?.server({ type: "error", fatal: true, error: { code: "voice_too_many", message: "同时最多 2 路语音输入" } });
    await expect(p1).rejects.toThrow("连不上语音服务：同时最多 2 路语音输入");

    const refused = relayFixture();
    const p2 = refused.start();
    FakeSocket.last?.drop(); // e.g. 403 on the upgrade
    await expect(p2).rejects.toThrow(/^连不上语音服务：连接被拒绝或网络不通/);

    const upstream = relayFixture();
    const p3 = upstream.start();
    FakeSocket.last?.server({ type: "error", fatal: true, error: { code: "voice_key_rejected", message: "连不上语音服务：OpenAI 拒绝了 API Key" } });
    await expect(p3).rejects.toThrow(/^连不上语音服务：OpenAI 拒绝了 API Key$/);

    vi.useFakeTimers();
    const silent = relayFixture(10_000);
    const p4 = silent.start();
    const ws = FakeSocket.last as FakeSocket;
    const caught = p4.catch((e: Error) => e.message);
    vi.advanceTimersByTime(10_000);
    expect(await caught).toBe("连不上语音服务：10 秒内没有连上");
    expect(ws.closed).toBe(true);
  });

  it("reports a drop after ready with the server's reason", async () => {
    const f = relayFixture();
    const p = f.start();
    const ws = FakeSocket.last as FakeSocket;
    ws.open();
    ws.server({ type: "ready", client_commits: true });
    await p;
    ws.server({ type: "error", error: { code: "x", message: "非致命" } });
    ws.server({ type: "error", fatal: true, error: { code: "voice_max_duration", message: "单次录音最长 5 分钟" } });
    ws.drop();
    expect(f.events).toEqual([{ type: "error", error: { code: "x", message: "非致命" } }]);
    expect(f.closes).toEqual(["单次录音最长 5 分钟"]);
    expect(f.capture.stopped).toBe(1);
  });
});

describe("controller timeouts", () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] }));
  afterEach(() => vi.useRealTimers());

  const base = (connect: VoiceDeps["connect"]) => {
    const state: VoiceState = { phase: "idle", level: 0, error: "" };
    const notices: string[] = [];
    const deps: VoiceDeps = {
      mint: async (language) => relaySession(language),
      openMic: async () => ({ getTracks: () => [{ stop() {} }] }) as unknown as MediaStream,
      connect,
      meter: () => () => {},
      now: () => Date.now(),
      onText: () => {},
      onNotice: (m) => notices.push(m),
      tailMs: 0,
    };
    return { state, deps, notices };
  };

  it("fails a connection that never comes up within 10 s and closes it if it arrives later", async () => {
    let resolve: (t: Transport) => void = () => {};
    let closed = 0;
    const f = base(() => new Promise<Transport>((r) => (resolve = r)));
    const c = new VoiceController(f.state, f.deps);
    const started = c.start({ text: "", selStart: 0, selEnd: 0, language: "auto", deviceId: "" });
    await vi.advanceTimersByTimeAsync(9_999);
    expect(f.state.phase).toBe("connecting");
    await vi.advanceTimersByTimeAsync(1);
    await started;
    expect(f.state).toMatchObject({ phase: "error", error: "连不上语音服务：10 秒内没有连上" });
    resolve({ send() {}, close: () => closed++ });
    await flushPromises();
    expect(closed).toBe(1);
  });

  it("a click while connecting cancels: mic released, the late connection closed", async () => {
    let resolve: (t: Transport) => void = () => {};
    let closed = 0;
    let micStopped = 0;
    const f = base(() => new Promise<Transport>((r) => (resolve = r)));
    f.deps.openMic = async () => ({ getTracks: () => [{ stop: () => micStopped++ }] }) as unknown as MediaStream;
    const c = new VoiceController(f.state, f.deps);
    const started = c.start({ text: "", selStart: 0, selEnd: 0, language: "auto", deviceId: "" });
    await vi.advanceTimersByTimeAsync(0);
    expect(f.state.phase).toBe("connecting");
    await c.stop(); // the button's click while connecting
    expect(f.state).toMatchObject({ phase: "idle", error: "" });
    expect(micStopped).toBe(1);
    resolve({ send() {}, close: () => closed++ });
    await started;
    await flushPromises();
    expect(closed).toBe(1);
    expect(f.state.phase).toBe("idle");
  });

  it("hurry ends the wait for the last words, keeping the grey text", async () => {
    let h: TransportHandlers | null = null;
    const f = base(async (_s, _st, handlers) => ((h = handlers), { send() {}, close() {} }));
    const c = new VoiceController(f.state, f.deps);
    await c.start({ text: "", selStart: 0, selEnd: 0, language: "auto", deviceId: "" });
    (h as unknown as TransportHandlers).onEvent({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "半句" });
    const stopping = c.stop();
    await vi.advanceTimersByTimeAsync(0);
    expect(f.state.phase).toBe("stopping");
    c.hurry();
    await stopping;
    expect(f.state.phase).toBe("idle");
    expect(c.transcript.value).toBe("半句");
  });

  it("after the final commit waits for its transcript even with no grey text yet", async () => {
    const sent: unknown[] = [];
    let h: TransportHandlers | null = null;
    let closed = 0;
    let level: (l: number) => void = () => {};
    const f = base(async (_s, _st, handlers) => ((h = handlers), { send: (e) => sent.push(e), close: () => closed++ }));
    f.deps.meter = (_s, cb) => ((level = cb), () => {});
    const c = new VoiceController(f.state, f.deps);
    await c.start({ text: "", selStart: 0, selEnd: 0, language: "auto", deviceId: "" });
    level(0.5); // speech, no delta yet (the relay's fake sends text only after a commit)
    const stopping = c.stop();
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toEqual([{ type: "input_audio_buffer.commit" }]);
    expect(f.state.phase).toBe("stopping");
    expect(closed).toBe(0);
    const hh = h as unknown as TransportHandlers;
    hh.onEvent({ type: "input_audio_buffer.committed", item_id: "i1" });
    hh.onEvent({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "最后一句" });
    await stopping;
    expect(c.transcript.value).toBe("最后一句");
    expect(f.state.phase).toBe("idle");
    expect(closed).toBe(1);
  });

  it("takes client commits from the transport and stops before the server's limit", async () => {
    const sent: unknown[] = [];
    const f = base(async () => ({ send: (e) => sent.push(e), close() {}, clientCommits: false, maxSeconds: 60 }));
    const c = new VoiceController(f.state, f.deps);
    await c.start({ text: "", selStart: 0, selEnd: 0, language: "auto", deviceId: "" });
    expect(f.state.phase).toBe("recording");
    await vi.advanceTimersByTimeAsync(57_001);
    expect(f.state.phase).toBe("idle");
    expect(f.notices[0]).toContain("最长 1 分钟");
    expect(sent).toEqual([]); // clientCommits false: no commit on stop
  });
});

const chrome = [
  { kind: "audioinput", deviceId: "default", groupId: "g1", label: "Default - Microphone Array (Conexant ISST Audio)" },
  { kind: "audioinput", deviceId: "communications", groupId: "g1", label: "Communications - Microphone Array (Conexant ISST Audio)" },
  { kind: "audioinput", deviceId: "a1", groupId: "g1", label: "Microphone Array (Conexant ISST Audio)" },
  { kind: "audioinput", deviceId: "a2", groupId: "g1", label: "Microphone Array (Conexant ISST Audio)" },
  { kind: "audioinput", deviceId: "b1", groupId: "g2", label: "Headset Microphone (USB)" },
  { kind: "audiooutput", deviceId: "o1", groupId: "g1", label: "Speakers" },
] as MediaDeviceInfo[];

describe("microphone list", () => {
  it("one 系统默认 row, no communications, duplicates merged", () => {
    const r = normalizeMics(chrome);
    expect(r.labelled).toBe(true);
    expect(r.devices).toEqual([
      { deviceId: "", label: "系统默认（Microphone Array (Conexant ISST Audio)）" },
      { deviceId: "a1", label: "Microphone Array (Conexant ISST Audio)" },
      { deviceId: "b1", label: "Headset Microphone (USB)" },
    ]);
  });

  it("placeholders before permission never match a saved choice", () => {
    const r = normalizeMics([
      { kind: "audioinput", deviceId: "", groupId: "", label: "" },
      { kind: "audioinput", deviceId: "", groupId: "", label: "" },
    ] as MediaDeviceInfo[]);
    expect(r.labelled).toBe(false);
    expect(r.devices.map((d) => d.label)).toEqual(["系统默认", "麦克风 1", "麦克风 2"]);
    expect(r.devices[1]).toMatchObject({ placeholder: true });
    expect(selectedMic(r.devices, "")).toBe("");
    expect(selectedMic(r.devices, "placeholder:1")).toBe("");
    expect(normalizeMics([{ kind: "audioinput", deviceId: "x", groupId: "", label: "USB Mic" }] as MediaDeviceInfo[]).devices[0].label).toBe("系统默认");
  });

  it("falls back to 系统默认 when the saved microphone is gone", () => {
    const list = normalizeMics(chrome).devices;
    expect(selectedMic(list, "b1")).toBe("b1");
    expect(selectedMic(list, "gone")).toBe("");
    expect(selectedMic(list, "communications")).toBe("");
  });
});

describe("mic menu", () => {
  const languages = [
    { code: "zh", name: "中文", zh: "中文" },
    { code: "en", name: "English", zh: "英语" },
  ];
  const mountMenu = (devices: MediaDeviceInfo[], deviceId: string) => {
    const listeners: (() => void)[] = [];
    Object.defineProperty(navigator, "mediaDevices", {
      configurable: true,
      value: {
        enumerateDevices: async () => devices,
        addEventListener: (_: string, cb: () => void) => listeners.push(cb),
        removeEventListener: () => {},
      },
    });
    const w = mount(VoiceMenu, {
      props: { prefs: { deviceId, language: "auto", pushToTalk: false }, languages, shortcut: "Alt+M" },
      global: { plugins: [ElementPlus] },
    });
    return { w, listeners };
  };
  const checked = (w: ReturnType<typeof mount>) => w.findAll("[data-testid=voice-mic-row]").filter((r) => r.attributes("aria-checked") === "true");

  it("checks exactly one microphone and falls back to 系统默认", async () => {
    const saved = mountMenu(chrome, "b1");
    await flushPromises();
    expect(saved.w.findAll("[data-testid=voice-mic-row]").map((r) => r.text())).toEqual([
      "系统默认（Microphone Array (Conexant ISST Audio)）",
      "Microphone Array (Conexant ISST Audio)",
      "Headset Microphone (USB)",
    ]);
    expect(checked(saved.w).map((r) => r.text())).toEqual(["Headset Microphone (USB)"]);
    expect(saved.w.findAll("[data-testid=voice-mic-row]")[1].attributes("title")).toBe("Microphone Array (Conexant ISST Audio)");

    const gone = mountMenu(chrome, "unplugged");
    await flushPromises();
    expect(checked(gone.w).map((r) => r.text())).toEqual(["系统默认（Microphone Array (Conexant ISST Audio)）"]);

    const noPermission = mountMenu([{ kind: "audioinput", deviceId: "", groupId: "", label: "" }] as MediaDeviceInfo[], "");
    await flushPromises();
    expect(checked(noPermission.w).map((r) => r.text())).toEqual(["系统默认"]);
    expect(noPermission.w.text()).toContain("允许使用麦克风后显示设备名");
    expect(noPermission.listeners).toHaveLength(1); // refreshes on devicechange
  });

  it("opens languages as a submenu and picks one", async () => {
    const { w } = mountMenu(chrome, "");
    await flushPromises();
    const row = w.find("[data-testid=voice-language-row]");
    expect(row.text()).toContain("自动");
    expect(w.find("input[aria-label=搜索语言]").exists()).toBe(false);
    await row.trigger("click");
    await w.find("input[aria-label=搜索语言]").setValue("英");
    const options = w.findAll("[role=radiogroup][aria-label=语言] [role=radio]");
    expect(options.map((o) => o.text())).toEqual(["English英语"]);
    await options[0].trigger("click");
    expect(w.emitted("change")?.at(-1)).toEqual([{ language: "en" }]);
    expect(w.find("[data-testid=voice-language-row]").exists()).toBe(true);
  });
});

describe("mic button", () => {
  const input = (phase: VoiceState["phase"], pushToTalk = false) => {
    const calls: string[] = [];
    const v = {
      enabled: ref(true),
      languages: ref([]),
      state: reactive({ phase, level: 0.2, error: "" }),
      prefs: reactive({ deviceId: "", language: "auto", pushToTalk }),
      parts: ref(null),
      active: ref(phase === "connecting" || phase === "recording" || phase === "stopping"),
      setPrefs: () => {},
      toggle: () => calls.push("toggle"),
      press: () => calls.push("press"),
      release: () => calls.push("release"),
      finish: async () => {},
      cancel: () => calls.push("cancel"),
      onKeyDown: () => false,
      onKeyUp: () => {},
    } as unknown as VoiceInput;
    const w = mount(VoiceButton, { props: { v }, global: { plugins: [ElementPlus] } });
    return { w, calls, mic: () => w.find("[data-testid=voice-mic]") };
  };

  it("is a blue mic with a spinner ring while connecting, and a click cancels", async () => {
    const { calls, mic, w } = input("connecting");
    expect(mic().attributes("title")).toBe("正在连接…点击取消");
    expect(w.find("[data-testid=voice-spinner]").exists()).toBe(true);
    expect(mic().classes()).toContain("text-[#2F6FDB]");
    await mic().trigger("click");
    expect(calls).toEqual(["toggle"]);
  });

  it("says 点击结束 while recording (solid blue, no spinner); push-to-talk says 松开结束 and a keyboard click stops", async () => {
    const rec = input("recording");
    expect(rec.mic().attributes("title")).toBe("正在录音，点击结束（Esc）");
    expect(rec.w.find("[data-testid=voice-spinner]").exists()).toBe(false);
    expect(rec.mic().classes()).toContain("bg-[#2F6FDB]");
    expect(rec.mic().classes()).toContain("text-white");
    await rec.mic().trigger("click");
    expect(rec.calls).toEqual(["toggle"]);
    const ptt = input("recording", true);
    expect(ptt.mic().attributes("title")).toBe("正在录音，松开结束");
    await ptt.mic().trigger("click"); // detail 0: keyboard activation
    expect(ptt.calls).toEqual(["toggle"]);
  });

  it("idle titles describe the mode", () => {
    expect(input("idle").mic().attributes("title")).toMatch(/^语音输入：点击开始，再点击结束/);
    expect(input("idle", true).mic().attributes("title")).toMatch(/^按住说话，松开结束/);
  });

  it("a push-to-talk press is not also a click", async () => {
    const { calls, mic } = input("idle", true);
    await mic().trigger("pointerdown");
    await mic().trigger("pointerup");
    await mic().trigger("click");
    expect(calls).toEqual(["press", "release"]);
  });
});
