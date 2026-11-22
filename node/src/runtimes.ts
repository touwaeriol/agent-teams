import { readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { query, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import type { ClaudeRuntime, CodexRuntime } from "./protocol.js";
import type { QueryFn } from "./sessions/claude/driver.js";
import { errorText } from "./usage.js";
import { AsyncQueue, withTimeout } from "./util/async.js";
import { CODEX_CLI_VERSION } from "./version.js";

/** One Claude login probe through the SDK; `error` set when the probe itself failed (auth is then a guess). */
export interface ClaudeProbe {
  cli?: string;
  auth: NonNullable<ClaudeRuntime["auth"]>;
  error?: string;
}

/** Where the Claude CLI keeps its OAuth login on Linux (CLAUDE_CONFIG_DIR, default ~/.claude). */
export function claudeCredentialsFile(claudeConfigDir: string): string {
  return join(claudeConfigDir, ".credentials.json");
}

/** Where Codex keeps its login ($CODEX_HOME/auth.json, default ~/.codex). */
export function codexAuthFile(codexHome: string): string {
  return join(codexHome, "auth.json");
}

/** The env names that make Claude use an API key / token instead of the subscription login. */
export function claudeKeyEnv(env: Record<string, string | undefined>): boolean {
  return Boolean(env.ANTHROPIC_API_KEY || env.ANTHROPIC_AUTH_TOKEN);
}

/**
 * Size and mtime of a file, or null when it is missing or empty. Only metadata: the content (a credential)
 * is never read.
 */
export function fileStamp(path: string): string | null {
  try {
    const s = statSync(path);
    return s.isFile() && s.size > 0 ? `${s.size}:${s.mtimeMs}` : null;
  } catch {
    return null;
  }
}

/**
 * Probes the Claude login (protocol.md §3: accountInfo, then close). Only the auth kind is reported, never the
 * account's email or organisation. A probe that fails or times out falls back to what the files say: a
 * credentials file means a subscription login, an API key env means api_key.
 */
export async function probeClaude(env: Record<string, string>, cwd: string, opts: { queryFn?: QueryFn; timeoutMs?: number; credentialsFile?: string } = {}): Promise<ClaudeProbe> {
  const prompt = new AsyncQueue<SDKUserMessage>();
  const q = (opts.queryFn ?? query)({ prompt, options: { cwd, env, settingSources: [], tools: [], permissionMode: "dontAsk", settings: { disableClaudeAiConnectors: true }, strictMcpConfig: true } });
  try {
    const init = await withTimeout(q.initializationResult(), opts.timeoutMs ?? 60_000, () => new Error("timeout"));
    const account = (init.account ?? {}) as { subscriptionType?: string; apiKeySource?: string; tokenSource?: string };
    const auth = account.subscriptionType ? "subscription" : claudeKeyEnv(env) ? "api_key" : "none";
    return { ...(init.claude_code_version ? { cli: init.claude_code_version } : {}), auth };
  } catch (err) {
    const guess = opts.credentialsFile && fileStamp(opts.credentialsFile) ? "subscription" : claudeKeyEnv(env) ? "api_key" : "none";
    return { auth: guess, error: `probe_failed: ${errorText(err)}` };
  } finally {
    prompt.end();
    q.close();
  }
}

/**
 * Which kind of login Codex's auth.json holds, from its structure only: an `auth_mode` naming an API key,
 * or a non-empty OPENAI_API_KEY without ChatGPT `tokens`, is an API key login; anything else (tokens,
 * unknown shape, unreadable or oversized file) counts as ChatGPT. The parsed object is dropped at once:
 * no value is kept, logged or reported. null: no (non-empty) file.
 */
export function codexFileAuth(file: string): "api_key" | "chatgpt" | null {
  if (!fileStamp(file)) return null;
  try {
    if (statSync(file).size > 1 << 20) return "chatgpt";
    const j = JSON.parse(readFileSync(file, "utf8")) as Record<string, unknown> | null;
    if (!j || typeof j !== "object") return "chatgpt";
    const mode = typeof j.auth_mode === "string" ? j.auth_mode.toLowerCase().replace(/[_-]/g, "") : "";
    if (mode === "apikey") return "api_key";
    if (mode.startsWith("chatgpt")) return "chatgpt";
    const hasKey = typeof j.OPENAI_API_KEY === "string" && j.OPENAI_API_KEY.length > 0;
    const hasTokens = j.tokens !== null && typeof j.tokens === "object";
    return hasKey && !hasTokens ? "api_key" : "chatgpt";
  } catch {
    return "chatgpt";
  }
}

/** Codex auth from OPENAI_API_KEY or the shape of $CODEX_HOME/auth.json (no network call, no process). */
export function probeCodex(codexHome: string, env: NodeJS.ProcessEnv, sandboxOk: boolean): CodexRuntime {
  const auth = env.OPENAI_API_KEY ? "api_key" : codexFileAuth(codexAuthFile(codexHome)) ?? "none";
  return { cli: CODEX_CLI_VERSION, auth, sandbox_ok: sandboxOk };
}

