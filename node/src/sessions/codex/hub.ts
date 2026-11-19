import type { Logger } from "../../util/log.js";
import type { RateLimitSnapshot } from "../../codex-protocol/v2/RateLimitSnapshot.js";
import { AppServer } from "./appserver.js";

export interface ThreadListener {
  onNotification(method: string, params: Record<string, unknown>): void;
  onServerRequest(method: string, params: Record<string, unknown>, requestId: string): Promise<unknown>;
  onAppServerExit(err: Error): void;
}

export interface CodexHubOptions {
  env: Record<string, string>;
  log: Logger;
  clientVersion: string;
  command?: string[];
}

/** One `codex app-server` per node (same CODEX_HOME); routes traffic to threads by threadId. */
export class CodexHub {
  private server: AppServer | null = null;
  private starting: Promise<AppServer> | null = null;
  private readonly threads = new Map<string, ThreadListener>();
  /** The last account/rateLimits/updated push (it carries no threadId), for node.usage. */
  lastRateLimits: { snapshot: RateLimitSnapshot; at: Date } | null = null;

  constructor(private readonly opts: CodexHubOptions) {}

  async ensure(): Promise<AppServer> {
    if (this.server?.running) return this.server;
    this.starting ??= this.spawn().finally(() => (this.starting = null));
    return this.starting;
  }

  /** A request outside any thread (account/read, account/rateLimits/read); starts the app-server if needed. */
  async request<T>(method: string, params: unknown): Promise<T> {
    return (await this.ensure()).request<T>(method, params);
  }

  register(threadId: string, listener: ThreadListener): void {
    this.threads.set(threadId, listener);
  }

  unregister(threadId: string): void {
    this.threads.delete(threadId);
  }

  stop(): void {
    this.server?.stop();
    this.server = null;
  }

  private async spawn(): Promise<AppServer> {
    const server = new AppServer({ env: this.opts.env, log: this.opts.log, clientVersion: this.opts.clientVersion, ...(this.opts.command ? { command: this.opts.command } : {}) });
    server.on("notification", (method, params) => {
      if (method === "account/rateLimits/updated" && params.rateLimits) this.noteRateLimits(params.rateLimits as RateLimitSnapshot);
      this.route(params)?.onNotification(method, params);
    });
    server.onRequest(async (method, params, id) => {
      const t = this.route(params);
      if (!t) throw new Error(`no session for ${method}`);
      return t.onServerRequest(method, params, id);
    });
    server.on("exit", (err) => this.onExit(server, err));
    await server.start();
    this.server = server;
    return server;
  }

  /** The push is sparse: null fields keep the value seen before. */
  noteRateLimits(update: RateLimitSnapshot): void {
    const merged = { ...(this.lastRateLimits?.snapshot ?? {}) } as Record<string, unknown>;
    for (const [k, v] of Object.entries(update)) if (v !== null && v !== undefined) merged[k] = v;
    this.lastRateLimits = { snapshot: merged as RateLimitSnapshot, at: new Date() };
  }

  private route(params: Record<string, unknown>): ThreadListener | undefined {
    return typeof params.threadId === "string" ? this.threads.get(params.threadId) : undefined;
  }

  private onExit(server: AppServer, err: Error): void {
    if (this.server !== server) return;
    this.server = null;
    this.opts.log.warn({ err: err.message }, "codex app-server exited");
    for (const t of [...this.threads.values()]) t.onAppServerExit(err);
    this.threads.clear();
  }
}
