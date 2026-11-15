import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";
import { summarize, truncate } from "../../util/redact.js";
import type { EventFields } from "../types.js";

const TEXT_MAX = 8 * 1024;
const ERROR_CODES: Record<string, EventFields["code"]> = {
  authentication_failed: "auth_failed",
  oauth_org_not_allowed: "auth_failed",
  account_on_hold: "auth_failed",
  verification_required: "auth_failed",
  billing_error: "billing",
};

type Msg = SDKMessage & Record<string, unknown>;

/** SDK message → session.event fields (protocol.md §4.8); turn bookkeeping lives in the driver. */
export function mapClaudeMessage(msg: SDKMessage): EventFields[] {
  const m = msg as Msg;
  switch (m.type) {
    case "assistant":
      return assistantEvents(m);
    case "result":
      return resultEvents(m);
    case "rate_limit_event":
      return rateLimitEvents(m);
    case "system":
      return systemEvents(m);
    default:
      return [];
  }
}

function assistantEvents(m: Msg): EventFields[] {
  const out: EventFields[] = [];
  const error = m.error as string | undefined;
  if (error) out.push(errorEvent(error));
  const content = ((m.message as { content?: unknown[] })?.content ?? []) as Array<{ type: string; text?: string }>;
  for (const block of content) {
    if (block.type === "thinking" || block.type === "redacted_thinking") out.push({ kind: "thinking" });
    if (block.type === "text" && block.text) out.push(textEvent(block.text, false));
  }
  return out;
}

function errorEvent(error: string): EventFields {
  if (error === "rate_limit" || error === "overloaded") return { kind: "rate_limited", detail: error };
  return { kind: "error", code: ERROR_CODES[error] ?? "protocol", message: error, fatal: false };
}

export function textEvent(text: string, final: boolean): EventFields {
  const t = truncate(text, TEXT_MAX);
  return { kind: "text", text: t.text, final, ...(t.truncated ? { truncated: true } : {}) };
}

function resultEvents(m: Msg): EventFields[] {
  const ended: EventFields = {
    kind: "turn_ended",
    result_subtype: String(m.subtype),
    is_error: Boolean(m.is_error),
    num_turns: Number(m.num_turns ?? 0),
    duration_ms: Number(m.duration_ms ?? 0),
    ...(typeof m.total_cost_usd === "number" ? { cost_usd: m.total_cost_usd } : {}),
    usage: (m.usage ?? {}) as Record<string, unknown>,
  };
  const final = m.subtype === "success" && typeof m.result === "string" && m.result ? [textEvent(m.result, true)] : [];
  return [...final, ended];
}

function rateLimitEvents(m: Msg): EventFields[] {
  const info = (m.rate_limit_info ?? {}) as { status?: string; resetsAt?: number; rateLimitType?: string };
  if (info.status !== "rejected") return [];
  return [{ kind: "rate_limited", detail: info.rateLimitType ?? "rate_limit", ...retryAt(info.resetsAt) }];
}

function systemEvents(m: Msg): EventFields[] {
  if (m.subtype === "status") return m.status === "compacting" ? [{ kind: "compacting", on: true }] : [];
  if (m.subtype === "compact_boundary") return [{ kind: "compacting", on: false }];
  if (m.subtype === "api_retry") return [{ kind: "rate_limited", detail: summarize(`${m.error ?? "api error"} (attempt ${m.attempt}/${m.max_retries})`), ...retryAt((Date.now() + Number(m.retry_delay_ms ?? 0)) / 1000) }];
  return [];
}

function retryAt(epochSeconds: number | undefined): { retry_at?: string } {
  if (!epochSeconds) return {};
  const ms = epochSeconds > 1e12 ? epochSeconds : epochSeconds * 1000;
  return { retry_at: new Date(ms).toISOString() };
}

const CATEGORIES: Record<string, string> = {
  Read: "read", Glob: "read", Grep: "read", LS: "read", NotebookRead: "read",
  Edit: "edit", Write: "edit", MultiEdit: "edit", NotebookEdit: "edit",
  Bash: "shell", BashOutput: "shell", KillShell: "shell",
  WebFetch: "web", WebSearch: "web",
};

/** category / subcategory / summary for tool_started. */
export function describeTool(tool: string, input: Record<string, unknown>): { category: string; subcategory?: string; summary: string } {
  const category = tool.startsWith("mcp__platform__") ? "platform" : CATEGORIES[tool] ?? "other";
  const summary = summarize(toolSummary(tool, input));
  const sub = category === "shell" ? shellSubcategory(String(input.command ?? "")) : undefined;
  return { category, ...(sub ? { subcategory: sub } : {}), summary };
}

function toolSummary(tool: string, input: Record<string, unknown>): string {
  for (const key of ["command", "file_path", "notebook_path", "pattern", "url", "query", "path"]) {
    if (typeof input[key] === "string") return String(input[key]);
  }
  return tool.replace(/^mcp__platform__/, "");
}

function shellSubcategory(cmd: string): string | undefined {
  if (/\b(pytest|vitest|jest|go test|cargo test|npm (run )?test|pnpm test|yarn test|make test)\b/.test(cmd)) return "test";
  if (/\bgit (commit|merge|rebase|cherry-pick)\b/.test(cmd)) return "commit";
  if (/\bgh (pr|run) (checks|view|watch)\b/.test(cmd)) return "ci";
  if (/\b(npm|pnpm|yarn|pip|uv|cargo|go) (install|add|i|get|sync)\b/.test(cmd)) return "install";
  return undefined;
}
