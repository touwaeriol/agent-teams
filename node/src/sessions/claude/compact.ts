import type { SessionOpen } from "../../protocol.js";

/**
 * Claude Code's autocompact settings for a role's context limit (shared-members.md §5.1, §5.2).
 *
 * Measured against the bundled CLI (SDK 0.3.325, scripts/smoke-compact.ts):
 * - CLAUDE_CODE_AUTO_COMPACT_WINDOW is the window in tokens, clamped to 100K…1M and to the
 *   model's own window;
 * - autocompact fires at min(effective × pct / 100, effective − 13K) where
 *   effective = window − 20K (the reserve for the summary's output) and pct is
 *   CLAUDE_AUTOCOMPACT_PCT_OVERRIDE (a float).
 * So "limit × in_task_pct" is converted into a percentage of the effective window. Without a
 * limit the window is left to the CLI; window_tokens (the window the last session reported) then
 * turns in_task_pct into a target of that window rather than of window − 20K.
 */
export const CLAUDE_MIN_WINDOW = 100_000;
export const CLAUDE_MAX_WINDOW = 1_000_000;
export const OUTPUT_RESERVE = 20_000;

type Context = SessionOpen["context"];

export function claudeCompactEnv(context: Context): Record<string, string> {
  const limit = context?.limit_tokens;
  const pct = context?.in_task_pct;
  if (limit) {
    const window = Math.min(CLAUDE_MAX_WINDOW, Math.max(CLAUDE_MIN_WINDOW, limit));
    const env = { CLAUDE_CODE_AUTO_COMPACT_WINDOW: String(window) };
    return pct ? { ...env, CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: pctOf((limit * pct) / 100, window) } : env;
  }
  if (!pct) return {};
  const seen = context?.window_tokens;
  if (!seen || seen <= OUTPUT_RESERVE) return { CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: String(pct) };
  return { CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: pctOf((seen * pct) / 100, seen) };
}

/** The percentage of window − 20K that comes to target tokens (two decimals, rounded up, ≤ 100). */
function pctOf(target: number, window: number): string {
  return String(Math.min(100, Math.ceil((target / (window - OUTPUT_RESERVE)) * 10_000) / 100));
}
