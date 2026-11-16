// Claude Code's own interactive tools, bridged to the platform (protocol.md §4.8, architecture.md §6.6):
// - AskUserQuestion is answered on the platform's question card (ask_user with via=AskUserQuestion,
//   blocking for every tier); the PreToolUse hook hands the answers back as updatedInput.answers,
//   which the CLI turns into the tool result. Without this a headless session gets
//   "The user did not answer the questions." (bypassPermissions still routes it to canUseTool).
// - TodoWrite becomes a "todos" event (the chat's live checklist), ExitPlanMode a "plan" event.
import { existsSync, readFileSync } from "node:fs";
import { isAbsolute, relative, resolve } from "node:path";
import type { HookJSONOutput } from "@anthropic-ai/claude-agent-sdk";
import type { McpToolResult } from "../../toolhost/broker.js";
import type { EventFields } from "../types.js";

export const ASK_TOOL = "AskUserQuestion";
const PLAN_MAX = 32 * 1024;
const TODO_MAX = 100;

interface ClaudeQuestion {
  question?: unknown;
  header?: unknown;
  multiSelect?: unknown;
  options?: Array<{ label?: unknown; description?: unknown }>;
}

interface AnswerItem {
  question: string;
  header?: string;
  selected: string[];
  other?: string;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** AskUserQuestion input → ask_user arguments (structured questions, blocking). */
export function askUserArgs(input: Record<string, unknown>): Record<string, unknown> {
  const qs = (Array.isArray(input.questions) ? input.questions : []) as ClaudeQuestion[];
  const questions = qs.slice(0, 4).map((q) => ({
    question: str(q.question) || "?",
    header: (str(q.header) || "问题").slice(0, 40),
    ...(q.multiSelect === true ? { multi_select: true } : {}),
    options: (q.options ?? []).slice(0, 4).map((o) => ({ label: str(o.label) || "?", ...(str(o.description) ? { description: str(o.description) } : {}) })),
  }));
  return { questions, via: ASK_TOOL };
}

/** The user's answers as AskUserQuestion expects them: question text → "label, label" (or the typed answer). */
export function claudeAnswers(res: McpToolResult): Record<string, string> | null {
  if (res.isError) return null;
  const items = (res.structuredContent?.answers ?? null) as AnswerItem[] | null;
  if (!Array.isArray(items)) return null;
  const out: Record<string, string> = {};
  for (const a of items) out[a.question] = [...(a.selected ?? []), ...(a.other ? [a.other] : [])].join(", ");
  return out;
}

/** PreToolUse output that runs AskUserQuestion with the answers filled in, or refuses it with the reason. */
export function askHookOutput(input: Record<string, unknown>, res: McpToolResult): HookJSONOutput {
  const answers = claudeAnswers(res);
  if (!answers) {
    const text = res.content.map((c) => ("text" in c ? String(c.text) : "")).join("").slice(0, 2000) || "the question was not answered";
    return { hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: `The user did not answer on the platform: ${text}` } };
  }
  return { hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "allow", updatedInput: { ...input, answers } } };
}

/** TodoWrite input → the "todos" event (latest full list). */
export function todosEvent(input: Record<string, unknown>): EventFields | null {
  if (!Array.isArray(input.todos)) return null;
  const todos = (input.todos as Array<Record<string, unknown>>).slice(0, TODO_MAX).map((t) => {
    const status = ["pending", "in_progress", "completed"].includes(str(t.status)) ? (str(t.status) as "pending") : "pending";
    const active = str(t.activeForm).slice(0, 2000);
    return { content: str(t.content).slice(0, 2000), status, ...(active ? { active_form: active } : {}) };
  });
  return { kind: "todos", todos };
}

/**
 * ExitPlanMode → the "plan" event. The plan is the input's plan (older CLIs) or, after the call,
 * the response's plan / the plan file the CLI wrote under its config directory.
 */
export function planEvent(input: Record<string, unknown>, response: unknown, configDir: string): EventFields | null {
  const r = (response ?? {}) as { plan?: unknown; filePath?: unknown };
  const text = str(input.plan) || str(r.plan) || planFile(str(r.filePath), configDir);
  if (!text.trim()) return null;
  return { kind: "plan", plan: text.length > PLAN_MAX ? text.slice(0, PLAN_MAX) : text };
}

function planFile(path: string, configDir: string): string {
  if (!path || !isAbsolute(path)) return "";
  const rel = relative(resolve(configDir), resolve(path));
  if (rel.startsWith("..") || isAbsolute(rel) || !existsSync(path)) return "";
  try {
    return readFileSync(path, "utf8");
  } catch {
    return "";
  }
}
