import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { WebSocketServer, type WebSocket } from "ws";
// The protocol package's own ajv setup: every frame the daemon sends must validate.
import { createAjv, describeErrors, messageValidator } from "../../../protocol/scripts/validator.mjs";

export interface Frame {
  v: 2;
  type: string;
  id: string;
  seq?: number;
  ts: string;
  body: Record<string, any>;
}

let counter = 0;

/** Minimal console for tests: accepts /nodes/connect, answers hello, records and validates frames. */
export class FakeConsole {
  readonly frames: Frame[] = [];
  readonly invalid: string[] = [];
  connections = 0;
  autoAck = true;
  ackSeq = 0;
  welcomeAckSeq: number | null = null;
  welcomeConfig: Record<string, unknown> = { park_after_s: 900, max_active_sessions: 4 };
  token = "test-token";
  /** Images served at GET /nodes/attachments/{id} (node token required). */
  readonly images = new Map<string, Buffer>();
  private seq = 0;
  private ws: WebSocket | null = null;
  private http: Server | null = null;
  private wss: WebSocketServer | null = null;
  private readonly validate = messageValidator(createAjv());
  private waiters: Array<() => void> = [];

  async start(): Promise<string> {
    this.http = createServer((req, res) => this.serveImage(req.url ?? "", req.headers.authorization, res));
    this.wss = new WebSocketServer({ noServer: true });
    this.http.on("upgrade", (req, socket, head) => {
      if (req.url !== "/nodes/connect" || req.headers.authorization !== `Bearer ${this.token}`) return socket.destroy();
      this.wss!.handleUpgrade(req, socket, head, (ws) => this.onConnection(ws));
    });
    await new Promise<void>((r) => this.http!.listen(0, "127.0.0.1", r));
    return `ws://127.0.0.1:${(this.http.address() as AddressInfo).port}/nodes/connect`;
  }

  private serveImage(url: string, auth: string | undefined, res: import("node:http").ServerResponse): void {
    const id = /^\/nodes\/attachments\/([^/?]+)$/.exec(url)?.[1];
    const data = id ? this.images.get(decodeURIComponent(id)) : undefined;
    if (auth !== `Bearer ${this.token}`) return void res.writeHead(401).end();
    if (!data) return void res.writeHead(404).end();
    res.writeHead(200, { "Content-Type": "image/png" }).end(data);
  }

  async stop(): Promise<void> {
    for (const c of this.wss?.clients ?? []) c.terminate();
    await new Promise<void>((r) => this.wss?.close(() => r()) ?? r());
    await new Promise<void>((r) => this.http?.close(() => r()) ?? r());
  }

  private onConnection(ws: WebSocket): void {
    this.connections++;
    this.ws = ws;
    ws.on("message", (data) => this.onFrame(ws, JSON.parse(data.toString()) as Frame));
  }

  private onFrame(ws: WebSocket, f: Frame): void {
    if (!this.validate(f)) this.invalid.push(`${f.type}: ${describeErrors(this.validate)}`);
    this.frames.push(f);
    if (f.type === "hello") this.welcome(ws, f);
    if (f.seq !== undefined && this.autoAck) this.ack(f.seq);
    for (const w of this.waiters.splice(0)) w();
  }

  private welcome(ws: WebSocket, hello: Frame): void {
    const ack = this.welcomeAckSeq ?? this.ackSeq;
    ws.send(JSON.stringify(this.envelope("welcome", { protocol: 2, node_id: "node-1", ack_seq: ack, server_time: new Date().toISOString(), config: this.welcomeConfig, features: hello.body.features })));
  }

  ack(seq: number): void {
    this.ackSeq = Math.max(this.ackSeq, seq);
    this.ws?.send(JSON.stringify(this.envelope("ack", { seq: this.ackSeq })));
  }

  /** Sends a reliable command (new seq) and returns the frame. */
  command(type: string, body: Record<string, unknown>, opts: { seq?: number; id?: string } = {}): Frame {
    const f = { ...this.envelope(type, body), seq: opts.seq ?? ++this.seq, ...(opts.id ? { id: opts.id } : {}) };
    this.ws?.send(JSON.stringify(f));
    return f;
  }

  unreliable(type: string, body: Record<string, unknown>): void {
    this.ws?.send(JSON.stringify(this.envelope(type, body)));
  }

  dropConnection(): void {
    this.ws?.terminate();
    this.ws = null;
  }

  envelope(type: string, body: Record<string, unknown>): Frame {
    return { v: 2, type, id: `c_${String(++counter).padStart(26, "0")}`, ts: new Date().toISOString(), body };
  }

  ofType(type: string): Frame[] {
    return this.frames.filter((f) => f.type === type);
  }

  /** Waits until a frame matching the predicate has arrived (searching all frames so far). */
  async waitFor(pred: (f: Frame) => boolean, timeoutMs = 10_000, what = "frame"): Promise<Frame> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const hit = this.frames.find(pred);
      if (hit) return hit;
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}; got: ${this.frames.map((f) => f.type).join(", ")}`);
      await new Promise<void>((r) => {
        const t = setTimeout(r, 100);
        this.waiters.push(() => {
          clearTimeout(t);
          r();
        });
      });
    }
  }

  waitType(type: string, pred: (b: Record<string, any>) => boolean = () => true, timeoutMs?: number): Promise<Frame> {
    return this.waitFor((f) => f.type === type && pred(f.body), timeoutMs, type);
  }
}
