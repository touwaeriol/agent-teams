// The Claude login's account (email, subscription type, plan tier) from `claude auth status`
// (docs/v2/usage-probe.md §账号). The CLI prints JSON there and reads only its local state; this
// process never sees a token. Only three fields are kept: no organisation, name or token.
import { execFile } from "node:child_process";
import { existsSync } from "node:fs";
import { createRequire } from "node:module";

export interface ClaudeAccount {
  email?: string;
  /** pro, max, team, enterprise. */
  subscription_type?: string;
  /** The CLI's tier: pro, max_5x, max_20x, max_other, other. */
  plan_tier?: string;
}

export const CLAUDE_ACCOUNT_TIMEOUT_MS = 5_000;
const SDK_PACKAGE = "@anthropic-ai/claude-agent-sdk";

/** glibc or musl, the way the SDK picks its native binary (no glibc version in the report means musl). */
function isMusl(): boolean {
  if (process.platform !== "linux") return false;
  try {
    const report = process.report?.getReport() as { header?: { glibcVersionRuntime?: string } } | undefined;
    return report?.header?.glibcVersionRuntime === undefined;
  } catch {
    return false;
  }
}

/**
 * The Claude CLI the SDK runs for sessions: its per-platform package next to the SDK (linux-x64, then
 * the -musl variant on a glibc host, the other way round on musl). null when none is installed.
 */
export function claudeExecutable(o: { platform?: string; arch?: string; musl?: boolean; resolve?: (id: string) => string; exists?: (p: string) => boolean } = {}): string | null {
  const platform = o.platform ?? process.platform;
  const arch = o.arch ?? process.arch;
  const musl = o.musl ?? isMusl();
  const resolve = o.resolve ?? createRequire(import.meta.url).resolve;
  const exists = o.exists ?? existsSync;
  const ext = platform === "win32" ? ".exe" : "";
  const names = platform === "linux"
    ? musl ? [`${SDK_PACKAGE}-linux-${arch}-musl`, `${SDK_PACKAGE}-linux-${arch}`] : [`${SDK_PACKAGE}-linux-${arch}`, `${SDK_PACKAGE}-linux-${arch}-musl`]
    : [`${SDK_PACKAGE}-${platform}-${arch}`];
  for (const name of names) {
    try {
      const p = resolve(`${name}/claude${ext}`);
      if (exists(p)) return p;
    } catch {
      // not installed for this platform
    }
  }
  return null;
}

const EMAIL = /^[^\s@]{1,200}@[^\s@]{1,200}$/;
const WORD = /^[a-z0-9_]{1,64}$/;

/**
 * The account of a claude.ai login in `claude auth status` output; null when it is not JSON (an older
 * CLI), not logged in, or an API key / third-party login (no subscription account to show).
 */
export function parseClaudeAuthStatus(stdout: string): ClaudeAccount | null {
  let j: unknown;
  try {
    j = JSON.parse(stdout);
  } catch {
    return null;
  }
  if (!j || typeof j !== "object") return null;
  const s = j as Record<string, unknown>;
  if (s.loggedIn !== true || s.authMethod !== "claude.ai") return null;
  const out: ClaudeAccount = {};
  if (typeof s.email === "string" && EMAIL.test(s.email.trim())) out.email = s.email.trim();
  if (typeof s.subscriptionType === "string" && WORD.test(s.subscriptionType)) out.subscription_type = s.subscriptionType;
  if (typeof s.planTier === "string" && WORD.test(s.planTier)) out.plan_tier = s.planTier;
  return Object.keys(out).length ? out : null;
}

/**
 * Runs `<claude> auth status` with the daemon's Claude env (HOME, CLAUDE_CONFIG_DIR) and a timeout.
 * Never throws: a missing CLI, a failure, a timeout or unexpected output all give null.
 */
export function readClaudeAccount(env: Record<string, string>, cwd: string, o: { bin?: string | null; timeoutMs?: number; signal?: AbortSignal } = {}): Promise<ClaudeAccount | null> {
  const bin = o.bin === undefined ? claudeExecutable() : o.bin;
  if (!bin || o.signal?.aborted) return Promise.resolve(null);
  return new Promise((resolve) => {
    try {
      execFile(bin, ["auth", "status"], { env, cwd, timeout: o.timeoutMs ?? CLAUDE_ACCOUNT_TIMEOUT_MS, maxBuffer: 64 * 1024, windowsHide: true, ...(o.signal ? { signal: o.signal } : {}) }, (err, stdout) => {
        resolve(err ? null : parseClaudeAuthStatus(String(stdout)));
      });
    } catch {
      resolve(null);
    }
  });
}
