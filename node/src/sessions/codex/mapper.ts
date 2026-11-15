import { summarize } from "../../util/redact.js";
import { textEvent } from "../claude/mapper.js";
import type { EventFields } from "../types.js";

type Item = { type: string; id: string; [k: string]: unknown };

const TOOL_ITEMS: Record<string, { tool: (i: Item) => string; category: string; summary: (i: Item) => string }> = {
  commandExecution: { tool: () => "shell", category: "shell", summary: (i) => String(i.command ?? "") },
  fileChange: { tool: () => "apply_patch", category: "edit", summary: (i) => ((i.changes ?? []) as Array<{ path?: string }>).map((c) => c.path).join(", ") },
  mcpToolCall: { tool: (i) => `mcp__${i.server}__${i.tool}`, category: "platform", summary: (i) => String(i.tool ?? "") },
  dynamicToolCall: { tool: (i) => String(i.tool ?? "tool"), category: "other", summary: (i) => String(i.tool ?? "") },
  webSearch: { tool: () => "web_search", category: "web", summary: (i) => String(i.query ?? "") },
};

/** item/started → event fields. */
export function itemStarted(item: Item): EventFields[] {
  if (item.type === "reasoning") return [{ kind: "thinking" }];
  if (item.type === "contextCompaction") return [{ kind: "compacting", on: true }];
  const t = TOOL_ITEMS[item.type];
  if (!t) return [];
  const category = item.type === "mcpToolCall" && item.server !== "platform" ? "other" : t.category;
  return [{ kind: "tool_started", tool: t.tool(item), category, tool_use_id: item.id, summary: summarize(t.summary(item)) }];
}

/** item/completed → event fields. */
export function itemCompleted(item: Item, startedAt: number | undefined): EventFields[] {
  if (item.type === "agentMessage") return typeof item.text === "string" && item.text ? [textEvent(item.text, false)] : [];
  if (item.type === "contextCompaction") return [{ kind: "compacting", on: false }];
  if (!TOOL_ITEMS[item.type]) return [];
  const status = String(item.status ?? "completed");
  return [{ kind: "tool_ended", tool_use_id: item.id, ok: status === "completed", duration_ms: startedAt ? Date.now() - startedAt : 0 }];
}

/** turn/completed → turn_ended. */
export function turnCompleted(turn: { status: string; error?: { message?: string } | null; durationMs?: number | null }, startedAt: number | undefined, lastText: string | undefined): EventFields[] {
  const final = lastText ? [textEvent(lastText, true)] : [];
  return [...final, {
    kind: "turn_ended",
    result_subtype: turn.status === "completed" ? "success" : turn.status,
    is_error: turn.status === "failed",
    num_turns: 1,
    duration_ms: turn.durationMs ?? (startedAt ? Date.now() - startedAt : 0),
    usage: {},
  }];
}

/** error notification → rate_limited / error; transient retries (willRetry) other than auth are dropped. */
export function errorNotification(error: { message?: string; codexErrorInfo?: unknown; additionalDetails?: string | null }, willRetry: boolean): EventFields | null {
  const info = typeof error.codexErrorInfo === "string" ? error.codexErrorInfo : JSON.stringify(error.codexErrorInfo ?? "");
  const message = summarize(error.additionalDetails ?? error.message ?? info, 500);
  if (/usageLimitExceeded|rateLimitExceeded|serverOverloaded/.test(info)) return { kind: "rate_limited", detail: message };
  if (/unauthorized|"httpStatusCode":40[13]/.test(info)) return { kind: "error", code: "auth_failed", message, fatal: false };
  if (willRetry) return null;
  return { kind: "error", code: "protocol", message, fatal: false };
}
