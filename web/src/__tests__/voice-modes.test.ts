// @vitest-environment jsdom
// Voice recording modes end to end in the component: the mic button driving the real
// useVoiceInput / VoiceController with a fake microphone, meter and transport. Click mode
// (click to start, click to stop) and push-to-talk (press to start, release to stop) are mutually
// exclusive; every way out releases the meter, the microphone tracks and the transport. Also the
// level meter (AudioContext closed on stop) and the waveform bars.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, ref } from "vue";
import VoiceButton from "@/components/app/voice/VoiceButton.vue";
import VoiceWave from "@/components/app/voice/VoiceWave.vue";
import { useVoiceInput, type VoiceInput } from "@/components/app/voice/useVoiceInput";
import { barHeight, WAVE_MIN, WAVE_TICK_MS } from "@/components/app/voice/wave";
import { startMeter } from "@/lib/voice/audio";
import type { VoiceDeps } from "@/lib/voice/controller";
import type { Transport, TransportHandlers } from "@/lib/voice/openai";
import { relaySession } from "@/lib/voice/relay";
import type { Transcript } from "@/lib/voice/transcript";

interface Rig {
  v: VoiceInput;
  text: { value: string };
  mic: () => HTMLButtonElement;
  phase: () => string | undefined;
  opened: number;
  tracksStopped: number;
  metersStarted: number;
  metersStopped: number;
  closed: number;
  sent: unknown[];
  level: (l: number) => void;
  handlers: () => TransportHandlers;
  /** Lets a pending connect finish (when built with gate: true). */
  connect: () => void;
  unmount: () => void;
  wrapper: ReturnType<typeof mount>;
}

const prefs = (pushToTalk: boolean) => {
  const v = JSON.stringify({ deviceId: "", language: "auto", pushToTalk });
  localStorage.setItem("voice-prefs:me", v);
  localStorage.setItem("voice-prefs:u1", v);
};

async function rig(opts: { pushToTalk?: boolean; gate?: boolean } = {}): Promise<Rig> {
  prefs(Boolean(opts.pushToTalk));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input instanceof Request ? input.url : input);
    const body = url.includes("/api/voice/config")
      ? { enabled: true, transport: "relay", languages: [{ code: "zh", name: "中文", zh: "中文" }] }
      : { id: "u1", email: "a@b.c", role: "user" };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  let onLevel: (l: number) => void = () => {};
  let h0: TransportHandlers | null = null;
  let open: (() => void) | null = null;
  const r = {
    opened: 0,
    tracksStopped: 0,
    metersStarted: 0,
    metersStopped: 0,
    closed: 0,
    sent: [] as unknown[],
  };
  const makeDeps = (onText: (t: Transcript) => void): VoiceDeps => ({
    mint: async (language) => relaySession(language),
    openMic: async () => {
      r.opened++;
      return { getTracks: () => [{ stop: () => r.tracksStopped++ }] } as unknown as MediaStream;
    },
    connect: async (_s, _st, handlers) => {
      h0 = handlers;
      if (opts.gate) await new Promise<void>((res) => (open = res));
      const t: Transport = { send: (e) => r.sent.push(e), close: () => r.closed++, clientCommits: true };
      return t;
    },
    meter: (_s, cb) => {
      r.metersStarted++;
      onLevel = cb;
      return () => r.metersStopped++;
    },
    now: () => Date.now(),
    onText,
    tailMs: 0,
  });
  let v!: VoiceInput;
  const text = ref("");
  const Host = defineComponent({
    setup() {
      const field = ref<HTMLTextAreaElement | null>(null);
      v = useVoiceInput(text, field, makeDeps);
      return () => h("div", [h("textarea", { ref: field }), h(VoiceButton, { v })]);
    },
  });
  const wrapper = mount(Host, {
    global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }], ElementPlus] as never[] },
    attachTo: document.body,
  });
  await flushPromises();
  const mic = () => wrapper.find("[data-testid=voice-mic]").element as HTMLButtonElement;
  return Object.assign(r, {
    v,
    text,
    mic,
    phase: () => mic().dataset.phase,
    level: (l: number) => onLevel(l),
    handlers: () => h0 as TransportHandlers,
    connect: () => open?.(),
    unmount: () => wrapper.unmount(),
    wrapper,
  });
}

/** jsdom has no PointerEvent: same-named MouseEvents carry what the button reads. */
const pointer = (el: EventTarget, type: string, init: MouseEventInit = {}) =>
  el.dispatchEvent(Object.assign(new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, ...init }), { pointerId: 1, pointerType: "mouse", isPrimary: true }));
/** A pointer click (detail 1); keyboard activation clicks have detail 0. */
const click = (el: EventTarget, detail = 1) => el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail }));
const settle = async (ms = 0) => {
  await vi.advanceTimersByTimeAsync(ms);
  await flushPromises();
};

beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date", "performance"] }));
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  localStorage.clear();
  document.body.innerHTML = "";
});

describe("click mode (default)", () => {
  it("click starts, click again stops and the transcript lands in the text box", async () => {
    const r = await rig();
    expect(r.mic().dataset.mode).toBe("click");
    click(r.mic());
    await settle();
    expect(r.phase()).toBe("recording");
    r.handlers().onEvent({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "你好" });
    r.handlers().onEvent({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "你好。" });
    await settle(1000);
    click(r.mic());
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.text.value).toBe("你好。");
    expect(r.opened).toBe(1);
  });

  it("pointer down/up alone neither starts nor stops", async () => {
    const r = await rig();
    pointer(r.mic(), "pointerdown");
    pointer(r.mic(), "pointerup");
    await settle(500);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(0);
    click(r.mic());
    await settle(500);
    pointer(r.mic(), "pointerdown");
    pointer(r.mic(), "pointerup");
    await settle(500);
    expect(r.phase()).toBe("recording");
  });

  it("a double click doesn't start and immediately stop (or stop and skip the last words)", async () => {
    const r = await rig();
    click(r.mic());
    click(r.mic(), 2);
    await settle();
    expect(r.phase()).toBe("recording");
    await settle(1000);
    click(r.mic());
    click(r.mic(), 2);
    await settle(10);
    expect(r.phase()).toBe("idle");
  });

  it("Alt+M toggles", async () => {
    const r = await rig();
    const key = (type: string) => new KeyboardEvent(type, { code: "KeyM", key: "µ", altKey: true, cancelable: true });
    expect(r.v.onKeyDown(key("keydown"))).toBe(true);
    r.v.onKeyUp(key("keyup"));
    await settle();
    expect(r.phase()).toBe("recording");
    r.v.onKeyDown(key("keydown"));
    await settle(10);
    expect(r.phase()).toBe("idle");
  });
});

describe("push-to-talk mode", () => {
  it("pointerdown starts, pointerup stops", async () => {
    const r = await rig({ pushToTalk: true });
    expect(r.mic().dataset.mode).toBe("hold");
    pointer(r.mic(), "pointerdown");
    await settle();
    expect(r.phase()).toBe("recording");
    pointer(r.mic(), "pointerup");
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect([r.metersStopped, r.tracksStopped, r.closed]).toEqual([1, 1, 1]);
  });

  it("pointercancel stops too", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle();
    pointer(r.mic(), "pointercancel");
    await settle(10);
    expect(r.phase()).toBe("idle");
  });

  it("released off the button (or the window losing focus) still stops", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle();
    pointer(document.body, "pointerup");
    await settle(10);
    expect(r.phase()).toBe("idle");
    pointer(r.mic(), "pointerdown");
    await settle();
    expect(r.phase()).toBe("recording");
    window.dispatchEvent(new Event("blur"));
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(2);
    expect(r.tracksStopped).toBe(2);
  });

  it("after a press whose release never came (window blur), the next press works", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle(500);
    window.dispatchEvent(new Event("blur")); // e.g. a permission prompt: no pointerup ever arrives
    await settle(10);
    expect(r.phase()).toBe("idle");
    pointer(r.mic(), "pointerdown");
    await settle(500);
    expect(r.phase()).toBe("recording");
    pointer(r.mic(), "pointerup");
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(2);
    expect(r.tracksStopped).toBe(2);
  });

  it("the tab hidden or pointer capture lost also ends the press, and the next press works", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle(500);
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    await settle(10);
    expect(r.phase()).toBe("idle");
    pointer(r.mic(), "pointerdown");
    await settle(500);
    expect(r.phase()).toBe("recording");
    pointer(r.mic(), "lostpointercapture");
    await settle(10);
    expect(r.phase()).toBe("idle");
    pointer(r.mic(), "pointerdown");
    await settle(500);
    expect(r.phase()).toBe("recording");
    pointer(r.mic(), "pointerup");
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(3);
  });

  it("pressed again while the last words settle: starts once they are in, if still held", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle(500);
    r.handlers().onEvent({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "第一句" });
    pointer(r.mic(), "pointerup");
    await settle(10);
    expect(r.phase()).toBe("stopping"); // waiting for the final text of 第一句
    pointer(r.mic(), "pointerdown");
    await settle(10);
    expect(r.phase()).toBe("stopping");
    expect(r.opened).toBe(1);
    r.handlers().onEvent({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "第一句。" });
    await settle(10);
    expect(r.text.value).toBe("第一句。"); // not cut short
    expect(r.phase()).toBe("recording");
    expect(r.opened).toBe(2);
    pointer(r.mic(), "pointerup");
    await settle(10);
    expect(r.phase()).toBe("idle");
  });

  it("pressed and released while the last words settle: nothing new starts", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle(500);
    r.handlers().onEvent({ type: "conversation.item.input_audio_transcription.delta", item_id: "i1", delta: "第一句" });
    pointer(r.mic(), "pointerup");
    await settle(10);
    pointer(r.mic(), "pointerdown");
    await settle(10);
    pointer(r.mic(), "pointerup");
    await settle(10);
    r.handlers().onEvent({ type: "conversation.item.input_audio_transcription.completed", item_id: "i1", transcript: "第一句。" });
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(1);
  });

  it("released while connecting: stops as soon as the connection is up", async () => {
    const r = await rig({ pushToTalk: true, gate: true });
    pointer(r.mic(), "pointerdown");
    await settle();
    expect(r.phase()).toBe("connecting");
    pointer(r.mic(), "pointerup");
    click(r.mic());
    await settle(100);
    expect(r.phase()).toBe("connecting");
    r.connect();
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect([r.metersStopped, r.tracksStopped, r.closed]).toEqual([1, 1, 1]);
  });

  it("one press is one recording: the click after it doesn't toggle again", async () => {
    const r = await rig({ pushToTalk: true });
    pointer(r.mic(), "pointerdown");
    await settle(800);
    pointer(r.mic(), "pointerup");
    click(r.mic());
    click(r.mic(), 0);
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(1);
    // A press with no click after it (a touch that moved) leaves nothing behind: switching to click
    // mode, the first click starts.
    pointer(r.mic(), "pointerdown");
    await settle(300);
    pointer(r.mic(), "pointerup");
    await settle(10);
    r.v.setPrefs({ pushToTalk: false });
    await settle();
    click(r.mic());
    await settle();
    expect(r.phase()).toBe("recording");
  });

  it("a click alone never starts a recording", async () => {
    const r = await rig({ pushToTalk: true });
    click(r.mic());
    click(r.mic(), 0);
    await settle(500);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(0);
  });

  it("Alt+M: held down records, released stops", async () => {
    const r = await rig({ pushToTalk: true });
    const key = (type: string, repeat = false) => new KeyboardEvent(type, { code: "KeyM", key: "µ", altKey: true, repeat, cancelable: true });
    r.v.onKeyDown(key("keydown"));
    await settle(300);
    r.v.onKeyDown(key("keydown", true));
    await settle(300);
    expect(r.phase()).toBe("recording");
    r.v.onKeyUp(key("keyup"));
    await settle(10);
    expect(r.phase()).toBe("idle");
    expect(r.opened).toBe(1);
  });
});

describe("resources", () => {
  it("cancel, a dropped connection and unmounting release meter, tracks and transport", async () => {
    const r = await rig();
    click(r.mic());
    await settle();
    r.v.cancel();
    await settle();
    expect([r.metersStopped, r.tracksStopped, r.closed]).toEqual([1, 1, 1]);
    await settle(400);
    click(r.mic());
    await settle();
    r.handlers().onClose("断了");
    await settle();
    expect(r.phase()).toBe("error");
    expect([r.metersStopped, r.tracksStopped]).toEqual([2, 2]);
    expect(r.wrapper.find("[data-testid=voice-wave]").exists()).toBe(false);
    await settle(400);
    click(r.mic());
    await settle();
    expect(r.phase()).toBe("recording");
    r.unmount();
    expect([r.metersStopped, r.tracksStopped, r.closed]).toEqual([3, 3, 2]);
  });

  it("cancelled while connecting: meter and mic released, the late connection closed", async () => {
    const r = await rig({ gate: true });
    click(r.mic());
    await settle();
    expect(r.phase()).toBe("connecting");
    expect(r.metersStarted).toBe(1); // the level shows while connecting
    await settle(400);
    click(r.mic());
    await settle();
    expect(r.phase()).toBe("idle");
    expect([r.metersStopped, r.tracksStopped]).toEqual([1, 1]);
    r.connect();
    await settle();
    expect(r.closed).toBe(1);
  });

  it("shows the level while connecting without committing utterances before recording", async () => {
    const r = await rig({ gate: true });
    click(r.mic());
    await settle();
    expect(r.wrapper.find("[data-testid=voice-wave]").exists()).toBe(true);
    r.level(0.5);
    expect(r.v.state.level).toBe(0.5);
    r.level(0);
    await settle(2000);
    r.connect();
    await settle();
    expect(r.phase()).toBe("recording");
    expect(r.sent).toEqual([]);
  });
});

describe("startMeter", () => {
  it("reports levels and closes the AudioContext when stopped", () => {
    const closed = vi.fn(async () => {});
    const disconnect = vi.fn();
    class FakeCtx {
      createMediaStreamSource = () => ({ connect: () => {}, disconnect });
      createAnalyser = () => ({ fftSize: 0, getFloatTimeDomainData: (b: Float32Array) => b.fill(0.1) });
      close = closed;
    }
    vi.stubGlobal("AudioContext", FakeCtx);
    const levels: number[] = [];
    const stop = startMeter({} as MediaStream, (l) => levels.push(l));
    vi.advanceTimersByTime(120);
    expect(levels.length).toBe(2);
    expect(levels[0]).toBeCloseTo(0.5);
    stop();
    vi.advanceTimersByTime(200);
    expect(levels.length).toBe(2);
    expect(closed).toHaveBeenCalledTimes(1);
    expect(disconnect).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });
});

describe("waveform", () => {
  const heights = (w: ReturnType<typeof mount>) => w.findAll("[data-testid=voice-wave-bar]").map((b) => parseFloat((b.element as HTMLElement).style.height));

  it("is flat dots when silent and jumps with the level", async () => {
    const w = mount(VoiceWave, { props: { level: 0, bars: 10, max: 18 } });
    vi.advanceTimersByTime(WAVE_TICK_MS * 12);
    await flushPromises();
    expect(heights(w)).toEqual(Array(10).fill(WAVE_MIN));
    await w.setProps({ level: 0.02 }); // background noise stays flat
    vi.advanceTimersByTime(WAVE_TICK_MS * 12);
    await flushPromises();
    expect(new Set(heights(w))).toEqual(new Set([WAVE_MIN]));
    await w.setProps({ level: 0.8 });
    vi.advanceTimersByTime(WAVE_TICK_MS * 3);
    await flushPromises();
    const hs = heights(w);
    expect(hs.length).toBe(10);
    expect(hs.at(-1)).toBe(18); // newest on the right
    expect(hs[0]).toBe(WAVE_MIN); // older silence still on the left
    await w.setProps({ level: 0 });
    vi.advanceTimersByTime(WAVE_TICK_MS * 12);
    await flushPromises();
    expect(heights(w)).toEqual(Array(10).fill(WAVE_MIN)); // scrolled out, even with a constant level
    w.unmount();
  });

  it("maps levels monotonically between the dot and the full height", () => {
    expect(barHeight(0, 18)).toBe(WAVE_MIN);
    expect(barHeight(0.2, 18)).toBeGreaterThan(WAVE_MIN);
    expect(barHeight(0.5, 18)).toBeGreaterThan(barHeight(0.2, 18));
    expect(barHeight(1, 18)).toBe(18);
  });
});
