import { EventEmitter } from "node:events";
import WebSocket from "ws";
import { newId } from "../util/ids.js";
import type { Logger } from "../util/log.js";
import { ProtocolValidator, isMessageType, isReliable, type Frame, type Hello, type Welcome } from "../protocol.js";
import type { Outbox } from "./outbox.js";

export const CLOSE = { normal: 1000, unauthorized: 4401, replaced: 4409, upgrade: 4426, tooFast: 4429 } as const;

export interface LinkOptions {
  url: string;
  token: string;
  outbox: Outbox;
  hello: () => Hello;
  log: Logger;
  pingMs?: number;
  deadMs?: number;
  welcomeTimeoutMs?: number;
  backoff?: { minMs: number; maxMs: number };
  maxPayload?: number;
}

export interface LinkEvents {
  welcome: [Welcome];
  frame: [Frame];
  stopped: [string];
}

const MAX_FRAME = 4 * 1024 * 1024;

/**
 * WebSocket link to <console>/nodes/connect (protocol.md §1, §3): hello → welcome →
 * outbox replay, 20 s pings, 45 s dead-link detection, exponential reconnect with
 * jitter, close-code handling, cumulative ACKs for console commands.
 */
export class Link extends EventEmitter<LinkEvents> {
  private ws: WebSocket | null = null;
  private ready = false;
  private stopped = false;
  private attempt = 0;
  private lastFrameAt = 0;
  private heartbeat: NodeJS.Timeout | null = null;
  private reconnectTimer: NodeJS.Timeout | null = null;
  private welcomeTimer: NodeJS.Timeout | null = null;
  private ackTimer: NodeJS.Timeout | null = null;
  private pendingAck = 0;
  private unackedCount = 0;
  private sentAck = 0;
  private readonly validator = new ProtocolValidator();

  constructor(private readonly opts: LinkOptions) {
    super();
  }

  get connected(): boolean {
    return this.ready;
  }

  start(): void {
    this.stopped = false;
    this.connect();
  }

  stop(reason = "stopped"): void {
    this.stopped = true;
    this.clearTimers();
    this.ws?.close(CLOSE.normal, reason);
    this.ws = null;
    this.ready = false;
  }

  /** Sends a frame: reliable types go through the outbox (persist, then send when connected). */
  send(type: string, body: object): void {
    if (isMessageType(type) && isReliable(type)) {
      const frame = this.opts.outbox.append(type, body);
      if (this.ready) this.write(frame);
      return;
    }
    if (this.ready) this.write({ v: 2, type, id: newId("n_"), ts: new Date().toISOString(), body: body as Record<string, unknown> });
  }

  /** Marks a console command seq as durably accepted; sent as a cumulative ACK. */
  ackCommand(seq: number): void {
    this.pendingAck = Math.max(this.pendingAck, seq);
    this.unackedCount++;
    if (this.unackedCount >= 50) return this.flushAck();
    this.ackTimer ??= setTimeout(() => this.flushAck(), 200);
  }

  /** Reports a protocol error about one inbound message. */
  sendError(refId: string | undefined, code: string, message: string): void {
    this.send("error", { ...(refId ? { ref_id: refId } : {}), code, message });
  }

  private flushAck(): void {
    if (this.ackTimer) clearTimeout(this.ackTimer);
    this.ackTimer = null;
    this.unackedCount = 0;
    if (!this.ready || this.pendingAck <= this.sentAck) return;
    this.sentAck = this.pendingAck;
    this.send("ack", { seq: this.pendingAck });
  }

  private connect(): void {
    if (this.stopped) return;
    const ws = new WebSocket(this.opts.url, {
      headers: { Authorization: `Bearer ${this.opts.token}` },
      perMessageDeflate: true,
      maxPayload: this.opts.maxPayload ?? MAX_FRAME,
      handshakeTimeout: 15_000,
    });
    this.ws = ws;
    ws.on("open", () => this.onOpen(ws));
    ws.on("message", (data) => this.onMessage(ws, data.toString()));
    ws.on("pong", () => (this.lastFrameAt = Date.now()));
    ws.on("ping", () => (this.lastFrameAt = Date.now()));
    ws.on("close", (code, reason) => this.onClose(ws, code, reason.toString()));
    ws.on("error", (err) => this.opts.log.warn({ err: err.message }, "link error"));
    ws.on("unexpected-response", (_req, res) => this.onUnexpected(ws, res.statusCode ?? 0));
  }

  private onOpen(ws: WebSocket): void {
    this.lastFrameAt = Date.now();
    this.write({ v: 2, type: "hello", id: newId("n_"), ts: new Date().toISOString(), body: this.opts.hello() as unknown as Record<string, unknown> }, ws);
    this.welcomeTimer = setTimeout(() => ws.terminate(), this.opts.welcomeTimeoutMs ?? 15_000);
    this.startHeartbeat(ws);
  }

  private onMessage(ws: WebSocket, text: string): void {
    if (ws !== this.ws) return;
    this.lastFrameAt = Date.now();
    const frame = parse(text);
    if (!frame) return this.sendError(undefined, "bad_schema", "not a JSON object");
    const v = this.validator.validateInbound(frame);
    if (!v.ok) return this.reject(frame, v.code, v.message);
    this.route(frame);
  }

  private reject(frame: Frame, code: string, message: string): void {
    this.opts.log.warn({ type: frame.type, code, message }, "rejected console frame");
    if (frame.type !== "error") this.sendError(typeof frame.id === "string" ? frame.id : undefined, code, message);
  }

  private route(frame: Frame): void {
    if (frame.type === "welcome") return this.onWelcome(frame.body as unknown as Welcome);
    if (frame.type === "ack") return this.opts.outbox.ack((frame.body as { seq: number }).seq);
    if (frame.type === "error") return void this.opts.log.warn({ body: frame.body }, "console reported an error");
    if (!this.ready) return;
    this.emit("frame", frame);
  }

  private onWelcome(w: Welcome): void {
    if (this.welcomeTimer) clearTimeout(this.welcomeTimer);
    if (w.protocol !== 2) return void this.ws?.close(CLOSE.upgrade, "protocol 2 required");
    this.ready = true;
    this.attempt = 0;
    this.opts.log.info({ node_id: w.node_id }, "connected to the console");
    this.opts.outbox.ack(w.ack_seq);
    for (const f of this.opts.outbox.unacked()) this.write(f);
    this.emit("welcome", w);
    this.flushAck();
  }

  private onUnexpected(ws: WebSocket, status: number): void {
    if (status === 401 || status === 403) this.opts.log.error({ status }, "console rejected the node token");
    ws.terminate();
    this.scheduleReconnect(status === 401 || status === 403 ? 600_000 : undefined);
  }

  private onClose(ws: WebSocket, code: number, reason: string): void {
    if (ws !== this.ws) return;
    this.ready = false;
    this.ws = null;
    this.clearTimers();
    if (this.stopped) return;
    this.opts.log.info({ code, reason }, "link closed");
    this.handleCloseCode(code);
  }

  private handleCloseCode(code: number): void {
    switch (code) {
      case CLOSE.unauthorized:
        this.opts.log.error("node token rejected (4401); retrying every 10 minutes");
        return this.scheduleReconnect(600_000);
      case CLOSE.replaced:
        this.stopped = true;
        this.opts.log.error("another daemon took over this node (4409); stopping");
        return void this.emit("stopped", "replaced");
      case CLOSE.upgrade:
        this.opts.log.error("console requires a newer daemon (4426)");
        return this.scheduleReconnect(600_000);
      case CLOSE.tooFast:
        return this.scheduleReconnect(60_000);
      default:
        return this.scheduleReconnect();
    }
  }

  private scheduleReconnect(fixedMs?: number): void {
    if (this.stopped || this.reconnectTimer) return;
    const delay = fixedMs ?? backoffDelay(this.attempt++, this.opts.backoff);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }

  private startHeartbeat(ws: WebSocket): void {
    const pingMs = this.opts.pingMs ?? 20_000;
    const deadMs = this.opts.deadMs ?? 45_000;
    this.heartbeat = setInterval(() => {
      if (Date.now() - this.lastFrameAt > deadMs) return ws.terminate();
      ws.ping();
    }, pingMs);
  }

  private write(frame: Frame, ws: WebSocket | null = this.ws): void {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify(frame));
  }

  private clearTimers(): void {
    for (const t of [this.heartbeat, this.welcomeTimer, this.ackTimer]) if (t) clearTimeout(t);
    this.heartbeat = this.welcomeTimer = this.ackTimer = null;
    this.sentAck = 0;
    this.unackedCount = 0;
  }
}

/** 1 s → 30 s exponential backoff with ±20% jitter (protocol.md §1). */
export function backoffDelay(attempt: number, b = { minMs: 1000, maxMs: 30_000 }): number {
  const base = Math.min(b.maxMs, b.minMs * 2 ** attempt);
  return Math.round(base * (0.8 + Math.random() * 0.4));
}

function parse(text: string): Frame | null {
  try {
    const v = JSON.parse(text);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Frame) : null;
  } catch {
    return null;
  }
}
