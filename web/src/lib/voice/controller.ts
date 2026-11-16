// One composer's voice input: idle → connecting → recording → stopping → idle (or error).
// Framework-free; the composer wraps it (useVoiceInput) and tests drive it with fakes.
//
// gpt-realtime-whisper has no server VAD, so with client_commits the controller ends each
// utterance itself: after speech followed by SILENCE_MS of quiet (or MAX_UTTERANCE_MS of talking)
// it sends input_audio_buffer.commit, and once more when recording stops; the completed
// transcript of each committed item then replaces its grey partial text.
import { micErrorText } from "./audio";
import { ConnectError, type Connect, type RealtimeEvent, type Transport, type VoiceSessionInfo } from "./openai";
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
}

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
    this.transcript.begin(opts.text, opts.selStart, opts.selEnd);
    this.set({ phase: "connecting", error: "", level: 0 });
    try {
      this.stream = await this.deps.openMic(opts.deviceId);
    } catch (e) {
      return this.fail(micErrorText(e));
    }
    if (run !== this.run) return this.release();
    try {
      await this.open(opts.language, run);
    } catch (e) {
      if (run === this.run) this.fail(e instanceof Error ? e.message : "连不上语音服务");
      return;
    }
    if (run !== this.run) return this.release();
    this.set({ phase: "recording" });
    this.stopMeter = this.deps.meter(this.stream, (level) => this.onLevel(level));
  }

  /** Mints a client secret and connects; a rejected (expired) secret is reminted once. */
  private async open(language: string, run: number): Promise<void> {
    for (let attempt = 0; ; attempt++) {
      const session = await this.deps.mint(language);
      if (run !== this.run) return;
      if (session.expires_at && session.expires_at - SECRET_MARGIN_S < this.deps.now() / 1000 && attempt === 0) continue;
      this.clientCommits = session.client_commits;
      try {
        this.transport = await this.deps.connect(session, this.stream as MediaStream, {
          onEvent: (e) => this.onEvent(e),
          onClose: () => this.onDrop(),
        });
        return;
      } catch (e) {
        const expired = e instanceof ConnectError && (e.status === 401 || e.status === 403);
        if (!expired || attempt > 0) throw e;
      }
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
    if (this.transcript.pending) await this.waitSettled();
    this.transcript.settle();
    this.deps.onText(this.transcript);
    this.release();
    this.set({ phase: "idle" });
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

  private commit(): void {
    this.speaking = this.heard = false;
    this.transport?.send({ type: "input_audio_buffer.commit" });
  }

  private onEvent(e: RealtimeEvent): void {
    const handler = this.handlers[e.type];
    if (!handler) return;
    handler(e);
    this.deps.onText(this.transcript);
    if (!this.transcript.pending) this.settled?.();
  }

  private handlers: Record<string, (e: RealtimeEvent) => void> = {
    "input_audio_buffer.committed": (e) => e.item_id && this.transcript.committed(e.item_id),
    "conversation.item.input_audio_transcription.delta": (e) => e.item_id && this.transcript.delta(e.item_id, e.delta ?? ""),
    "conversation.item.input_audio_transcription.completed": (e) => e.item_id && this.transcript.complete(e.item_id, e.transcript ?? ""),
    "conversation.item.input_audio_transcription.failed": (e) => {
      if (e.item_id) this.transcript.drop(e.item_id);
      this.deps.onNotice?.("有一段语音没有识别出来");
    },
    error: (e) => {
      // Committing with too little audio is expected after a short noise.
      if (e.error?.code === "input_audio_buffer_commit_empty") return;
      this.deps.onNotice?.(`语音识别出错：${e.error?.message ?? "未知错误"}`);
    },
  };

  private onDrop(): void {
    if (!this.active) return;
    this.transport = null;
    this.transcript.settle();
    this.deps.onText(this.transcript);
    this.settled?.();
    this.fail("和语音服务的连接断开了");
  }

  private fail(message: string): void {
    this.release();
    this.set({ phase: "error", error: message, level: 0 });
  }

  private closeMic(): void {
    this.stopMeter?.();
    this.stopMeter = null;
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = null;
  }

  private release(): void {
    this.closeMic();
    const t = this.transport;
    this.transport = null;
    t?.close();
    this.speaking = this.heard = false;
  }

  private set(patch: Partial<VoiceState>): void {
    Object.assign(this.state, patch);
  }
}
