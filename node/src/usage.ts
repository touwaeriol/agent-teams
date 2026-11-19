// Subscription quota of the node's Claude Code / Codex logins (protocol.md §9.1, docs/v2/usage-probe.md).
// Credentials never leave the runtimes: Claude's numbers come from the CLI's own get_usage control
// request, Codex's from its app-server; neither the token nor the account's identity is reported.
import { query, type SDKControlGetUsageResponse, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import type { GetAccountRateLimitsResponse } from "./codex-protocol/v2/GetAccountRateLimitsResponse.js";
import type { GetAccountResponse } from "./codex-protocol/v2/GetAccountResponse.js";
import type { RateLimitSnapshot } from "./codex-protocol/v2/RateLimitSnapshot.js";
import type { RateLimitWindow } from "./codex-protocol/v2/RateLimitWindow.js";
import type { ClaudeRuntime, CodexRuntime, NodeUsage as GenNodeUsage, NodeUsageRefresh, RuntimeUsage as GenRuntimeUsage, UsageWindow } from "./protocol.js";
import { AsyncQueue, withTimeout } from "./util/async.js";
import type { Logger } from "./util/log.js";

export const USAGE_INTERVAL_MS = 5 * 60_000;
/** Budget per runtime for the whole read (the console waits 15 s for the answer to node.usage_refresh). */
export const USAGE_TIMEOUT_MS = 12_000;
/** node.usage_refresh answers from a successful read younger than this instead of asking again. */
export const USAGE_CACHE_MS = 60_000;
export const USAGE_RUNTIMES = ["claude", "codex"] as const;
export type UsageRuntime = (typeof USAGE_RUNTIMES)[number];

/** The generated types (protocol/gen), narrowed: this node always sends windows and fetched_at, and both runtimes. */
export type RuntimeUsage = GenRuntimeUsage & { windows: UsageWindow[]; fetched_at: string };
export type NodeUsage = GenNodeUsage & { runtimes: Partial<Record<UsageRuntime, RuntimeUsage>> };
const MAX_WINDOWS = 8;

export type ClaudeUsage = Pick<SDKControlGetUsageResponse, "subscription_type" | "rate_limits_available" | "rate_limits">;
export interface CodexRateLimitEvent { snapshot: RateLimitSnapshot; at: Date }

export interface UsageSources {
  claudeAuth(): ClaudeRuntime["auth"];
  codexAuth(): CodexRuntime["auth"];
  /** The CLI's /usage data (get_usage control request). */
  claudeUsage(): Promise<ClaudeUsage>;
  /** codex app-server account/read: refines a chatgpt guess from auth.json (an API key login also writes it). */
  codexAccount(): Promise<GetAccountResponse>;
  /** codex app-server account/rateLimits/read. */
  codexRateLimits(): Promise<GetAccountRateLimitsResponse>;
  /** The last account/rateLimits/updated notification, the fallback when the read fails. */
  lastCodexEvent(): CodexRateLimitEvent | null;
}

/** Reads the runtimes in parallel; each read has its own timeout and failures become status "error". */
export async function collectUsage(src: UsageSources, timeoutMs = USAGE_TIMEOUT_MS, now: () => Date = () => new Date(), which: readonly UsageRuntime[] = USAGE_RUNTIMES): Promise<NodeUsage> {
  const read = { claude: () => claudeUsage(src, timeoutMs, now), codex: () => codexUsage(src, Date.now() + timeoutMs, now) };
  const results = await Promise.all(which.map(async (rt) => [rt, await read[rt]()] as const));
  return { runtimes: Object.fromEntries(results) };
}

async function claudeUsage(src: UsageSources, timeoutMs: number, now: () => Date): Promise<RuntimeUsage> {
  const auth = src.claudeAuth() ?? "none";
  if (auth !== "subscription") return unsupported(auth, now);
  try {
    const u = await withTimeout(src.claudeUsage(), timeoutMs, () => new Error("timeout"));
    const plan = u.subscription_type ?? undefined;
    if (!u.rate_limits_available) return failed(auth, "rate_limits_unavailable", now, plan);
    if (!u.rate_limits) return failed(auth, "fetch_failed", now, plan);
    return { auth, status: "ok", ...(plan ? { plan: clip(plan, 40) } : {}), windows: claudeWindows(u.rate_limits), fetched_at: now().toISOString() };
  } catch (err) {
    return failed(auth, errorText(err), now);
  }
}

type ClaudeLimits = NonNullable<ClaudeUsage["rate_limits"]>;
type ClaudeWindow = { utilization: number | null; resets_at: string | null } | null | undefined;

/** five_hour → five_hour, seven_day → weekly, seven_day_opus/sonnet → weekly_opus/sonnet, model_scoped[] → weekly_model + label. */
export function claudeWindows(rl: ClaudeLimits): UsageWindow[] {
  const out: UsageWindow[] = [];
  const add = (key: string, w: ClaudeWindow, minutes: number, label?: string) => {
    const pct = w ? percent(w.utilization) : null;
    if (pct === null) return;
    out.push({ key, ...(label ? { label: clip(label, 40) } : {}), used_percent: pct, window_minutes: minutes, resets_at: isoOrNull(w!.resets_at) });
  };
  add("five_hour", rl.five_hour, 300);
  add("weekly", rl.seven_day, 10080);
  add("weekly_opus", rl.seven_day_opus, 10080);
  add("weekly_sonnet", rl.seven_day_sonnet, 10080);
  for (const m of rl.model_scoped ?? []) add("weekly_model", m, 10080, m.display_name);
  return out.slice(0, MAX_WINDOWS);
}

/** Codex takes two calls; both share one deadline so the runtime never exceeds its budget. */
async function codexUsage(src: UsageSources, deadline: number, now: () => Date): Promise<RuntimeUsage> {
  const left = () => Math.max(0, deadline - Date.now());
  let auth = src.codexAuth() ?? "none";
  if (auth !== "chatgpt") return unsupported(auth, now);
  try {
    const account = await withTimeout(src.codexAccount(), left(), () => new Error("timeout"));
    if (account.account?.type === "apiKey") return unsupported((auth = "api_key"), now);
    if (account.account === null && account.requiresOpenaiAuth) return failed(auth, "not_logged_in", now);
    const r = await withTimeout(src.codexRateLimits(), left(), () => new Error("timeout"));
    return codexResult(r.rateLimitsByLimitId?.codex ?? r.rateLimits, now());
  } catch (err) {
    const ev = src.lastCodexEvent();
    if (ev) return codexResult(ev.snapshot, ev.at);
    return failed(auth, errorText(err), now);
  }
}

function codexResult(s: RateLimitSnapshot, at: Date): RuntimeUsage {
  const windows: UsageWindow[] = [];
  const add = (key: string, w: RateLimitWindow | null) => {
    const pct = w ? percent(w.usedPercent) : null;
    if (pct === null) return;
    windows.push({
      key, used_percent: pct,
      ...(Number.isInteger(w!.windowDurationMins) && w!.windowDurationMins! >= 0 ? { window_minutes: w!.windowDurationMins! } : {}),
      resets_at: typeof w!.resetsAt === "number" ? new Date(w!.resetsAt * 1000).toISOString() : null,
    });
  };
  add("primary", s.primary);
  add("secondary", s.secondary);
  const plan = s.planType && s.planType !== "unknown" ? s.planType : undefined;
  return { auth: "chatgpt", status: "ok", ...(plan ? { plan } : {}), windows, fetched_at: at.toISOString() };
}

function unsupported(auth: RuntimeUsage["auth"], now: () => Date): RuntimeUsage {
  return { auth, status: "unsupported", windows: [], fetched_at: now().toISOString() };
}

function failed(auth: RuntimeUsage["auth"], error: string, now: () => Date, plan?: string): RuntimeUsage {
  return { auth, status: "error", ...(plan ? { plan: clip(plan, 40) } : {}), windows: [], error, fetched_at: now().toISOString() };
}

function percent(v: unknown): number | null {
  if (typeof v !== "number" || !Number.isFinite(v)) return null;
  return Math.min(100, Math.max(0, v));
}

function isoOrNull(v: string | null | undefined): string | null {
  if (!v) return null;
  const t = Date.parse(v);
  return Number.isNaN(t) ? null : new Date(t).toISOString();
}

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) : s;
}

/** A short, credential-free reason: token-looking strings are masked and the text is capped. */
export function errorText(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const clean = msg
    .replace(/\b(sk-[A-Za-z0-9_-]{8,}|eyJ[A-Za-z0-9_.-]{20,}|Bearer\s+\S+)/g, "[redacted]")
    .replace(/[\w.+-]+@[\w-]+\.[\w.-]+/g, "[redacted]")
    .replace(/\s+/g, " ")
    .trim();
  return clip(clean || "error", 200);
}

/**
 * Asks the Claude CLI for its /usage data through the SDK's get_usage control request. The CLI reads
 * (and, when it must, refreshes) its own OAuth token from CLAUDE_CONFIG_DIR; this process never sees it.
 */
export async function readClaudeUsage(env: Record<string, string>, cwd: string): Promise<ClaudeUsage> {
  const prompt = new AsyncQueue<SDKUserMessage>();
  const q = query({ prompt, options: { cwd, env, settingSources: [], tools: [], permissionMode: "dontAsk", settings: { disableClaudeAiConnectors: true }, strictMcpConfig: true } });
  try {
    await q.initializationResult();
    const read = (q as unknown as { usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET?: (o: { skipBehaviors: boolean }) => Promise<SDKControlGetUsageResponse> }).usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET;
    // The method is experimental: absent or rejected means this SDK / CLI cannot answer (usage-probe.md).
    if (typeof read !== "function") throw new Error("unsupported_sdk");
    const u = await read.call(q, { skipBehaviors: true }).catch(() => {
      throw new Error("unsupported_sdk");
    });
    return { subscription_type: u.subscription_type, rate_limits_available: u.rate_limits_available, rate_limits: u.rate_limits };
  } finally {
    prompt.end();
    q.close();
  }
}

interface Cached { usage: RuntimeUsage; at: number }

/**
 * Sends node.usage after each hello (welcome) and then every intervalMs, and answers node.usage_refresh.
 * One read runs at a time (a refresh during a read waits for it); successful reads are cached for
 * cacheMs so hovering over a node card does not hit Anthropic / OpenAI each time; failures are not.
 */
export class UsageReporter {
  private timer: NodeJS.Timeout | null = null;
  private running: Promise<void> | null = null;
  private readonly last = new Map<UsageRuntime, Cached>();

  constructor(private readonly o: {
    sources: UsageSources; send: (body: NodeUsage) => void; log: Logger;
    intervalMs?: number; timeoutMs?: number; cacheMs?: number; now?: () => number;
  }) {}

  /** Called on every welcome: report now and arm the interval. intervalMs 0 disables the periodic report. */
  kick(): void {
    const every = this.o.intervalMs ?? USAGE_INTERVAL_MS;
    if (every <= 0) return;
    void this.report();
    if (!this.timer) {
      this.timer = setInterval(() => void this.report(), every);
      this.timer.unref?.();
    }
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  /** The periodic report: always a fresh read of every runtime (or the one already running). */
  async report(): Promise<void> {
    try {
      await (this.running ?? this.read(USAGE_RUNTIMES));
      this.o.send(this.compose());
    } catch (err) {
      this.o.log.warn({ err: (err as Error).message }, "usage report failed");
    }
  }

  /**
   * node.usage_refresh: the body to answer with. Requested runtimes with a successful read younger than
   * cacheMs come from the cache (their fetched_at unchanged); the rest are read now. A runtime never read
   * yet is read too, so the answer always covers both.
   */
  async refresh(requested?: NodeUsageRefresh["runtimes"]): Promise<NodeUsage> {
    if (this.running) {
      await this.running;
      return this.compose();
    }
    const want = USAGE_RUNTIMES.filter((rt) => !requested?.length || requested.includes(rt));
    const ttl = this.o.cacheMs ?? USAGE_CACHE_MS;
    const now = this.now();
    const stale = USAGE_RUNTIMES.filter((rt) => {
      const c = this.last.get(rt);
      if (!c) return true;
      if (!want.includes(rt)) return false;
      return c.usage.status !== "ok" || now - c.at >= ttl;
    });
    if (stale.length) await this.read(stale);
    return this.compose();
  }

  private read(which: readonly UsageRuntime[]): Promise<void> {
    this.running ??= collectUsage(this.o.sources, this.o.timeoutMs ?? USAGE_TIMEOUT_MS, () => new Date(this.now()), which)
      .then((body) => {
        const at = this.now();
        for (const [rt, u] of Object.entries(body.runtimes) as [UsageRuntime, RuntimeUsage][]) {
          if (u.status === "error") this.o.log.warn({ runtime: rt, err: u.error }, "usage read failed");
          this.last.set(rt, { usage: u, at });
        }
      })
      .finally(() => (this.running = null));
    return this.running;
  }

  private compose(): NodeUsage {
    const runtimes: NodeUsage["runtimes"] = {};
    for (const rt of USAGE_RUNTIMES) {
      const c = this.last.get(rt);
      if (c) runtimes[rt] = c.usage;
    }
    return { runtimes };
  }

  private now(): number {
    return (this.o.now ?? Date.now)();
  }
}
