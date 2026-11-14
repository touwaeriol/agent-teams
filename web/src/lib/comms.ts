// Pure helpers for worker questions, approvals, mail, plans and sub-tasks.
// No React and no runtime imports, so `node --test web/tests` can load it.
import type { Mail, Message, Question, Task } from "./api";

/** Answers an approval card can send (see docs/agent-comms.md). */
export const APPROVAL_ANSWERS = { allow: "allow", always: "allow_always", deny: "deny" } as const;

/** Open questions that wait for the user (not the lead). */
export function pendingForUser(questions: Question[] | undefined): Question[] {
  return (questions ?? []).filter((q) => q.status === "open" && q.audience === "user");
}

/** Insert or replace by ``id``, keeping the list's order (new items go last). */
export function upsertById<T extends { id: string | number }>(list: T[], item: T): T[] {
  const index = list.findIndex((x) => x.id === item.id);
  if (index === -1) return [...list, item];
  const copy = [...list];
  copy[index] = item;
  return copy;
}

export interface TaskNode {
  task: Task;
  depth: number;
}

/**
 * Tasks in display order: each sub-task right below its parent, one level
 * deeper. A sub-task whose parent is not in the list is shown at the top level.
 */
export function taskTree(tasks: Task[]): TaskNode[] {
  const ids = new Set(tasks.map((t) => t.id));
  const children = new Map<string, Task[]>();
  const roots: Task[] = [];
  for (const t of tasks) {
    const parent = t.parent && ids.has(t.parent) && t.parent !== t.id ? t.parent : "";
    if (parent) children.set(parent, [...(children.get(parent) ?? []), t]);
    else roots.push(t);
  }
  const out: TaskNode[] = [];
  const seen = new Set<string>();
  const visit = (task: Task, depth: number) => {
    if (seen.has(task.id)) return;
    seen.add(task.id);
    out.push({ task, depth });
    for (const child of children.get(task.id) ?? []) visit(child, depth + 1);
  };
  roots.forEach((t) => visit(t, 0));
  // Parent cycles have no root; show what is left flat so nothing disappears.
  tasks.forEach((t) => visit(t, 0));
  return out;
}

/** How a recipient reads in the activity feed. */
export function recipientLabel(recipient: string): string {
  if (recipient === "*") return "全体";
  if (recipient === "lead") return "负责人";
  return recipient;
}

export type TimelineItem = { kind: "message"; at: number; message: Message } | { kind: "question"; at: number; question: Question };

/** Conversation messages and the user's question cards, in time order. */
export function chatTimeline(messages: Message[], questions: Question[]): TimelineItem[] {
  const items: TimelineItem[] = [
    ...messages.map((message) => ({ kind: "message" as const, at: Date.parse(message.created_at), message })),
    ...questions.filter((q) => q.audience === "user").map((question) => ({ kind: "question" as const, at: Date.parse(question.created_at), question })),
  ];
  return stableSortByTime(items);
}

export interface ActivityLine {
  at: number;
  text: string;
}

export type ActivityItem = { kind: "line"; at: number; text: string } | { kind: "mail"; at: number; mail: Mail };

/** Worker activity lines and mail between members, in time order. */
export function activityFeed(lines: ActivityLine[], mail: Mail[]): ActivityItem[] {
  const items: ActivityItem[] = [
    ...lines.map((l) => ({ kind: "line" as const, at: l.at, text: l.text })),
    ...mail.map((m) => ({ kind: "mail" as const, at: Date.parse(m.created_at), mail: m })),
  ];
  return stableSortByTime(items);
}

function stableSortByTime<T extends { at: number }>(items: T[]): T[] {
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => (a.item.at || 0) - (b.item.at || 0) || a.index - b.index)
    .map((x) => x.item);
}

/** What an approval asks to run: the shell command when there is one, else the input as JSON. */
export function approvalBody(question: Question): { tool: string; command: string | null; input: string } {
  const tool = typeof question.detail?.tool_name === "string" ? question.detail.tool_name : "未知工具";
  const input = question.detail?.input ?? {};
  const command = typeof input.command === "string" ? input.command : null;
  return { tool, command, input: JSON.stringify(input, null, 2) };
}

/** How an answered question reads on its card. */
export function answerLabel(question: Question): string {
  if (question.status === "cancelled") return "已取消";
  if (question.kind !== "approval") return question.answer ?? "";
  return { allow: "已放行", allow_always: "已总是允许", deny: "已拒绝" }[question.answer ?? ""] ?? question.answer ?? "";
}
