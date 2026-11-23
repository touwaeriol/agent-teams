import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, describe, expect, it } from "vitest";
import { claudeExecutable, parseClaudeAuthStatus, readClaudeAccount } from "../src/claude-account.js";

// `claude auth status` of CLI 2.1.327 (values made up; orgName and the like are dropped).
const STATUS = {
  loggedIn: true, authMethod: "claude.ai", apiProvider: "firstParty", email: "me@example.com",
  orgId: "b27e6a33-0000-0000-0000-000000000000", orgName: "me@example.com's Organization",
  subscriptionType: "max", storedClaudeAiLogin: true, planTier: "max_20x",
};

describe("parseClaudeAuthStatus", () => {
  it("keeps only email, subscription type and plan tier", () => {
    expect(parseClaudeAuthStatus(JSON.stringify(STATUS))).toEqual({ email: "me@example.com", subscription_type: "max", plan_tier: "max_20x" });
  });

  it("gives null for an older CLI's text output, a logout and an API key login", () => {
    expect(parseClaudeAuthStatus("Logged in as me@example.com\n")).toBeNull();
    expect(parseClaudeAuthStatus("")).toBeNull();
    expect(parseClaudeAuthStatus("null")).toBeNull();
    expect(parseClaudeAuthStatus(JSON.stringify({ loggedIn: false }))).toBeNull();
    expect(parseClaudeAuthStatus(JSON.stringify({ ...STATUS, authMethod: "api_key" }))).toBeNull();
  });

  it("drops fields of an unexpected shape", () => {
    expect(parseClaudeAuthStatus(JSON.stringify({ ...STATUS, email: "not an email", planTier: 20, subscriptionType: "Max Plan!" }))).toBeNull();
    expect(parseClaudeAuthStatus(JSON.stringify({ ...STATUS, planTier: undefined, subscriptionType: "team" }))).toEqual({ email: "me@example.com", subscription_type: "team" });
  });
});

describe("claudeExecutable", () => {
  const found = (paths: string[]) => ({ resolve: (id: string) => { if (!paths.includes(id)) throw new Error("nope"); return `/nm/${id}`; }, exists: () => true });
  it("prefers the glibc package on glibc and the musl one on musl, like the SDK", () => {
    const both = ["@anthropic-ai/claude-agent-sdk-linux-x64/claude", "@anthropic-ai/claude-agent-sdk-linux-x64-musl/claude"];
    expect(claudeExecutable({ platform: "linux", arch: "x64", musl: false, ...found(both) })).toBe("/nm/@anthropic-ai/claude-agent-sdk-linux-x64/claude");
    expect(claudeExecutable({ platform: "linux", arch: "x64", musl: true, ...found(both) })).toBe("/nm/@anthropic-ai/claude-agent-sdk-linux-x64-musl/claude");
    expect(claudeExecutable({ platform: "linux", arch: "x64", musl: false, ...found([both[1]!]) })).toBe("/nm/@anthropic-ai/claude-agent-sdk-linux-x64-musl/claude");
    expect(claudeExecutable({ platform: "darwin", arch: "arm64", ...found(["@anthropic-ai/claude-agent-sdk-darwin-arm64/claude"]) })).toBe("/nm/@anthropic-ai/claude-agent-sdk-darwin-arm64/claude");
    expect(claudeExecutable({ platform: "linux", arch: "x64", ...found([]) })).toBeNull();
  });

  it("finds the binary installed with this package", () => {
    if (process.platform === "linux" && process.arch === "x64") expect(claudeExecutable()).toMatch(/claude-agent-sdk-linux-x64(-musl)?\/claude$/);
  });
});

describe("readClaudeAccount", () => {
  const dir = mkdtempSync(join(tmpdir(), "claude-account-"));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  const script = (name: string, body: string) => {
    const p = join(dir, name);
    writeFileSync(p, `#!/bin/sh\n${body}\n`);
    chmodSync(p, 0o755);
    return p;
  };

  it("runs `<bin> auth status` with the given env and parses it", async () => {
    const bin = script("ok", `[ "$1 $2" = "auth status" ] || exit 2\nprintf '{"loggedIn":true,"authMethod":"claude.ai","email":"%s","subscriptionType":"max","planTier":"max_5x"}' "$CLAUDE_CONFIG_DIR"`);
    expect(await readClaudeAccount({ PATH: "/usr/bin:/bin", CLAUDE_CONFIG_DIR: "x@y.z" }, dir, { bin })).toEqual({ email: "x@y.z", subscription_type: "max", plan_tier: "max_5x" });
  });

  it("gives null on failure, bad output, a timeout, an abort or no binary", async () => {
    expect(await readClaudeAccount({}, dir, { bin: script("fail", "echo '{}'; exit 1") })).toBeNull();
    expect(await readClaudeAccount({}, dir, { bin: script("text", "echo 'Logged in'") })).toBeNull();
    const t0 = Date.now();
    expect(await readClaudeAccount({}, dir, { bin: script("hang", "exec sleep 10"), timeoutMs: 200 })).toBeNull();
    expect(Date.now() - t0).toBeLessThan(5000);
    const abort = new AbortController();
    const p = readClaudeAccount({}, dir, { bin: script("hang2", "exec sleep 10"), signal: abort.signal });
    abort.abort();
    expect(await p).toBeNull();
    expect(await readClaudeAccount({}, dir, { bin: null })).toBeNull();
    expect(await readClaudeAccount({}, dir, { bin: join(dir, "missing") })).toBeNull();
  });
});
