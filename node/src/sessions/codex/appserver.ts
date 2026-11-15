import { spawn, type ChildProcess } from "node:child_process";
import { EventEmitter } from "node:events";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { createInterface } from "node:readline";
import type { Logger } from "../../util/log.js";

type Id = number | string;

export interface RpcError {
  code: number;
  message: string;
  data?: unknown;
}

export class RpcFailure extends Error {
  constructor(readonly method: string, readonly error: RpcError) {
    super(`${method}: ${error.message}`);
  }
}

export interface AppServerEvents {
  notification: [string, Record<string, unknown>];
  exit: [Error];
}

export type ServerRequestHandler = (method: string, params: Record<string, unknown>, id: string) => Promise<unknown>;

export interface AppServerOptions {
  command?: string[];
  env: Record<string, string>;
  cwd?: string;
  log: Logger;
  clientVersion: string;
}

/** Resolves the pinned codex CLI (node_modules/@openai/codex/bin/codex.js). */
export function codexCommand(): string[] {
  const require = createRequire(import.meta.url);
  const pkg = require.resolve("@openai/codex/package.json");
  return [process.execPath, join(dirname(pkg), "bin", "codex.js")];
}

/**
 * JSON-RPC client for `codex app-server` over stdio (newline-delimited JSON). Server →
 * client requests (approvals) go to the request handler; notifications are emitted.
 */
export class AppServer extends EventEmitter<AppServerEvents> {
  private child: ChildProcess | null = null;
  private nextId = 1;
  private readonly calls = new Map<Id, { method: string; resolve: (v: unknown) => void; reject: (e: Error) => void }>();
  private handler: ServerRequestHandler = async (method) => {
    throw new Error(`unsupported server request ${method}`);
  };
  initialize: Record<string, unknown> | null = null;

  constructor(private readonly opts: AppServerOptions) {
    super();
  }

  get running(): boolean {
    return this.child !== null;
  }

  onRequest(handler: ServerRequestHandler): void {
    this.handler = handler;
  }

  async start(): Promise<void> {
    if (this.child) return;
    const [cmd, ...args] = this.opts.command ?? codexCommand();
    const child = spawn(cmd!, [...args, "app-server"], { env: this.opts.env, cwd: this.opts.cwd, stdio: ["pipe", "pipe", "pipe"] });
    this.child = child;
    createInterface({ input: child.stdout! }).on("line", (line) => this.onLine(line));
    createInterface({ input: child.stderr! }).on("line", (line) => this.opts.log.debug({ codex: line.slice(0, 2000) }, "codex stderr"));
    child.on("exit", (code, signal) => this.onExit(child, new Error(`codex app-server exited (${code ?? signal})`)));
    child.on("error", (err) => this.onExit(child, err));
    this.initialize = (await this.request("initialize", {
      clientInfo: { name: "agent-teams-node", title: "agent-teams", version: this.opts.clientVersion },
      capabilities: { experimentalApi: true, requestAttestation: false },
    })) as Record<string, unknown>;
    this.notify("initialized", {});
  }

  stop(): void {
    const child = this.child;
    this.child = null;
    child?.kill("SIGTERM");
  }

  request<T = unknown>(method: string, params: unknown): Promise<T> {
    if (!this.child) return Promise.reject(new Error("codex app-server is not running"));
    const id = this.nextId++;
    return new Promise<T>((resolve, reject) => {
      this.calls.set(id, { method, resolve: resolve as (v: unknown) => void, reject });
      this.write({ id, method, params });
    });
  }

  notify(method: string, params: unknown): void {
    this.write({ method, params });
  }

  private write(msg: unknown): void {
    this.child?.stdin?.write(JSON.stringify(msg) + "\n");
  }

  private onLine(line: string): void {
    const msg = parse(line);
    if (!msg) return;
    if ("method" in msg && "id" in msg) return void this.serveRequest(msg.id as Id, String(msg.method), (msg.params ?? {}) as Record<string, unknown>);
    if ("method" in msg) return void this.emit("notification", String(msg.method), (msg.params ?? {}) as Record<string, unknown>);
    if ("id" in msg) this.settle(msg as { id: Id; result?: unknown; error?: RpcError });
  }

  private settle(msg: { id: Id; result?: unknown; error?: RpcError }): void {
    const call = this.calls.get(msg.id);
    if (!call) return;
    this.calls.delete(msg.id);
    if (msg.error) call.reject(new RpcFailure(call.method, msg.error));
    else call.resolve(msg.result);
  }

  private async serveRequest(id: Id, method: string, params: Record<string, unknown>): Promise<void> {
    try {
      this.write({ id, result: await this.handler(method, params, String(id)) });
    } catch (err) {
      this.write({ id, error: { code: -32601, message: (err as Error).message } });
    }
  }

  private onExit(child: ChildProcess, err: Error): void {
    if (this.child === child) this.child = null;
    for (const [id, call] of this.calls) {
      this.calls.delete(id);
      call.reject(err);
    }
    this.emit("exit", err);
  }
}

function parse(line: string): Record<string, unknown> | null {
  try {
    const v = JSON.parse(line);
    return v && typeof v === "object" ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}
