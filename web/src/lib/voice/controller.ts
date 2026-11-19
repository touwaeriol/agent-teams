// One composer's voice input: idle → connecting → recording → stopping → idle (or error).
// Framework-free; the composer wraps it (useVoiceInput) and tests drive it with fakes. The
// transport is the server relay (relay.ts, default) or WebRTC straight to OpenAI (openai.ts);
// connecting fails after CONNECT_TIMEOUT instead of spinning.
//
// A click opens the microphone and connects at the same time. With the relay, recording starts
// as soon as the microphone is open (the relay buffers audio until the server is ready), so a
// slow connection delays the text, not the recording; a connection that fails still ends in
// error. The microphone has its own MIC_TIMEOUT (a permission prompt may be waiting for the
// user), CONNECT_TIMEOUT counts from when connecting starts. Each recording logs one
// "[voice] timings" line (console.debug, ms since the click) once it is up or failed, and one
// more when it ends.
//
// gpt-realtime-whisper has no server VAD, so with client_commits the controller ends each
// utterance itself: after speech followed by SILENCE_MS of quiet (or MAX_UTTERANCE_MS of talking)
// it sends input_audio_buffer.commit, and once more when recording stops; the completed
// transcript of each committed item then replaces its grey partial text.
import { micErrorText } from "./audio";
import { CONNECT_TIMEOUT, ConnectError, type Connect, type RealtimeEvent, type Transport, type TransportHandlers, type VoiceSessionInfo } from "./openai";
import { Transcript } from "./transcript";

export type Phase = "idle" | "connecting" | "recording" | "stopping" | "error";

export interface VoiceState {
  phase: Phase;
  level: number;
  error: string;
}

export interface VoiceDeps {
  mint(language: string): Promise<VoiceSessionInfo>;
  openMic(deviceId: string): Promise<MediaStream>;
  connect: Connect;
  meter(stream: MediaStream, onLevel: (level: number) => void): () => void;
  now(): number;
  /** Text changed (value, caret, partial parts): the composer re-renders. */
  onText(t: Transcript): void;
  /** A non-fatal problem worth a toast. */
  onNotice?(message: string): void;
  /** How long the microphone stays open after stop before the final commit (default TAIL_MS). */
  tailMs?: number;
  /** Connecting (minting included) fails after this long (default CONNECT_TIMEOUT). */
  connectTimeoutMs?: number;
  /** Opening the microphone fails after this long (default MIC_TIMEOUT). */
  micTimeoutMs?: number;
  /** Clock for the timing log (default performance.now). */
  clock?(): number;
  /** Where the timing log goes (default console.debug). */
  log?(message: string, data: Record<string, unknown>): void;
}

/** The microphone (permission prompt included) must open within this. */
export const MIC_TIMEOUT = 15_000;
export const micTimeoutText = (ms: number) => `麦克风 ${ms / 1000} 秒内没有打开，请检查浏览器的权限弹框或麦克风设备`;

export const SPEECH_LEVEL = 0.06;
export const SILENCE_MS = 800;
export const MAX_UTTERANCE_MS = 20_000;
export const SETTLE_MS = 4000;
export const TAIL_MS = 300;
/** Remint when the secret has less than this left before connecting. */
const SECRET_MARGIN_S = 5;

export class VoiceController {
  readonly transcript = new Transcript();
  private transport: Transport | null = null;
  private stream: MediaStream | null = null;
  private stopMeter: (() => void) | null = null;
  private clientCommits = false;
  private speaking = false;
  private heard = false;
  private lastVoice = 0;
  private utteranceStart = 0;
  private run = 0;
  private settled: (() => void) | null = null;
  private limitTimer: ReturnType<typeof setTimeout> | null = null;
  /** Commits sent whose input_audio_buffer.committed hasn't come back yet. */
  private outstanding = 0;
  private acked = new Set<string>();
  /** Relay: settles true once the server is ready (false: connecting failed). Null when connected. */
  private connecting: Promise<boolean> | null = null;
  private t0 = 0;
  private timings: Record<string, number> = {};
  private transportName = "";

  constructor(
    public state: VoiceState,
    private deps: VoiceDeps,
  ) {}

  get active(): boolean {
    return this.state.phase === "connecting" || this.state.phase === "recording" || this.state.phase === "stopping";
  }

  /** Starts recording into text at the selection. */
  async start(opts: { text: string; selStart: number; selEnd: number; language: string; deviceId: string }): Promise<void> {
    if (this.active) return;
    const run = ++this.run;
    this.t0 = this.clock();
    this.timings = {};
    this.transportName = "";
    this.transcript.begin(opts.text, opts.selStart, opts.selEnd);
    this.set({ phase: "connecting", error: "", level: 0 });
    // Microphone and connection at the same time.
    const mic = this.openMic(opts.deviceId, run);
    mic.catch(() => {});
    let t: Transport;
    try {
      t = await this.open(opts.language, mic, run);
    } catch (e) {
      if (run !== this.run) return;
      this.fail(e instanceof Error ? e.message : "连不上语音服务");
      this.run++; // a connection that still finishes is closed (open checks run)
      return;
    }
    if (run !== this.run) return this.release();
    this.transport = t;
    this.set({ phase: "recording" });
    this.stopMeter = this.deps.meter(this.stream as MediaStream, (level) => this.onLevel(level));
    if (t.ready) {
      this.connecting = this.untilReady(t.ready, run);
      if (!(await this.connecting) || run !== this.run) return;
    }
    if (t.clientCommits !== undefined) this.clientCommits = t.clientCommits;
    this.logTimings("recording");
    if (this.state.phase === "recording") this.watchLimit();
  }

  /** Opens the microphone within MIC_TIMEOUT; a stream that comes for a finished run is closed. */
  private openMic(deviceId: string, run: number): Promise<MediaStream> {
    const ms = this.deps.micTimeoutMs ?? MIC_TIMEOUT;
    return new Promise((resolve, reject) => {
      let late = false;
      const timer = setTimeout(() => {
        late = true;
        reject(new Error(micTimeoutText(ms)));
      }, ms);
      this.deps.openMic(deviceId).then(
        (stream) => {
          clearTimeout(timer);
          if (late || run !== this.run) {
            stream.getTracks().forEach((t) => t.stop());
            return reject(new Error("已取消"));
          }
          this.stream = stream;
          this.mark("mic");
          resolve(stream);
        },
        (e) => {
          clearTimeout(timer);
          reject(new Error(micErrorText(e)));
        },
      );
    });
  }

  /** Relay: waits for the server; a failure ends the recording with the reason. */
  private async untilReady(ready: Promise<void>, run: number): Promise<boolean> {
    try {
      await this.withTimeout(ready);
      return true;
    } catch (e) {
      if (run === this.run) {
        this.transcript.dropPartials();
        this.deps.onText(this.transcript);
        this.settled?.();
        this.fail(e instanceof Error ? e.message : "连不上语音服务");
        this.run++;
      }
      return false;
    }
  }

  /** Never spin: a connection that isn't up in time fails with a reason. */
  private withTimeout<T>(p: Promise<T>): Promise<T> {
    const ms = this.deps.connectTimeoutMs ?? CONNECT_TIMEOUT;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const late = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new ConnectError(0, `连不上语音服务：${ms / 1000} 秒内没有连上`)), ms);
    });
    return Promise.race([p, late]).finally(() => clearTimeout(timer));
  }

  /**
   * The connection's own timer runs from when it starts; this backstop starts once the
   * microphone is open, so waiting for the microphone never eats the connect time.
   */
  private afterMic<T>(mic: Promise<MediaStream>, p: Promise<T>): Promise<T> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let done = false;
    const ms = this.deps.connectTimeoutMs ?? CONNECT_TIMEOUT;
    const late = new Promise<never>((_, reject) => {
      mic.then(() => {
        if (!done) timer = setTimeout(() => reject(new ConnectError(0, `连不上语音服务：${ms / 1000} 秒内没有连上`)), ms);
      }, reject);
    });
    // A transport is only usable with the microphone open (fakes may not wait for it).
    const both = Promise.all([p, mic]).then(([t]) => t);
    return Promise.race([both, late]).finally(() => {
      done = true;
      clearTimeout(timer);
    });
  }

  /** Stops on our own shortly before the server's maximum recording length. */
  private watchLimit(): void {
    const max = this.transport?.maxSeconds;
    if (!max) return;
    this.limitTimer = setTimeout(() => {
      this.deps.onNotice?.(`单次录音最长 ${Math.round(max / 60)} 分钟，已自动结束`);
      void this.stop();
    }, Math.max(1, max - 3) * 1000);
  }

  /** Mints a client secret and connects (while the microphone opens); a rejected (expired) secret is reminted once. */
  private async open(language: string, mic: Promise<MediaStream>, run: number): Promise<Transport> {
    const handlers: TransportHandlers = {
      onEvent: (e) => this.onEvent(e),
      onClose: (message) => this.onDrop(message),
      mark: (name) => run === this.run && this.mark(name),
      onNotice: (message) => this.deps.onNotice?.(message),
    };
    for (let attempt = 0; ; attempt++) {
      const session = await this.withTimeout(this.deps.mint(language));
      if (run !== this.run) throw new Error("已取消");
      if (session.expires_at && session.expires_at - SECRET_MARGIN_S < this.deps.now() / 1000 && attempt === 0) continue;
      this.clientCommits = session.client_commits;
      this.transportName = session.transport;
      const connecting = this.deps.connect(session, mic, handlers);
      let t: Transport;
      try {
        t = await this.afterMic(mic, connecting);
      } catch (e) {
        connecting.then((late) => late.close(), () => {}); // one that comes up after all is closed
        const expired = e instanceof ConnectError && (e.status === 401 || e.status === 403);
        if (!expired || attempt > 0) throw e;
        continue;
      }
      if (run !== this.run) {
        t.close();
        throw new Error("已取消");
      }
      return t;
    }
  }

  /** Stops recording and waits (up to SETTLE_MS) for the last transcripts. */
  async stop(): Promise<void> {
    if (this.state.phase === "connecting") return this.cancel();
    if (this.state.phase !== "recording") return;
    this.set({ phase: "stopping", level: 0 });
    this.stopMeter?.();
    this.stopMeter = null;
    // Let the last audio packets reach OpenAI before ending the utterance.
    await new Promise((r) => setTimeout(r, this.deps.tailMs ?? TAIL_MS));
    if ((this.state.phase as Phase) !== "stopping") return; // cancelled meanwhile
    if (this.clientCommits && (this.heard || this.transcript.pending)) this.commit();
    this.closeMic();
    // Stopped before the server was ready: the buffered audio and the commit go out once it is
    // (still within the connect timeout); a failure has already ended the recording in error.
    if (this.connecting && !(await this.connecting)) return;
    if ((this.state.phase as Phase) !== "stopping") return;
    if (this.unsettled) await this.waitSettled();
    this.transcript.settle();
    this.deps.onText(this.transcript);
    this.logTimings("stopped");
    this.release();
    this.set({ phase: "idle" });
  }

  /** While stopping: stop waiting for the last transcripts and keep the grey text as it is. */
  hurry(): void {
    if (this.state.phase === "stopping") this.settled?.();
  }

  /** Stops at once; partial (grey) text is dropped. */
  cancel(): void {
    this.run++;
    this.transcript.dropPartials();
    this.deps.onText(this.transcript);
    this.release();
    this.settled?.();
    this.set({ phase: "idle", level: 0 });
  }

  /** Clears an error so the button is usable again. */
  reset(): void {
    if (this.state.phase === "error") this.set({ phase: "idle", error: "" });
  }

  private waitSettled(): Promise<void> {
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        this.settled = null;
        resolve();
      };
      const timer = setTimeout(done, SETTLE_MS);
      this.settled = done;
    });
  }

  private onLevel(level: number): void {
    this.set({ level });
    if (!this.clientCommits || this.state.phase !== "recording") return;
    const t = this.deps.now();
    if (level >= SPEECH_LEVEL) {
      if (!this.speaking) this.utteranceStart = t;
      this.speaking = this.heard = true;
      this.lastVoice = t;
    }
    const quiet = this.speaking && t - this.lastVoice >= SILENCE_MS;
    const long = this.speaking && t - this.utteranceStart >= MAX_UTTERANCE_MS;
    if (quiet || long) this.commit();
  }

  /** Text still grey, or a commit whose utterance hasn't been acknowledged yet. */
  private get unsettled(): boolean {
    return this.transcript.pending || this.outstanding > 0;
  }

  private commit(): void {
    this.mark("first_commit");
    this.speaking = this.heard = false;
    this.outstanding++;
    this.transport?.send({ type: "input_audio_buffer.commit" });
  }

  private onEvent(e: RealtimeEvent): void {
    const handler = this.handlers[e.type];
    if (!handler) return;
    if (e.type.startsWith("conversation.item.input_audio_transcription.") && e.type !== "conversation.item.input_audio_transcription.failed") this.mark("first_text");
    handler(e);
    this.deps.onText(this.transcript);
    if (!this.unsettled) this.settled?.();
  }

  /** An utterance came back (committed, or its result without a committed event first). */
  private acknowledged(id?: string): void {
    if (id) {
      if (this.acked.has(id)) return;
      this.acked.add(id);
    }
    this.outstanding = Math.max(0, this.outstanding - 1);
  }

  private handlers: Record<string, (e: RealtimeEvent) => void> = {
    "input_audio_buffer.committed": (e) => {
      this.acknowledged(e.item_id);
      if (e.item_id) this.transcript.committed(e.item_id);
    },
    "conversation.item.input_audio_transcription.delta": (e) => e.item_id && this.transcript.delta(e.item_id, e.delta ?? ""),
    "conversation.item.input_audio_transcription.completed": (e) => {
      if (!e.item_id) return;
      this.acknowledged(e.item_id);
      this.transcript.complete(e.item_id, e.transcript ?? "");
    },
    "conversation.item.input_audio_transcription.failed": (e) => {
      if (e.item_id) this.acknowledged(e.item_id);
      if (e.item_id) this.transcript.drop(e.item_id);
      this.deps.onNotice?.("有一段语音没有识别出来");
    },
    error: (e) => {
      // Committing with too little audio is expected after a short noise.
      if (e.error?.code === "input_audio_buffer_commit_empty") return this.acknowledged();
      this.deps.onNotice?.(`语音识别出错：${e.error?.message ?? "未知错误"}`);
    },
  };

  private onDrop(message?: string): void {
    if (!this.active) return;
    this.transport = null;
    this.transcript.settle();
    this.deps.onText(this.transcript);
    this.settled?.();
    this.fail(message || "和语音服务的连接断开了");
  }

  private fail(message: string): void {
    this.logTimings(`error: ${message}`);
    this.release();
    this.set({ phase: "error", error: message, level: 0 });
  }

  private clock(): number {
    return this.deps.clock ? this.deps.clock() : performance.now();
  }

  /** First time this milestone was reached in the current recording, in ms since the click. */
  private mark(name: string): void {
    if (!(name in this.timings)) this.timings[name] = Math.round(this.clock() - this.t0);
  }

  private logTimings(outcome: string): void {
    const data = { outcome, transport: this.transportName, ...this.timings };
    if (this.deps.log) this.deps.log("[voice] timings", data);
    else console.debug("[voice] timings", data);
  }

  private closeMic(): void {
    this.stopMeter?.();
    this.stopMeter = null;
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = null;
  }

  private release(): void {
    if (this.limitTimer) clearTimeout(this.limitTimer);
    this.limitTimer = null;
    this.closeMic();
    const t = this.transport;
    this.transport = null;
    t?.close();
    this.speaking = this.heard = false;
    this.outstanding = 0;
    this.acked.clear();
    this.connecting = null;
  }

  private set(patch: Partial<VoiceState>): void {
    Object.assign(this.state, patch);
  }
}
