// session.context (protocol.md §4.10 上下文细分): the SDK's getContextUsage() reply turned into the
// protocol's ContextDetail. Only the rows and lists the session page shows are kept (no gridRows,
// no message breakdown); every list is capped to the schema's maxItems.
import type { SDKControlGetContextUsageResponse } from "@anthropic-ai/claude-agent-sdk";
import type { ContextCategory, ContextDetail, ContextUsage } from "../../protocol.js";

export type ContextReply = SDKControlGetContextUsageResponse;

const KINDS = new Set<ContextCategory["kind"]>(["used", "free", "buffer", "deferred"]);

const tokens = (n: unknown): number => (typeof n === "number" && Number.isFinite(n) ? Math.max(0, Math.round(n)) : 0);
const text = (s: unknown, max: number): string => (typeof s === "string" ? s.slice(0, max) : "");

/** The two totals turn_ended carries. */
export function contextUsageOf(u: ContextReply): ContextUsage {
  return { used_tokens: tokens(u.totalTokens), max_tokens: tokens(u.maxTokens) };
}

/** The breakdown session.context carries; sampledAt = when getContextUsage was called (the console keeps the newest). */
export function contextDetailOf(u: ContextReply, detail: ContextDetail["detail"], sampledAt?: Date): ContextDetail {
  const out: ContextDetail = {
    detail,
    ...(sampledAt ? { sampled_at: sampledAt.toISOString() } : {}),
    total_tokens: tokens(u.totalTokens),
    max_tokens: tokens(u.maxTokens),
    categories: (u.categories ?? []).slice(0, 64).map((c) => {
      const kind: ContextCategory["kind"] = KINDS.has(c.kind) ? c.kind : c.isDeferred ? "deferred" : "used";
      return { ...(c.id ? { id: text(c.id, 64) } : {}), name: text(c.name, 200), tokens: tokens(c.tokens), kind };
    }),
  };
  if (u.model) out.model = text(u.model, 200);
  if (typeof u.rawMaxTokens === "number") out.raw_max_tokens = tokens(u.rawMaxTokens);
  if (typeof u.isAutoCompactEnabled === "boolean") out.auto_compact = u.isAutoCompactEnabled;
  if (typeof u.autoCompactThreshold === "number") out.auto_compact_threshold = tokens(u.autoCompactThreshold);
  if (u.mcpTools?.length) {
    out.mcp_tools = u.mcpTools.slice(0, 2000).map((t) => ({
      name: text(t.name, 300), server: text(t.serverName, 200), tokens: tokens(t.tokens), ...(typeof t.isLoaded === "boolean" ? { loaded: t.isLoaded } : {}),
    }));
  }
  if (u.mcpInstructions?.length) out.mcp_servers = u.mcpInstructions.slice(0, 500).map((s) => ({ server: text(s.serverName, 200), tokens: tokens(s.tokens) }));
  if (u.memoryFiles?.length) {
    out.memory_files = u.memoryFiles.slice(0, 500).map((f) => ({ path: text(f.path, 1000), ...(f.type ? { type: text(f.type, 100) } : {}), tokens: tokens(f.tokens) }));
  }
  const skills = u.skills?.skillFrontmatter ?? [];
  if (skills.length) out.skills = skills.slice(0, 1000).map((s) => ({ name: text(s.name, 200), ...(s.source ? { source: text(s.source, 200) } : {}), tokens: tokens(s.tokens) }));
  return out;
}
