// The browser's connection to OpenAI Realtime transcription (WebRTC, as OpenAI recommends for
// browsers): the microphone track goes up, transcript events come back on the "oai-events" data
// channel. Authenticated with the short-lived client secret from POST /api/voice/session; our
// server never sees the audio.

export interface VoiceSessionInfo {
  provider: string;
  transport: "webrtc";
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
}

export interface TransportHandlers {
  onEvent(e: RealtimeEvent): void;
  /** The connection dropped (not called after close()). */
  onClose(): void;
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

const OPEN_TIMEOUT = 10_000;

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
  try {
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    const res = await fetch(session.connect_url, {
      method: "POST",
      body: offer.sdp,
      headers: { Authorization: `Bearer ${session.client_secret}`, "Content-Type": "application/sdp" },
    }).catch(() => {
      throw new ConnectError(0, "连不上语音服务");
    });
    if (!res.ok) throw new ConnectError(res.status, `语音服务拒绝了连接（${res.status}）`);
    await pc.setRemoteDescription({ type: "answer", sdp: await res.text() });
    await opened(dc);
  } catch (e) {
    closed = true;
    pc.close();
    throw e;
  }
  return { send: (event) => dc.readyState === "open" && dc.send(JSON.stringify(event)), close };
};

function opened(dc: RTCDataChannel): Promise<void> {
  if (dc.readyState === "open") return Promise.resolve();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new ConnectError(0, "连接语音服务超时")), OPEN_TIMEOUT);
    dc.addEventListener("open", () => (clearTimeout(timer), resolve()), { once: true });
  });
}
