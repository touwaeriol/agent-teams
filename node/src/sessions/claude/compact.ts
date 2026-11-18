/**
 * Claude Code's autocompact settings for a role's context limit (shared-members.md §5.1, §5.2).
 *
 * Measured against the bundled CLI (SDK 0.3.325, scripts/smoke-compact.ts):
 * - CLAUDE_CODE_AUTO_COMPACT_WINDOW is the window in tokens, clamped to 100K…1M and to the
 *   model's own window;
 * - autocompact fires at min(effective × pct / 100, effective − 13K) where
 *   effective = window − 20K (the reserve for the summary's output) and pct is
 *   CLAUDE_AUTOCOMPACT_PCT_OVERRIDE (a float).
 * So "limit × in_task_pct" is converted into a percentage of the effective window.
 */
export const CLAUDE_MIN_WINDOW = 100_000;
export const CLAUDE_MAX_WINDOW = 1_000_000;
const OUTPUT_RESERVE = 20_000;

export function claudeCompactEnv(limitTokens?: number | null, inTaskPct?: number | null): Record<string, string> {
  if (!limitTokens && !inTaskPct) return {};
  const window = limitTokens ? Math.min(CLAUDE_MAX_WINDOW, Math.max(CLAUDE_MIN_WINDOW, limitTokens)) : null;
  const env: Record<string, string> = window ? { CLAUDE_CODE_AUTO_COMPACT_WINDOW: String(window) } : {};
  if (!inTaskPct) return env;
  if (!window || !limitTokens) return { ...env, CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: String(inTaskPct) };
  const target = (limitTokens * inTaskPct) / 100;
  const pct = Math.min(100, Math.ceil((target / (window - OUTPUT_RESERVE)) * 10_000) / 100);
  return { ...env, CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: String(pct) };
}
