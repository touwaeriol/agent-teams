// Transports shared types, and the optional direct connection to OpenAI Realtime transcription
// (WebRTC): the microphone track goes up, transcript events come back on the "oai-events" data
// channel, authenticated with the short-lived client secret from POST /api/voice/session. It
// needs the browser to reach api.openai.com; the default is the server relay (relay.ts).

export interface VoiceSessionInfo {
  provider: string;
  transport: "webrtc" | "relay";
  connect_url: string;
  client_secret: string;
  expires_at: number;
  model: string;
  language: string;
  client_commits: boolean;
}

export interface RealtimeEvent {
  type: string;
  item_id?: string;
  delta?: string;
  transcript?: string;
  error?: { type?: string; code?: string; message?: string };
  [k: string]: unknown;
}

export interface Transport {
  send(event: Record<string, unknown>): void;
  close(): void;
  /** Set when the transport learns it on connect (relay: from the server's ready message). */
  clientCommits?: boolean;
  /** Longest recording the server allows, in seconds. */
  maxSeconds?: number;
}

export interface TransportHandlers {
  onEvent(e: RealtimeEvent): void;
  /** The connection dropped (not called after close()); message says why when known. */
  onClose(message?: string): void;
}

export type Connect = (session: VoiceSessionInfo, stream: MediaStream, h: TransportHandlers) => Promise<Transport>;

/** A failed connect; status 401/403 means the client secret was rejected (expired). */
export class ConnectError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/** Connecting (signalling plus the data channel) must finish within this. */
export const CONNECT_TIMEOUT = 10_000;

/** Why fetch failed, for "连不上语音服务：…". */
export function networkReason(e: unknown, timedOut: boolean): string {
  if (timedOut) return `${CONNECT_TIMEOUT / 1000} 秒内没有响应`;
  return e instanceof Error && e.message ? `网络不通（${e.message}）` : "网络不通";
}

export const connectOpenAI: Connect = async (session, stream, h) => {
  const pc = new RTCPeerConnection();
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    try {
      dc.close();
    } catch {
      // already closed
    }
    pc.close();
  };
  for (const track of stream.getAudioTracks()) pc.addTrack(track, stream);
  const dc = pc.createDataChannel("oai-events");
  dc.onmessage = (m) => {
    try {
      h.onEvent(JSON.parse(String(m.data)) as RealtimeEvent);
    } catch {
      // not JSON: ignore
    }
  };
  const lost = () => {
    if (closed) return;
    close();
    h.onClose();
  };
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === "failed" || pc.connectionState === "closed") lost();
  };
  dc.onclose = lost;
  const abort = new AbortController();
  let timedOut = false;
  const deadline = Date.now() + CONNECT_TIMEOUT;
  const timer = setTimeout(() => ((timedOut = true), abort.abort()), CONNECT_TIMEOUT);
  try {
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    const res = await fetch(session.connect_url, {
      method: "POST",
      body: offer.sdp,
      headers: { Authorization: `Bearer ${session.client_secret}`, "Content-Type": "application/sdp" },
      signal: abort.signal,
    }).catch((e) => {
      throw new ConnectError(0, `连不上语音服务：${networkReason(e, timedOut)}`);
    });
    if (!res.ok) throw new ConnectError(res.status, `连不上语音服务：OpenAI 拒绝了连接（${res.status}）`);
    await pc.setRemoteDescription({ type: "answer", sdp: await res.text() });
    await opened(dc, Math.max(0, deadline - Date.now()));
  } catch (e) {
    closed = true;
    pc.close();
    throw e;
  } finally {
    clearTimeout(timer);
  }
  return { send: (event) => dc.readyState === "open" && dc.send(JSON.stringify(event)), close };
};

function opened(dc: RTCDataChannel, ms: number): Promise<void> {
  if (dc.readyState === "open") return Promise.resolve();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new ConnectError(0, "连不上语音服务：浏览器和 OpenAI 之间的 WebRTC 连接没有建立（可能被网络或代理拦截）")), ms);
    dc.addEventListener("open", () => (clearTimeout(timer), resolve()), { once: true });
  });
}
