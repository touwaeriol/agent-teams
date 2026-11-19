// The default transport: microphone audio goes to our own server over a WebSocket
// (GET /api/voice/stream) and the server relays it to OpenAI, so the browser only ever talks
// to this site. Browser → server: 24 kHz PCM16 frames (binary) and {"type":"commit"|"stop"};
// server → browser: {"type":"ready"}, transcript events, and {"type":"error","fatal":true} before
// it closes. The CSRF token travels as the subprotocol "csrf.<token>" (no headers on a WebSocket).
//
// Recording does not wait for the server: the socket opens while the microphone is still
// opening, capture starts as soon as the microphone is there, and frames recorded before the
// server's ready (OpenAI accepted the session) are kept in order (at most MAX_BUFFER_BYTES, the
// oldest dropped beyond) and sent first once it comes. Commits wait for ready and their audio.
import { csrfToken } from "@/api/client";
import { CONNECT_TIMEOUT, ConnectError, type Connect, type RealtimeEvent, type Transport, type TransportHandlers, type VoiceSessionInfo } from "./openai";
import { OUT_RATE, startCapture, type Capture } from "./pcm";

export const RELAY_PATH = "/api/voice/stream";
export const RELAY_PROTOCOL = "agent-teams-voice";
/** Audio kept while connecting: 8 s of 24 kHz PCM16 (the server allows a 10 s burst from Accept). */
export const MAX_BUFFER_BYTES = 8 * OUT_RATE * 2;
export const BUFFER_DROPPED = "连接语音服务较慢，最早的几秒录音没有发出去";

export interface RelayDeps {
  /** WebSocket constructor (a fake in tests). */
  WebSocket: new (url: string, protocols: string[]) => WebSocket;
  capture(stream: MediaStream, onFrame: (frame: ArrayBuffer) => void): Promise<Capture>;
  url(language: string): string;
  csrf(): string;
  timeoutMs: number;
}

/** A relay "session": nothing to mint, the server holds the key. */
export function relaySession(language: string): VoiceSessionInfo {
  return { provider: "openai", transport: "relay", connect_url: RELAY_PATH, client_secret: "", expires_at: 0, model: "", language, client_commits: true };
}

export function relayURL(language: string, loc: { protocol: string; host: string } = location): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${RELAY_PATH}?language=${encodeURIComponent(language || "auto")}`;
}

const browserDeps = (): RelayDeps => ({
  WebSocket: globalThis.WebSocket,
  capture: startCapture,
  url: (language) => relayURL(language),
  csrf: csrfToken,
  timeoutMs: CONNECT_TIMEOUT,
});

interface ServerMessage extends RealtimeEvent {
  fatal?: boolean;
  model?: string;
  client_commits?: boolean;
  max_seconds?: number;
}

export function makeConnectRelay(deps: () => RelayDeps = browserDeps): Connect {
  return (session, stream, h) => new RelayConnection(deps(), h).open(session.language, stream);
}

export const connectRelay: Connect = makeConnectRelay();

class RelayConnection {
  private ws: WebSocket | null = null;
  private capture: Capture | null = null;
  private closed = false;
  private fatal = "";
  /** The server said ready: frames go straight out. */
  private live = false;
  private pending: ArrayBuffer[] = [];
  private pendingBytes = 0;
  private dropped = false;
  private sentFrame = false;
  /** Why connecting failed, once it did. */
  private failure: ConnectError | null = null;
  /** open() has handed the transport out: from then on a drop is reported through onClose. */
  private handedOut = false;
  /** Rejects when the connection drops after ready but before open() returned. */
  private diedEarly: ((e: ConnectError) => void) | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private ready: ((m: ServerMessage) => void) | null = null;
  private failed: ((e: ConnectError) => void) | null = null;
  /** Settles with the server's ready (or the reason connecting failed). */
  private accepted: Promise<ServerMessage> = Promise.resolve({ type: "ready" });
  /** Commits in order (each waits for ready and flushes the frame buffer first); stop goes after the last one. */
  private chain: Promise<void> = Promise.resolve();

  constructor(
    private deps: RelayDeps,
    private h: TransportHandlers,
  ) {}

  async open(language: string, stream: Promise<MediaStream>): Promise<Transport> {
    this.accepted = this.connect(language);
    this.accepted.catch(() => {});
    // Connecting may fail before the microphone opens: don't wait for it then.
    const lost = new Promise<never>((_, reject) => void this.accepted.catch(reject));
    // …or drop after ready while the microphone is still opening (up to MIC_TIMEOUT).
    const gone = new Promise<never>((_, reject) => (this.diedEarly = reject));
    gone.catch(() => {});
    let media: MediaStream;
    try {
      media = await Promise.race([stream, lost, gone]);
    } catch (e) {
      this.abort();
      throw e;
    }
    try {
      this.capture = await this.deps.capture(media, (frame) => this.onFrame(frame));
    } catch (e) {
      this.abort();
      throw new ConnectError(0, e instanceof Error && e.message ? e.message : "打不开录音");
    }
    // Failed (or closed) while the capture was starting: don't leave the microphone captured.
    if (this.failure || this.closed) {
      this.capture.stop();
      this.capture = null;
      if (this.failure) throw this.failure;
    }
    const t: Transport = { send: (event) => this.send(event), close: () => this.close() };
    t.ready = this.accepted.then((hello) => {
      t.clientCommits = hello.client_commits ?? true;
      t.maxSeconds = hello.max_seconds;
    });
    t.ready.catch(() => {}); // a cancelled transport's ready is nobody's business
    this.handedOut = true;
    this.diedEarly = null;
    return t;
  }

  /** Opens the socket; the promise settles with the server's ready (OpenAI accepted the session). */
  private connect(language: string): Promise<ServerMessage> {
    return new Promise((resolve, reject) => {
      this.timer = setTimeout(() => fail(`${this.deps.timeoutMs / 1000} 秒内没有连上`), this.deps.timeoutMs);
      const fail = (reason: string) => {
        clearTimeout(this.timer);
        this.ready = this.failed = null;
        this.shut();
        this.failure = new ConnectError(0, reason.startsWith("连不上语音服务") ? reason : `连不上语音服务：${reason}`);
        reject(this.failure);
      };
      this.ready = (m) => {
        clearTimeout(this.timer);
        this.ready = this.failed = null;
        this.h.mark?.("ready");
        // What was recorded while connecting goes first, in order.
        this.live = true;
        const pending = this.pending;
        this.pending = [];
        this.pendingBytes = 0;
        for (const frame of pending) this.sendFrame(frame);
        resolve(m);
      };
      this.failed = (e) => fail(e.message);
      let ws: WebSocket;
      try {
        ws = new this.deps.WebSocket(this.deps.url(language), [RELAY_PROTOCOL, `csrf.${this.deps.csrf()}`]);
      } catch (e) {
        return fail(e instanceof Error ? e.message : "地址不对");
      }
      ws.binaryType = "arraybuffer";
      ws.onopen = () => this.h.mark?.("ws_open");
      ws.onmessage = (m) => this.onMessage(m.data);
      ws.onclose = () => this.onSocketClose();
      ws.onerror = () => {};
      this.ws = ws;
    });
  }

  /** Gives up connecting (cancelled, or the microphone failed): nothing is sent. */
  private abort(): void {
    if (this.failed) this.failed(new ConnectError(0, "已取消"));
    else this.shut();
  }

  private onFrame(frame: ArrayBuffer): void {
    if (this.closed) return;
    if (this.live) return this.sendFrame(frame);
    this.pending.push(frame);
    this.pendingBytes += frame.byteLength;
    while (this.pendingBytes > MAX_BUFFER_BYTES && this.pending.length > 1) {
      this.pendingBytes -= (this.pending.shift() as ArrayBuffer).byteLength;
      if (!this.dropped) {
        this.dropped = true;
        this.h.onNotice?.(BUFFER_DROPPED);
      }
    }
  }

  private sendFrame(frame: ArrayBuffer): void {
    if (this.ws?.readyState !== 1) return;
    this.ws.send(frame);
    if (!this.sentFrame) {
      this.sentFrame = true;
      this.h.mark?.("first_frame");
    }
  }

  private onMessage(data: unknown): void {
    if (typeof data !== "string") return;
    let m: ServerMessage;
    try {
      m = JSON.parse(data) as ServerMessage;
    } catch {
      return;
    }
    if (m.type === "ready") return this.ready?.(m);
    if (m.type === "error" && m.fatal) {
      this.fatal = m.error?.message || "语音服务出错";
      return this.failed?.(new ConnectError(0, this.fatal));
    }
    if (this.live) this.h.onEvent(m);
  }

  private onSocketClose(): void {
    if (this.failed) return this.failed(new ConnectError(0, this.fatal || "连接被拒绝或网络不通，请刷新页面后重试"));
    if (this.closed) return;
    this.closed = true;
    this.capture?.stop();
    if (!this.handedOut) {
      // Ready came, but the connection died before the microphone opened (or while the capture
      // started): open() fails with the reason instead of handing out a dead transport.
      const reason = this.fatal || "和语音服务的连接断开了";
      this.failure = new ConnectError(0, reason.startsWith("连不上语音服务") ? reason : `连不上语音服务：${reason}`);
      this.shut();
      this.diedEarly?.(this.failure);
      return;
    }
    this.h.onClose(this.fatal || undefined);
  }

  private send(event: Record<string, unknown>): void {
    if (event.type !== "input_audio_buffer.commit") return;
    // After ready (and the buffered audio), audio still in the frame buffer belongs to this utterance: send it before the commit.
    this.chain = this.chain
      .then(async () => {
        const hello = await this.accepted;
        // Made before ready on the assumption of client commits; a server-VAD model ends utterances itself.
        if (hello.client_commits === false) return;
        await this.capture?.flush();
        this.sendRaw(JSON.stringify({ type: "commit" }));
      })
      .catch(() => {});
  }

  private sendRaw(data: string): void {
    if (this.ws?.readyState === 1) this.ws.send(data);
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    // Still connecting (cancelled): drop the buffered audio and the queued commits.
    if (!this.live) return this.abort();
    void this.chain.then(() => {
      this.sendRaw(JSON.stringify({ type: "stop" }));
      this.shut();
    });
  }

  private shut(): void {
    clearTimeout(this.timer);
    this.capture?.stop();
    this.capture = null;
    this.pending = [];
    this.pendingBytes = 0;
    const ws = this.ws;
    this.ws = null;
    if (!ws) return;
    ws.onmessage = ws.onclose = ws.onopen = null;
    try {
      ws.close(1000);
    } catch {
      // already closed
    }
  }
}
