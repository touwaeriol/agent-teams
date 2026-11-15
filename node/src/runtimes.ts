import { existsSync } from "node:fs";
import { join } from "node:path";
import { query, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import type { ClaudeRuntime, CodexRuntime } from "./protocol.js";
import { AsyncQueue, withTimeout } from "./util/async.js";
import { CLAUDE_SDK_VERSION, CODEX_CLI_VERSION } from "./version.js";

/**
 * Probes the Claude login once at startup (protocol.md §3: accountInfo, then close).
 * Only the auth kind is reported, never the account's email or organisation.
 */
export async function probeClaude(env: Record<string, string>, cwd: string): Promise<ClaudeRuntime> {
  const prompt = new AsyncQueue<SDKUserMessage>();
  const q = query({ prompt, options: { cwd, env, settingSources: [], tools: [], permissionMode: "dontAsk", settings: { disableClaudeAiConnectors: true }, strictMcpConfig: true } });
  try {
    const init = await withTimeout(q.initializationResult(), 60_000, () => new Error("claude probe timed out"));
    const account = init.account as { subscriptionType?: string; apiKeySource?: string; tokenSource?: string };
    const auth = account.subscriptionType ? "subscription" : env.ANTHROPIC_API_KEY || env.ANTHROPIC_AUTH_TOKEN ? "api_key" : "none";
    return { sdk: CLAUDE_SDK_VERSION, ...(init.claude_code_version ? { cli: init.claude_code_version } : {}), auth };
  } catch {
    return { sdk: CLAUDE_SDK_VERSION, auth: "none" };
  } finally {
    prompt.end();
    q.close();
  }
}

/** Codex auth from $CODEX_HOME/auth.json or OPENAI_API_KEY (no network call). */
export function probeCodex(codexHome: string, env: NodeJS.ProcessEnv, sandboxOk: boolean): CodexRuntime {
  const file = join(codexHome, "auth.json");
  const auth = env.OPENAI_API_KEY ? "api_key" : existsSync(file) ? "chatgpt" : "none";
  return { cli: CODEX_CLI_VERSION, auth, sandbox_ok: sandboxOk };
}
