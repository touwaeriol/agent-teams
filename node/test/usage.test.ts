import { afterEach, describe, expect, it, vi } from "vitest";
import type { GetAccountRateLimitsResponse } from "../src/codex-protocol/v2/GetAccountRateLimitsResponse.js";
import type { RateLimitSnapshot } from "../src/codex-protocol/v2/RateLimitSnapshot.js";
import { collectUsage, errorText, UsageReporter, type ClaudeUsage, type UsageSources } from "../src/usage.js";
import { silentLogger } from "../src/util/log.js";
import { startHarness, type Harness } from "./helpers/daemon.js";

const NOW = new Date("2026-11-19T12:00:00.000Z");
const now = () => NOW;

const claudeOk: ClaudeUsage = {
  subscription_type: "max",
  rate_limits_available: true,
  rate_limits: {
    five_hour: { utilization: 16, resets_at: "2026-11-19T23:09:59.592082+00:00" },
    seven_day: { utilization: 11, resets_at: "2026-11-25T14:59:59.592104+00:00" },
    seven_day_opus: null,
    seven_day_sonnet: { utilization: 130, resets_at: null },
    model_scoped: [{ display_name: "Fable", utilization: 14, resets_at: "2026-11-25T14:59:59Z" }],
  } as ClaudeUsage["rate_limits"],
};

function snapshot(primary: number, secondary: number): RateLimitSnapshot {
  return {
    limitId: "codex", limitName: null, normalModelSlug: null, credits: null, individualLimit: null, spendControlReached: null, rateLimitReachedType: null,
    planType: "pro",
    primary: { usedPercent: primary, windowDurationMins: 300, resetsAt: 1795608000 },
    secondary: { usedPercent: secondary, windowDurationMins: 10080, resetsAt: null },
  };
}

function rateLimits(s: RateLimitSnapshot): GetAccountRateLimitsResponse {
  return { ordinaryUsageAllowed: true, rateLimits: s, rateLimitsByLimitId: { codex: s }, rateLimitResetCredits: null, accountId: "acct-secret", rateLimitUpsell: null };
}

function sources(over: Partial<UsageSources> = {}): UsageSources & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    claudeAuth: () => "subscription",
    codexAuth: () => "chatgpt",
    claudeUsage: async () => (calls.push("claude"), claudeOk),
    codexAccount: async () => (calls.push("account"), { account: { type: "chatgpt", email: "a@b.c", planType: "pro" }, requiresOpenaiAuth: true } as never),
    codexRateLimits: async () => (calls.push("rateLimits"), rateLimits(snapshot(42, 7))),
    lastCodexEvent: () => null,
    ...over,
  };
}

describe("collectUsage", () => {
  it("maps Claude /usage windows and Codex rate limits", async () => {
    const u = await collectUsage(sources(), 1000, now);
    expect(u.runtimes.claude).toEqual({
      auth: "subscription", status: "ok", plan: "max", fetched_at: "2026-11-19T12:00:00.000Z",
      windows: [
        { key: "five_hour", used_percent: 16, window_minutes: 300, resets_at: "2026-11-19T23:09:59.592Z" },
        { key: "weekly", used_percent: 11, window_minutes: 10080, resets_at: "2026-11-25T14:59:59.592Z" },
        { key: "weekly_sonnet", used_percent: 100, window_minutes: 10080, resets_at: null },
        { key: "weekly_model", label: "Fable", used_percent: 14, window_minutes: 10080, resets_at: "2026-11-25T14:59:59.000Z" },
      ],
    });
    expect(u.runtimes.codex).toEqual({
      auth: "chatgpt", status: "ok", plan: "pro", fetched_at: "2026-11-19T12:00:00.000Z",
      windows: [
        { key: "primary", used_percent: 42, window_minutes: 300, resets_at: new Date(1795608000 * 1000).toISOString() },
        { key: "secondary", used_percent: 7, window_minutes: 10080, resets_at: null },
      ],
    });
    expect(JSON.stringify(u)).not.toMatch(/acct-secret|a@b\.c/);
  });

  it("does not call anything for API-key or missing logins", async () => {
    const src = sources({ claudeAuth: () => "api_key", codexAuth: () => "none" });
    const u = await collectUsage(src, 1000, now);
    expect(src.calls).toEqual([]);
    expect(u.runtimes.claude).toEqual({ auth: "api_key", status: "unsupported", windows: [], fetched_at: NOW.toISOString() });
    expect(u.runtimes.codex).toEqual({ auth: "none", status: "unsupported", windows: [], fetched_at: NOW.toISOString() });
  });

  it("reports a Codex API-key login found by account/read as unsupported without reading limits", async () => {
    const src = sources({ codexAccount: async () => ({ account: { type: "apiKey" }, requiresOpenaiAuth: true }) as never });
    const u = await collectUsage(src, 1000, now);
    expect(u.runtimes.codex).toMatchObject({ auth: "api_key", status: "unsupported", windows: [] });
    expect(src.calls).not.toContain("rateLimits");
  });

  it("turns failures and timeouts into status error with a credential-free reason", async () => {
    const src = sources({
      claudeUsage: async () => { throw new Error("401 Unauthorized: Bearer sk-ant-oat01-abcdefghijklmnop for x@example.com"); },
      codexAccount: () => new Promise(() => {}),
    });
    const u = await collectUsage(src, 50, now);
    expect(u.runtimes.claude).toEqual({ auth: "subscription", status: "error", windows: [], error: "401 Unauthorized: [redacted] for [redacted]", fetched_at: NOW.toISOString() });
    expect(u.runtimes.codex).toMatchObject({ auth: "chatgpt", status: "error", windows: [], error: "timeout" });
  });

  it("reports an expired / unfetchable Claude token as an error, keeping the plan", async () => {
    const u = await collectUsage(sources({ claudeUsage: async () => ({ subscription_type: "pro", rate_limits_available: true, rate_limits: null }) }), 1000, now);
    expect(u.runtimes.claude).toMatchObject({ status: "error", plan: "pro", error: "fetch_failed", windows: [] });
    const v = await collectUsage(sources({ claudeUsage: async () => ({ subscription_type: null, rate_limits_available: false, rate_limits: null }) }), 1000, now);
    expect(v.runtimes.claude).toMatchObject({ status: "error", error: "rate_limits_unavailable" });
  });

  it("reports a Codex logout as not_logged_in", async () => {
    const u = await collectUsage(sources({ codexAccount: async () => ({ account: null, requiresOpenaiAuth: true }) as never }), 1000, now);
    expect(u.runtimes.codex).toMatchObject({ status: "error", error: "not_logged_in" });
  });

  it("falls back to the last rate-limit event, dated by the event", async () => {
    const at = new Date("2026-11-19T11:30:00Z");
    const src = sources({
      codexRateLimits: async () => { throw new Error("account/rateLimits/read: method not found"); },
      lastCodexEvent: () => ({ snapshot: snapshot(55, 20), at }),
    });
    const u = await collectUsage(src, 1000, now);
    expect(u.runtimes.codex).toMatchObject({ status: "ok", fetched_at: at.toISOString(), windows: [{ key: "primary", used_percent: 55 }, { key: "secondary", used_percent: 20 }] });
  });

  it("errorText caps the reason at 200 characters", () => {
    expect(errorText(new Error("x".repeat(500)))).toHaveLength(200);
    expect(errorText("token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload")).toBe("token [redacted]");
  });
});

describe("UsageReporter", () => {
  afterEach(() => vi.useRealTimers());

  it("reports on kick and then on the interval, never two reads at once", async () => {
    vi.useFakeTimers();
    const sent: unknown[] = [];
    const src = sources();
    const reads = () => src.calls.filter((c) => c === "claude").length;
    const r = new UsageReporter({ sources: src, send: (b) => sent.push(b), log: silentLogger, intervalMs: 60_000, timeoutMs: 1000 });
    r.kick();
    r.kick();
    await vi.advanceTimersByTimeAsync(0);
    expect(reads()).toBe(1);
    expect(sent).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(reads()).toBe(2);
    expect(sent).toHaveLength(3);
    r.stop();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(reads()).toBe(2);
  });

  it("answers node.usage_refresh from a successful read younger than a minute, and reads again after", async () => {
    let t = 1_000_000;
    const src = sources();
    const r = new UsageReporter({ sources: src, send: () => {}, log: silentLogger, intervalMs: 0, timeoutMs: 1000, now: () => t });
    const first = await r.refresh();
    expect(src.calls.filter((c) => c === "claude")).toHaveLength(1);
    t += 59_000;
    const cached = await r.refresh();
    expect(src.calls.filter((c) => c === "claude")).toHaveLength(1);
    expect(cached).toEqual(first);
    expect(cached.runtimes.claude!.fetched_at).toBe(new Date(1_000_000).toISOString());
    t += 1_000;
    const fresh = await r.refresh(["claude"]);
    expect(src.calls.filter((c) => c === "claude")).toHaveLength(2);
    expect(src.calls.filter((c) => c === "rateLimits")).toHaveLength(1);
    expect(fresh.runtimes.claude!.fetched_at).toBe(new Date(1_060_000).toISOString());
    expect(fresh.runtimes.codex).toEqual(first.runtimes.codex);
  });

  it("does not cache failed reads", async () => {
    let fail = true;
    const calls: string[] = [];
    const src = sources({
      codexAuth: () => "none",
      claudeUsage: async () => { calls.push("claude"); if (fail) throw new Error("boom"); return claudeOk; },
    });
    const r = new UsageReporter({ sources: src, send: () => {}, log: silentLogger, intervalMs: 0, timeoutMs: 1000 });
    expect((await r.refresh()).runtimes.claude).toMatchObject({ status: "error" });
    fail = false;
    expect((await r.refresh()).runtimes.claude).toMatchObject({ status: "ok" });
    expect(calls).toHaveLength(2);
  });

  it("lets a refresh during a read share that read", async () => {
    let release!: () => void;
    const gate = new Promise<void>((res) => (release = res));
    const calls: string[] = [];
    const src = sources({ codexAuth: () => "none", claudeUsage: async () => { calls.push("claude"); await gate; return claudeOk; } });
    const sent: unknown[] = [];
    const r = new UsageReporter({ sources: src, send: (b) => sent.push(b), log: silentLogger, intervalMs: 0, timeoutMs: 1000 });
    const periodic = r.report();
    const a = r.refresh();
    const b = r.refresh(["claude"]);
    release();
    const [ra, rb] = await Promise.all([a, b, periodic]);
    expect(calls).toHaveLength(1);
    expect(ra).toEqual(rb);
    expect(sent).toEqual([ra]);
  });

  it("is off with interval 0", async () => {
    const sent: unknown[] = [];
    const r = new UsageReporter({ sources: sources(), send: (b) => sent.push(b), log: silentLogger, intervalMs: 0 });
    r.kick();
    await new Promise((res) => setTimeout(res, 20));
    expect(sent).toEqual([]);
  });
});

describe("daemon node.usage", () => {
  let h: Harness;
  afterEach(async () => {
    const invalid = [...h.console.invalid];
    await h.close();
    expect(invalid).toEqual([]);
  });

  it("advertises the usage feature and sends a schema-valid node.usage after welcome", async () => {
    h = await startHarness({
      usageSources: {
        claudeAuth: () => "subscription",
        claudeUsage: async () => claudeOk,
        codexAuth: () => "chatgpt",
        codexAccount: async () => ({ account: { type: "chatgpt" }, requiresOpenaiAuth: true }) as never,
        codexRateLimits: async () => rateLimits(snapshot(42, 7)),
      },
    });
    expect(h.console.ofType("hello")[0]!.body.features).toContain("usage");
    const f = await h.console.waitType("node.usage");
    expect(f.seq).toBeUndefined();
    expect(f.body.runtimes.claude).toMatchObject({ status: "ok", windows: [{ key: "five_hour", used_percent: 16 }, { key: "weekly" }, { key: "weekly_sonnet" }, { key: "weekly_model", label: "Fable" }] });
    expect(f.body.runtimes.codex).toMatchObject({ status: "ok", windows: [{ key: "primary" }, { key: "secondary" }] });
  });

  it("answers node.usage_refresh with both runtimes, from the cache within a minute", async () => {
    let claudeReads = 0;
    h = await startHarness({
      usageSources: {
        claudeAuth: () => "subscription",
        claudeUsage: async () => (claudeReads++, claudeOk),
        codexAuth: () => "none",
      },
    });
    const first = await h.console.waitType("node.usage");
    expect(claudeReads).toBe(1);
    h.console.unreliable("node.usage_refresh", { runtimes: ["claude"] });
    await h.console.waitFor((f) => f.type === "node.usage" && f !== first);
    const answers = h.console.ofType("node.usage");
    expect(answers).toHaveLength(2);
    expect(claudeReads).toBe(1);
    expect(answers[1]!.seq).toBeUndefined();
    expect(answers[1]!.body).toEqual(first.body);
    expect(h.console.ofType("error")).toEqual([]);
  });

  it("reports unsupported runtimes without probing when nothing is logged in", async () => {
    h = await startHarness();
    const f = await h.console.waitType("node.usage");
    expect(f.body.runtimes).toMatchObject({ claude: { auth: "none", status: "unsupported" }, codex: { auth: "none", status: "unsupported" } });
  });
});

describe("CodexHub rate-limit pushes", () => {
  it("merges sparse account/rateLimits/updated pushes", async () => {
    const { CodexHub } = await import("../src/sessions/codex/hub.js");
    const hub = new CodexHub({ env: {}, log: silentLogger, clientVersion: "t" });
    hub.noteRateLimits(snapshot(10, 5));
    hub.noteRateLimits({ ...snapshot(30, 0), secondary: null, planType: null });
    expect(hub.lastRateLimits!.snapshot).toMatchObject({ primary: { usedPercent: 30 }, secondary: { usedPercent: 5 }, planType: "pro" });
  });
});
