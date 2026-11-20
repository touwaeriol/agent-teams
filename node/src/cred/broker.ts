import type { CredRequest, CredResponse } from "../protocol.js";
import { deferred, type Deferred } from "../util/async.js";
import { newId } from "../util/ids.js";

export type SendFn = (type: string, body: object) => void;
export type CredScope = { session_id: string; open_gen?: number } | { job_id: string };

export interface Credential {
  username: string;
  password: string;
  expiresAt: number;
}

export class CredDenied extends Error {}

interface Waiting {
  wait: Deferred<CredResponse>;
  timer: NodeJS.Timeout;
  body: CredRequest;
  tries: number;
}

const RETRY_MS = 10_000;
const MAX_TRIES = 3;
const REFRESH_MARGIN_MS = 5 * 60_000;

/**
 * Short-lived GitHub tokens on demand (protocol.md §8): cred.request is unreliable and
 * retried every 10 s with the same req_id; tokens live only in memory until 5 minutes
 * before they expire. Bodies are never logged.
 */
export class CredBroker {
  private readonly waiting = new Map<string, Waiting>();
  private readonly cache = new Map<string, { scope: CredScope; host: string; path: string; cred: Credential }>();

  constructor(private readonly send: SendFn, private readonly retryMs = RETRY_MS) {}

  async get(scope: CredScope, host: string, path: string, purpose: "read" | "push"): Promise<Credential> {
    const key = `${JSON.stringify(scope)}|${host}|${path}|${purpose}`;
    const hit = this.cache.get(key)?.cred;
    if (hit && hit.expiresAt - REFRESH_MARGIN_MS > Date.now()) return hit;
    const res = await this.request({ req_id: newId("cr_"), ...scope, host, path, purpose });
    if (!res.ok || !res.password) throw new CredDenied(res.error ?? "credential refused");
    const cred = { username: res.username ?? "x-access-token", password: res.password, expiresAt: res.expires_at ? Date.parse(res.expires_at) : Date.now() + 30 * 60_000 };
    if (purpose === "read") this.cache.set(key, { scope, host, path, cred });
    this.sweep();
    return cred;
  }

  /**
   * Drops cached tokens whose scope matches (and, when given, of that host and repository):
   * git erased a rejected token, or the session ended. Returns how many were dropped.
   */
  forget(match: (scope: CredScope) => boolean, host?: string, path?: string): number {
    let n = 0;
    for (const [key, e] of this.cache) {
      if (!match(e.scope) || (host !== undefined && e.host !== host) || (path !== undefined && e.path !== path)) continue;
      this.cache.delete(key);
      n++;
    }
    return n;
  }

  /** Drops expired entries (sessions that never ended cleanly). */
  private sweep(): void {
    const now = Date.now();
    for (const [key, e] of this.cache) if (e.cred.expiresAt <= now) this.cache.delete(key);
  }

  /** Handles cred.response. */
  resolve(res: CredResponse): void {
    const w = this.waiting.get(res.req_id);
    if (!w) return;
    clearTimeout(w.timer);
    this.waiting.delete(res.req_id);
    w.wait.resolve(res);
  }

  private request(body: CredRequest): Promise<CredResponse> {
    const w: Waiting = { wait: deferred<CredResponse>(), timer: setTimeout(() => this.retry(body.req_id), this.retryMs), body, tries: 1 };
    this.waiting.set(body.req_id, w);
    this.send("cred.request", body);
    return w.wait.promise;
  }

  private retry(reqId: string): void {
    const w = this.waiting.get(reqId);
    if (!w) return;
    if (w.tries >= MAX_TRIES) {
      this.waiting.delete(reqId);
      return w.wait.resolve({ req_id: reqId, ok: false, error: "console did not answer" });
    }
    w.tries++;
    w.timer = setTimeout(() => this.retry(reqId), this.retryMs);
    this.send("cred.request", w.body);
  }
}
