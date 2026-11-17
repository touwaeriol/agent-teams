// The default transport: microphone audio goes to our own server over a WebSocket
// (GET /api/voice/stream) and the server relays it to OpenAI, so the browser only ever talks
// to this site. Browser → server: 24 kHz PCM16 frames (binary) and {"type":"commit"|"stop"};
// server → browser: {"type":"ready"}, transcript events, and {"type":"error","fatal":true} before
// it closes. The CSRF token travels as the subprotocol "csrf.<token>" (no headers on a WebSocket).
import { csrfToken } from "@/api/client";
import { CONNECT_TIMEOUT, ConnectError, type Connect, type RealtimeEvent, type Transport, type TransportHandlers, type VoiceSessionInfo } from "./openai";
import { startCapture, type Capture } from "./pcm";

export const RELAY_PATH = "/api/voice/stream";
export const RELAY_PROTOCOL = "agent-teams-voice";

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
  private ready: ((m: ServerMessage) => void) | null = null;
  private failed: ((e: ConnectError) => void) | null = null;
  /** Commits in order (each flushes the frame buffer first); stop goes after the last one. */
  private chain: Promise<void> = Promise.resolve();

  constructor(
    private deps: RelayDeps,
    private h: TransportHandlers,
  ) {}

  async open(language: string, stream: MediaStream): Promise<Transport> {
    const hello = await this.connect(language);
    try {
      this.capture = await this.deps.capture(stream, (frame) => this.sendRaw(frame));
    } catch (e) {
      this.close();
      throw new ConnectError(0, e instanceof Error && e.message ? e.message : "打不开录音");
    }
    return {
      send: (event) => this.send(event),
      close: () => this.close(),
      clientCommits: hello.client_commits ?? true,
      maxSeconds: hello.max_seconds,
    };
  }

  /** Opens the socket and waits for the server's ready (OpenAI accepted the session). */
  private connect(language: string): Promise<ServerMessage> {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => fail(`${this.deps.timeoutMs / 1000} 秒内没有连上`), this.deps.timeoutMs);
      const fail = (reason: string) => {
        clearTimeout(timer);
        this.ready = this.failed = null;
        this.shut();
        reject(new ConnectError(0, reason.startsWith("连不上语音服务") ? reason : `连不上语音服务：${reason}`));
      };
      this.ready = (m) => {
        clearTimeout(timer);
        this.ready = this.failed = null;
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
      ws.onmessage = (m) => this.onMessage(m.data);
      ws.onclose = () => this.onSocketClose();
      ws.onerror = () => {};
      this.ws = ws;
    });
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
    if (!this.ready) this.h.onEvent(m);
  }

  private onSocketClose(): void {
    if (this.failed) return this.failed(new ConnectError(0, this.fatal || "连接被拒绝或网络不通，请刷新页面后重试"));
    if (this.closed) return;
    this.closed = true;
    this.capture?.stop();
    this.h.onClose(this.fatal || undefined);
  }

  private send(event: Record<string, unknown>): void {
    if (event.type !== "input_audio_buffer.commit") return;
    // Audio still in the frame buffer belongs to this utterance: send it before the commit.
    this.chain = this.chain.then(async () => {
      await this.capture?.flush();
      this.sendRaw(JSON.stringify({ type: "commit" }));
    });
  }

  private sendRaw(data: ArrayBuffer | string): void {
    if (this.ws?.readyState === 1) this.ws.send(data);
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    void this.chain.then(() => {
      this.sendRaw(JSON.stringify({ type: "stop" }));
      this.shut();
    });
  }

  private shut(): void {
    this.capture?.stop();
    this.capture = null;
    const ws = this.ws;
    this.ws = null;
    if (!ws) return;
    ws.onmessage = ws.onclose = null;
    try {
      ws.close(1000);
    } catch {
      // already closed
    }
  }
}
