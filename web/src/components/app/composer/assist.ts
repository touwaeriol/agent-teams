// Composer assist, pure parts: where a "/" or "@" popup starts, the platform's own commands,
// the popup rows, what a pick does to the text, and which @ references a message still carries.
import type { components } from "@/api/schema";

type S = components["schemas"];
export type ComposerCommand = S["ComposerCommand"];
export type RefCandidate = S["RefCandidate"];
export type MessageRef = S["MessageRef"];

/** Which employee the box talks to: the project coordinator or a thread's lead. */
export type Scope = "coordinator" | "lead";

export interface Trigger {
  kind: "slash" | "mention";
  /** Index of the "/" or "@". */
  start: number;
  /** Caret position (end of the typed query). */
  end: number;
  query: string;
}

/**
 * The popup trigger at the caret: "/name" only at the very start of the message (that is where
 * the session runs it), "@query" after a space or at the start. No whitespace inside the query.
 */
export function findTrigger(text: string, caret: number): Trigger | null {
  const before = text.slice(0, caret);
  const slash = /^\/(\S*)$/.exec(before);
  if (slash) return { kind: "slash", start: 0, end: caret, query: slash[1]! };
  const at = /(^|\s)@(\S*)$/.exec(before);
  if (at) return { kind: "mention", start: caret - at[2]!.length - 1, end: caret, query: at[2]! };
  return null;
}

/** Platform commands the web runs itself; each does something real (see runCommand). */
export interface PlatformCommand {
  name: string;
  desc: string;
  scopes: Scope[];
}

export const PLATFORM_COMMANDS: PlatformCommand[] = [
  { name: "model", desc: "选这位员工用的模型", scopes: ["coordinator", "lead"] },
  { name: "effort", desc: "讨论串的思考强度", scopes: ["lead"] },
  { name: "status", desc: "谁在忙、待处理的卡片、节点", scopes: ["coordinator", "lead"] },
  { name: "context", desc: "员工手上的上下文和本次会话的用量", scopes: ["coordinator", "lead"] },
  { name: "skills", desc: "这位员工的技能和插件", scopes: ["coordinator", "lead"] },
  { name: "usage", desc: "打开使用情况", scopes: ["coordinator", "lead"] },
  { name: "memory", desc: "打开记忆设置", scopes: ["coordinator", "lead"] },
  { name: "config", desc: "打开项目设置", scopes: ["coordinator", "lead"] },
];

export const platformCommands = (scope: Scope) => PLATFORM_COMMANDS.filter((c) => c.scopes.includes(scope));

/** The platform command a whole message names ("/status", "/model  "), if any. */
export function commandOf(text: string, scope: Scope): string | null {
  const m = /^\/([a-z]+)\s*$/.exec(text.trim());
  return m && platformCommands(scope).some((c) => c.name === m[1]) ? m[1]! : null;
}

/** One row of the popup. */
export interface AssistItem {
  key: string;
  group: string;
  label: string;
  detail: string;
  /** Platform command to run. */
  command?: string;
  /** Text that replaces the trigger. */
  insert?: string;
  /** Reference the message will carry. */
  ref?: MessageRef & { label: string };
}

/** Case-insensitive match: prefix first, then substring, then subsequence; null = no match. */
export function matchScore(q: string, text: string): number | null {
  if (!q) return 0;
  const a = q.toLowerCase();
  const b = text.toLowerCase();
  if (b.startsWith(a)) return 0;
  const i = b.indexOf(a);
  if (i >= 0) return 1 + i;
  let pos = 0;
  for (const ch of a) {
    const j = b.indexOf(ch, pos);
    if (j < 0) return null;
    pos = j + 1;
  }
  return 100 + pos;
}

function ranked<T>(q: string, list: T[], name: (x: T) => string): T[] {
  return list
    .map((x) => ({ x, s: matchScore(q, name(x)) }))
    .filter((r): r is { x: T; s: number } => r.s !== null)
    .sort((p, r) => p.s - r.s)
    .map((r) => r.x);
}

/** "/" rows: platform commands (group 命令), then the employee's skills and plugin commands (group 技能). */
export function slashItems(q: string, scope: Scope, skills: ComposerCommand[]): AssistItem[] {
  const commands = ranked(q, platformCommands(scope), (c) => c.name).map<AssistItem>((c) => ({
    key: `cmd:${c.name}`, group: "命令", label: `/${c.name}`, detail: c.desc, command: c.name,
  }));
  const own = ranked(q, skills, (c) => c.name).map<AssistItem>((c) => ({
    key: `skill:${c.name}`, group: "技能", label: `/${c.name}`, detail: c.description || (c.source === "plugin" ? "插件命令" : "技能"),
    insert: `/${c.name} `,
  }));
  return [...commands, ...own];
}

const REF_GROUPS: Record<RefCandidate["kind"], string> = { file: "文件", thread: "讨论串", deliverable: "交付物", memory: "记忆" };
const REF_ORDER: RefCandidate["kind"][] = ["file", "thread", "deliverable", "memory"];

/** The token a reference leaves in the text: @path, @#12, @标题, @name.md. */
export function refToken(c: Pick<RefCandidate, "kind" | "label" | "no">): string {
  if (c.kind === "thread") return `@#${c.no}`;
  return `@${c.label.replace(/\s+/g, "_")}`;
}

/** "@" rows from the server's candidates (already filtered), grouped 文件 / 讨论串 / 交付物 / 记忆. */
export function mentionItems(list: RefCandidate[]): AssistItem[] {
  return REF_ORDER.flatMap((kind) =>
    list
      .filter((c) => c.kind === kind)
      .map<AssistItem>((c) => ({
        key: `${c.kind}:${c.repo ?? ""}:${c.path ?? c.no ?? c.id ?? c.name}`,
        group: REF_GROUPS[c.kind],
        label: c.label,
        detail: c.detail,
        insert: `${refToken(c)} `,
        ref: { kind: c.kind, label: c.label, ...(c.repo ? { repo: c.repo } : {}), ...(c.path ? { path: c.path } : {}), ...(c.no ? { no: c.no } : {}), ...(c.id ? { id: c.id } : {}), ...(c.name ? { name: c.name } : {}) },
      })),
  );
}

/** The text after a pick: the trigger replaced by the insertion, and where the caret goes. */
export function applyPick(text: string, t: Trigger, insertion: string): { text: string; caret: number } {
  const next = text.slice(0, t.start) + insertion + text.slice(t.end);
  return { text: next, caret: t.start + insertion.length };
}

/** The references a message carries: those whose token is still in the text, each once. */
export function refsInText(text: string, refs: Array<MessageRef & { label: string }>): MessageRef[] {
  const seen = new Set<string>();
  const out: MessageRef[] = [];
  for (const r of refs) {
    const token = refToken({ kind: r.kind, label: r.label, no: r.no });
    const key = `${r.kind}:${r.repo ?? ""}:${r.path ?? r.no ?? r.id ?? r.name}`;
    if (!text.includes(token) || seen.has(key)) continue;
    seen.add(key);
    const { label: _label, ...ref } = r;
    out.push(ref);
  }
  return out;
}

/** Index after moving the highlight by delta, wrapping around. */
export function moveIndex(i: number, delta: number, n: number): number {
  if (n === 0) return 0;
  return (((i + delta) % n) + n) % n;
}
