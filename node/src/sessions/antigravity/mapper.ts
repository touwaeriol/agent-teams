// ACP session/update → platform session events (protocol.md §4.8). The ACP update kinds are the
// open standard's (agent_message_chunk, agent_thought_chunk, tool_call, tool_call_update, plan…);
// agy 1.5.14 advertises them, but its exact tool titles and kinds are not verified yet.
import { summarize } from "../../util/redact.js";
import type { EventFields } from "../types.js";

/** One ACP tool call as session/update reports it (only the fields used here). */
export interface AcpToolCall {
  toolCallId: string;
  title?: string;
  kind?: string;
  status?: string;
  rawInput?: unknown;
  locations?: Array<{ path?: string }>;
}

/** ACP tool kinds that change the workspace or run something. */
export const WRITE_KINDS = new Set(["edit", "delete", "move", "execute"]);
/** ACP tool kinds a read-only session may run without asking (execute is checked against bash_policy). */
export const READ_ONLY_KINDS = new Set(["read", "search", "think"]);

const CATEGORY: Record<string, string> = { read: "read", search: "read", edit: "edit", delete: "edit", move: "edit", execute: "shell", fetch: "web", think: "other", switch_mode: "other", other: "other" };
/** The Claude tool the policy checks (sessions/policy.ts) stand in for each ACP kind. */
const POLICY_TOOL: Record<string, string> = { read: "Read", search: "Grep", edit: "Edit", delete: "Write", move: "Write", execute: "Bash", fetch: "WebFetch", think: "Think" };

/** The policy tool name of an ACP kind (unknown kinds keep the kind). */
export function policyTool(kind: string | undefined): string {
  return POLICY_TOOL[kind ?? "other"] ?? `agy_${kind ?? "other"}`;
}

/**
 * The platform tool a call names, when its whole title is "platform/<tool>", "mcp_platform_<tool>"
 * or "mcp(platform/<tool>)" (guessed from agy's permission rule syntax; to verify once logged in)
 * and, given the session's platform tool names, <tool> is one of them. A title that merely
 * mentions "platform" ("Edit platform.go") is not one: these calls are allowed without asking.
 */
export function platformToolOf(t: Pick<AcpToolCall, "title" | "kind">, names?: readonly string[]): string | null {
  // Only calls of no particular kind can be MCP tools: an execute call titled
  // "platform/send_message" is a command (maybe a file of that name), never a platform tool.
  if (t.kind && !PLATFORM_KINDS.has(t.kind)) return null;
  const m = /^\s*(?:mcp[_:(/]\s*)?platform[/_:]([a-z][a-z0-9_]*)\s*\)?\s*$/i.exec(String(t.title ?? ""));
  if (!m) return null;
  return !names || names.includes(m[1]!) ? m[1]! : null;
}

/** ACP kinds an MCP tool call may carry (no kind at all is accepted too). */
const PLATFORM_KINDS = new Set(["other", "think"]);

export function isPlatformTool(t: Pick<AcpToolCall, "title" | "kind">, names?: readonly string[]): boolean {
  return platformToolOf(t, names) !== null;
}

/** The shell command of an execute tool call (rawInput.command as a string or argv). */
export function commandOf(t: AcpToolCall): string | null {
  const raw = (t.rawInput ?? {}) as Record<string, unknown>;
  const c = raw.command ?? raw.cmd ?? raw.CommandLine;
  if (typeof c === "string") return c;
  if (Array.isArray(c) && c.every((x) => typeof x === "string")) return c.join(" ");
  return null;
}

/** The file a file tool call touches: the first location, else a path field of rawInput. */
export function pathOf(t: AcpToolCall): string | null {
  const loc = t.locations?.find((l) => typeof l.path === "string")?.path;
  if (loc) return loc;
  const raw = (t.rawInput ?? {}) as Record<string, unknown>;
  for (const k of ["file_path", "path", "filePath", "TargetFile", "AbsolutePath"]) if (typeof raw[k] === "string") return raw[k] as string;
  return null;
}

export function toolStarted(t: AcpToolCall, names?: readonly string[]): EventFields {
  const platform = isPlatformTool(t, names);
  const summary = t.kind === "execute" ? (commandOf(t) ?? t.title ?? "") : (t.title ?? pathOf(t) ?? "");
  return { kind: "tool_started", tool: toolName(t, names), category: platform ? "platform" : (CATEGORY[t.kind ?? "other"] ?? "other"), tool_use_id: t.toolCallId, summary: summarize(summary) };
}

export function toolEnded(t: AcpToolCall, startedAt: number | undefined): EventFields {
  return { kind: "tool_ended", tool_use_id: t.toolCallId, ok: t.status === "completed", duration_ms: startedAt ? Date.now() - startedAt : 0 };
}

/** A readable tool name: the platform tool as mcp__platform__<name>, else the ACP kind. */
export function toolName(t: Pick<AcpToolCall, "title" | "kind">, names?: readonly string[]): string {
  const p = platformToolOf(t, names);
  return p ? `mcp__platform__${p}` : (t.kind ?? "other");
}

/** ACP plan entries → the "todos" event (the employee's checklist). */
export function planEvent(entries: unknown): EventFields | null {
  if (!Array.isArray(entries)) return null;
  const todos = entries.slice(0, 100).map((e: Record<string, unknown>) => {
    const status = ["pending", "in_progress", "completed"].includes(String(e.status)) ? (String(e.status) as "pending") : "pending";
    return { content: String(e.content ?? "").slice(0, 2000), status };
  });
  return { kind: "todos", todos };
}

/** session/prompt's stopReason → turn_ended. */
export function turnEnded(stopReason: string, startedAt: number | undefined): EventFields {
  return {
    kind: "turn_ended",
    result_subtype: stopReason === "end_turn" ? "success" : stopReason,
    is_error: stopReason === "refusal",
    num_turns: 1,
    duration_ms: startedAt ? Date.now() - startedAt : 0,
    usage: {},
  };
}

/** A failed session/prompt → error / rate_limited (the quota error text is a guess until seen). */
export function promptError(code: number | undefined, message: string): EventFields {
  const text = summarize(message, 500);
  if (code === -32000 || /auth(entication)? required|not logged in|unauthenticated/i.test(message)) return { kind: "error", code: "auth_failed", message: text, fatal: false };
  if (/quota|rate.?limit|resource.?exhausted|\b429\b|too many requests/i.test(message)) return { kind: "rate_limited", detail: text };
  return { kind: "error", code: "protocol", message: text, fatal: false };
}
