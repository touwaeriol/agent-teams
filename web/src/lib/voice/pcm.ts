// Audio for the server relay: microphone samples (Float32 at the AudioContext's rate, usually
// 48 kHz) → 24 kHz mono PCM16 little-endian in 100 ms frames, which /api/voice/stream forwards
// to OpenAI as input_audio_buffer.append. Pure and streaming, so tests check the math.
import { $t } from "@/i18n";

export const OUT_RATE = 24_000;
/** 100 ms at 24 kHz. */
export const FRAME_SAMPLES = 2400;

/**
 * Streaming sample-rate converter. Downsampling averages the input samples under each output
 * sample (a box filter, enough against aliasing for speech); upsampling interpolates linearly.
 * Input split across push() calls gives the same output as one call.
 */
export class Resampler {
  private carry: Float32Array = new Float32Array(0);
  /** Position of the next output sample, in input samples from the start of carry. */
  private t = 0;
  readonly step: number;

  constructor(
    readonly inRate: number,
    readonly outRate = OUT_RATE,
  ) {
    if (!(inRate > 0) || !(outRate > 0)) throw new Error("bad sample rate");
    this.step = inRate / outRate;
  }

  push(input: Float32Array): Float32Array {
    const buf = concat(this.carry, input);
    const out: number[] = [];
    const need = Math.max(this.step, 1);
    while (this.t + need <= buf.length) {
      out.push(this.step >= 1 ? average(buf, this.t, this.t + this.step) : interpolate(buf, this.t));
      this.t += this.step;
    }
    const used = Math.min(Math.floor(this.t), buf.length);
    this.carry = buf.slice(used);
    this.t -= used;
    return Float32Array.from(out);
  }
}

function concat(a: Float32Array, b: Float32Array): Float32Array {
  if (!a.length) return b;
  const out = new Float32Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

/** Mean of buf over [from, to), weighting the partial samples at both ends. */
function average(buf: Float32Array, from: number, to: number): number {
  let sum = 0;
  let i = Math.floor(from);
  while (i < to) {
    const w = Math.min(i + 1, to) - Math.max(i, from);
    sum += buf[i] * w;
    i++;
  }
  return sum / (to - from);
}

function interpolate(buf: Float32Array, at: number): number {
  const i = Math.floor(at);
  const next = i + 1 < buf.length ? buf[i + 1] : buf[i];
  return buf[i] + (next - buf[i]) * (at - i);
}

/** Float −1..1 → signed 16-bit, clamped. */
export function toPcm16(samples: Float32Array): Int16Array {
  const out = new Int16Array(samples.length);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    out[i] = s < 0 ? Math.round(s * 0x8000) : Math.round(s * 0x7fff);
  }
  return out;
}

/** Collects PCM16 samples and hands out fixed-size little-endian frames. */
export class Framer {
  private buf: Int16Array;
  private n = 0;

  constructor(
    private onFrame: (frame: ArrayBuffer) => void,
    readonly size = FRAME_SAMPLES,
  ) {
    this.buf = new Int16Array(size);
  }

  push(samples: Int16Array): void {
    let i = 0;
    while (i < samples.length) {
      const take = Math.min(this.size - this.n, samples.length - i);
      this.buf.set(samples.subarray(i, i + take), this.n);
      this.n += take;
      i += take;
      if (this.n === this.size) this.emit();
    }
  }

  /** Sends what is buffered (the end of an utterance) as a shorter frame. */
  flush(): void {
    if (this.n) this.emit();
  }

  private emit(): void {
    this.onFrame(littleEndian(this.buf.subarray(0, this.n)));
    this.n = 0;
  }
}

function littleEndian(samples: Int16Array): ArrayBuffer {
  const out = new ArrayBuffer(samples.length * 2);
  const view = new DataView(out);
  samples.forEach((s, i) => view.setInt16(i * 2, s, true));
  return out;
}

/**
 * The AudioWorklet processor: copies the microphone's 128-sample render quanta into batches of
 * about 20 ms and posts them to the page ({samples, flushed}; "flush" posts what it has at once) (resampling happens there, in Resampler).
 */
export const CAPTURE_WORKLET = `
class CaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.batch = new Float32Array(Math.max(128, Math.round(sampleRate / 50)));
    this.n = 0;
    this.port.onmessage = (e) => { if (e.data === "flush") this.flush(true); };
  }
  flush(reply) {
    if (this.n || reply) this.port.postMessage({ samples: this.batch.slice(0, this.n), flushed: Boolean(reply) });
    this.n = 0;
  }
  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (!ch) return true;
    let i = 0;
    while (i < ch.length) {
      const take = Math.min(this.batch.length - this.n, ch.length - i);
      this.batch.set(ch.subarray(i, i + take), this.n);
      this.n += take;
      i += take;
      if (this.n === this.batch.length) this.flush();
    }
    return true;
  }
}
registerProcessor("agent-teams-capture", CaptureProcessor);
`;

export interface Capture {
  /** Pushes buffered audio out as a frame now (before a commit). */
  flush(): Promise<void>;
  stop(): void;
}

/** Captures the stream as 24 kHz PCM16 frames until stop(). */
export async function startCapture(stream: MediaStream, onFrame: (frame: ArrayBuffer) => void): Promise<Capture> {
  const Ctx = globalThis.AudioContext ?? (globalThis as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
  if (!Ctx || typeof AudioWorkletNode === "undefined") throw new Error($t("这个浏览器不支持录音（需要 HTTPS 和较新的浏览器）"));
  const ctx = new Ctx();
  const url = URL.createObjectURL(new Blob([CAPTURE_WORKLET], { type: "text/javascript" }));
  try {
    await ctx.audioWorklet.addModule(url);
  } finally {
    URL.revokeObjectURL(url);
  }
  if (ctx.state === "suspended") await ctx.resume().catch(() => {});
  const source = ctx.createMediaStreamSource(stream);
  const node = new AudioWorkletNode(ctx, "agent-teams-capture", { numberOfInputs: 1, numberOfOutputs: 0, channelCount: 1, channelCountMode: "explicit" });
  const resampler = new Resampler(ctx.sampleRate);
  const framer = new Framer(onFrame);
  let flushed: (() => void) | null = null;
  node.port.onmessage = (e: MessageEvent<{ samples: Float32Array; flushed: boolean }>) => {
    framer.push(toPcm16(resampler.push(e.data.samples)));
    if (e.data.flushed && flushed) {
      framer.flush();
      flushed();
      flushed = null;
    }
  };
  source.connect(node);
  return {
    flush: () =>
      new Promise<void>((resolve) => {
        const timer = setTimeout(() => (framer.flush(), (flushed = null), resolve()), 100);
        flushed = () => (clearTimeout(timer), resolve());
        node.port.postMessage("flush");
      }),
    stop: () => {
      node.port.onmessage = null;
      source.disconnect();
      node.disconnect();
      void ctx.close();
    },
  };
}
