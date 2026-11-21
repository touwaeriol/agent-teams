// A minimal Agent Client Protocol (ACP, https://agentclientprotocol.com) client over stdio: one
// `agy acp --process-per-session` child per session, newline-delimited JSON-RPC 2.0 both ways.
// Agent → client requests (session/request_permission) go to the request handler; notifications
// (session/update) are emitted.
import { spawn, type ChildProcess } from "node:child_process";
import { EventEmitter } from "node:events";
import { createInterface } from "node:readline";
import type { Logger } from "../../util/log.js";

type Id = number | string;

export interface AcpError {
  code: number;
  message: string;
  data?: unknown;
}

/** ACP's "authentication required" (agy answers session/new with it when not logged in). */
export const ACP_AUTH_REQUIRED = -32000;

export class AcpFailure extends Error {
  constructor(readonly method: string, readonly error: AcpError) {
    super(`${method}: ${error.message}`);
  }
}

export interface AcpEvents {
  notification: [string, Record<string, unknown>];
  exit: [Error];
}

export type AcpRequestHandler = (method: string, params: Record<string, unknown>) => Promise<unknown>;

export interface AcpClientOptions {
  /** Executable and leading arguments (e.g. ["/home/u/.local/bin/agy"]); args are appended. */
  command: string[];
  args: string[];
  env: Record<string, string>;
  cwd?: string;
  log: Logger;
}

export class AcpClient extends EventEmitter<AcpEvents> {
  private child: ChildProcess | null = null;
  private nextId = 1;
  private readonly calls = new Map<Id, { method: string; resolve: (v: unknown) => void; reject: (e: Error) => void }>();
  private handler: AcpRequestHandler = async (method) => {
    throw new Error(`unsupported request ${method}`);
  };

  constructor(private readonly opts: AcpClientOptions) {
    super();
  }

  get running(): boolean {
    return this.child !== null;
  }

  onRequest(handler: AcpRequestHandler): void {
    this.handler = handler;
  }

  /** Spawns the agent; rejects when the executable cannot be started. */
  start(): Promise<void> {
    if (this.child) return Promise.resolve();
    const [cmd, ...pre] = this.opts.command;
    const child = spawn(cmd!, [...pre, ...this.opts.args], { env: this.opts.env, cwd: this.opts.cwd, stdio: ["pipe", "pipe", "pipe"] });
    this.child = child;
    // A write racing the child's exit fails with EPIPE; the exit event reports it.
    child.stdin?.on("error", (err) => this.opts.log.debug({ err: err.message }, "agy stdin"));
    createInterface({ input: child.stdout! }).on("line", (line) => this.onLine(line));
    createInterface({ input: child.stderr! }).on("line", (line) => this.opts.log.debug({ agy: line.slice(0, 2000) }, "agy stderr"));
    child.on("exit", (code, signal) => this.onExit(child, new Error(`agy acp exited (${code ?? signal})`)));
    return new Promise((resolve, reject) => {
      child.once("spawn", () => resolve());
      child.once("error", (err) => {
        this.onExit(child, err);
        reject(err);
      });
    });
  }

  stop(): void {
    const child = this.child;
    this.child = null;
    child?.stdin?.end();
    child?.kill("SIGTERM");
  }

  request<T = unknown>(method: string, params: unknown, timeoutMs?: number): Promise<T> {
    if (!this.child) return Promise.reject(new Error("agy acp is not running"));
    const id = this.nextId++;
    return new Promise<T>((resolve, reject) => {
      const timer = timeoutMs ? setTimeout(() => {
        this.calls.delete(id);
        reject(new Error(`${method}: timed out`));
      }, timeoutMs) : null;
      const done = <V>(f: (v: V) => void) => (v: V) => {
        if (timer) clearTimeout(timer);
        f(v);
      };
      this.calls.set(id, { method, resolve: done(resolve as (v: unknown) => void), reject: done(reject) });
      this.write({ jsonrpc: "2.0", id, method, params });
    });
  }

  notify(method: string, params: unknown): void {
    this.write({ jsonrpc: "2.0", method, params });
  }

  private write(msg: unknown): void {
    this.child?.stdin?.write(JSON.stringify(msg) + "\n");
  }

  private onLine(line: string): void {
    const msg = parse(line);
    if (!msg) return;
    if ("method" in msg && "id" in msg) return void this.serve(msg.id as Id, String(msg.method), (msg.params ?? {}) as Record<string, unknown>);
    if ("method" in msg) return void this.emit("notification", String(msg.method), (msg.params ?? {}) as Record<string, unknown>);
    if ("id" in msg) this.settle(msg as { id: Id; result?: unknown; error?: AcpError });
  }

  private settle(msg: { id: Id; result?: unknown; error?: AcpError }): void {
    const call = this.calls.get(msg.id);
    if (!call) return;
    this.calls.delete(msg.id);
    if (msg.error) call.reject(new AcpFailure(call.method, msg.error));
    else call.resolve(msg.result ?? null);
  }

  private async serve(id: Id, method: string, params: Record<string, unknown>): Promise<void> {
    try {
      this.write({ jsonrpc: "2.0", id, result: await this.handler(method, params) });
    } catch (err) {
      this.write({ jsonrpc: "2.0", id, error: { code: -32601, message: (err as Error).message } });
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
