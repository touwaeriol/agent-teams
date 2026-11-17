// The right panel's thread list before a thread is opened: status groups, the row's mark and
// line, search and filters, relative times, and which groups the viewer folded (per user,
// in localStorage). Pure and unit-tested.
import type { QuestionView, ThreadView } from "@/api/views";
import { isWorking, liveLine } from "./live";
import { readLocal, writeLocal } from "./persist";
import { threadLine } from "./present";

export { shortAgo } from "./relativeTime";

export type ListGroupId = "active" | "done" | "failed" | "paused" | "archived";

export const LIST_GROUPS: { id: ListGroupId; label: string }[] = [
  { id: "active", label: "进行中" },
  { id: "done", label: "已解决" },
  { id: "failed", label: "失败" },
  { id: "paused", label: "已暂停" },
  { id: "archived", label: "已归档" },
];

const GROUP_BY_STATUS: Record<ThreadView["status"], ListGroupId> = {
  queued: "active",
  running: "active",
  waiting: "active",
  review: "active",
  done: "done",
  failed: "failed",
  paused: "paused",
  archived: "archived",
};

/**
 * Where a thread goes, by its status: queued / running / waiting / review are 进行中, done is
 * 已解决, failed 失败, paused 已暂停, archived 已归档. A reminder the user set that has fired
 * brings a thread back to 进行中 (archived threads stay archived). What waits on the user is
 * told apart inside 进行中 by the row's mark (and sorted first), not by a group of its own.
 */
export function listGroupOf(t: ThreadView): ListGroupId {
  if (t.status === "archived") return "archived";
  if (t.reminded_at) return "active";
  return GROUP_BY_STATUS[t.status] ?? "active";
}

/** Something waits on the user: acceptance, an open card or approval, a question, or a fired reminder. */
export function waitsOnUser(t: ThreadView, waiting?: Map<number, QuestionView>): boolean {
  if (t.status === "archived") return false;
  return Boolean(t.reminded_at) || t.status === "review" || t.status === "waiting" || t.counts.waiting > 0 || Boolean(waiting?.has(t.no));
}

/** The round mark before a row (and in the open thread's header): see ThreadStatusIcon. */
export type RowMark = "wait" | "working" | "queued" | "idle" | "done" | "failed" | "paused" | "archived";

const SETTLED_MARK: Partial<Record<ListGroupId, RowMark>> = { done: "done", failed: "failed", paused: "paused", archived: "archived" };

/**
 * The status mark of a thread: settled groups have their own mark; in 进行中, orange when it
 * waits on the user, a spinner when someone works (grey while queued), a dashed ring when idle.
 */
export function threadMark(t: ThreadView, waiting?: Map<number, QuestionView>): RowMark {
  const group = listGroupOf(t);
  const settled = group === "active" ? undefined : SETTLED_MARK[group];
  if (settled) return settled;
  if (waitsOnUser(t, waiting)) return "wait";
  if (t.status === "queued") return "queued";
  return t.members.some(isWorking) ? "working" : "idle";
}

/**
 * Only 进行中 and 失败 rows have a second line (progress, tags, the tree line); 已解决 / 已暂停 /
 * 已归档 are one compact line. 失败 stays rich on purpose: a failure needs the user's attention,
 * so its reason and plan progress stay visible.
 */
export const isCompactGroup = (g: ListGroupId) => g !== "active" && g !== "failed";

/** Main tasks done / total ("2/7"), from the plan when the thread carries it; null without a plan. */
export function mainProgress(t: Pick<ThreadView, "main_tasks" | "counts">): { done: number; total: number; label: string } | null {
  const total = t.main_tasks.length || t.counts.main_total;
  if (!total) return null;
  const done = t.main_tasks.length ? t.main_tasks.filter((m) => m.status === "done").length : t.counts.main_done;
  return { done, total, label: `${done}/${total}` };
}

/** How many deliverables the thread published (older servers: 1 when it has a newest one). */
export const deliverableCount = (t: Pick<ThreadView, "counts" | "latest_deliverable">): number =>
  t.counts.deliverables ?? (t.latest_deliverable ? 1 : 0);

export type NodeState = "online" | "partial" | "offline" | "none";

/**
 * The nodes the thread's employees run on: all online, some offline, all offline, or none
 * assigned; `label` is the tooltip ("rc-erio 在线 · cvp 离线").
 */
export function threadNodes(t: Pick<ThreadView, "members">): { state: NodeState; nodes: { id: string; name: string; online: boolean }[]; label: string } {
  const by = new Map<string, { id: string; name: string; online: boolean }>();
  for (const m of t.members) if (m.node && !by.has(m.node.id)) by.set(m.node.id, m.node);
  const nodes = [...by.values()];
  if (nodes.length === 0) return { state: "none", nodes, label: "还没有分配节点" };
  const up = nodes.filter((n) => n.online).length;
  const state: NodeState = up === nodes.length ? "online" : up === 0 ? "offline" : "partial";
  return { state, nodes, label: nodes.map((n) => `${n.name} ${n.online ? "在线" : "离线"}`).join(" · ") };
}

/** When anything last happened in the thread (servers before last_activity_at: its last update or lead reply). */
export function lastActivity(t: ThreadView): string {
  if (t.last_activity_at) return t.last_activity_at;
  const lead = t.lead_last?.at;
  return lead && Date.parse(lead) > Date.parse(t.updated_at) ? lead : t.updated_at;
}

const pinTime = (t: ThreadView) => (t.pinned_at ? Date.parse(t.pinned_at) : 0);
/** Pinned first (newest pin first), then newest activity first. */
const byPinThenActivity = (a: ThreadView, b: ThreadView) =>
  pinTime(b) - pinTime(a) || Date.parse(lastActivity(b)) - Date.parse(lastActivity(a)) || b.no - a.no;

export interface ListGroup {
  id: ListGroupId;
  label: string;
  items: ThreadView[];
}

/**
 * Every thread in exactly one group, groups in LIST_GROUPS order, empty ones left out. Inside a
 * group: what waits on the user first (进行中), then pinned ones, then newest activity first.
 */
export function groupThreads(threads: ThreadView[], waiting?: Map<number, QuestionView>): ListGroup[] {
  const by = new Map<ListGroupId, ThreadView[]>();
  for (const t of threads) {
    const g = listGroupOf(t);
    by.set(g, [...(by.get(g) ?? []), t]);
  }
  const userFirst = (a: ThreadView, b: ThreadView) => Number(waitsOnUser(b, waiting)) - Number(waitsOnUser(a, waiting)) || byPinThenActivity(a, b);
  return LIST_GROUPS.filter((g) => by.has(g.id)).map((g) => ({ ...g, items: by.get(g.id)!.sort(g.id === "active" ? userFirst : byPinThenActivity) }));
}

const firstLine = (s: string) => s.split("\n").map((l) => l.trim()).find(Boolean) ?? "";

/** The grey line under a title: what waits on the user, else the lead's latest message (first line), the summary, the brief. */
export function rowLine(t: ThreadView, open?: QuestionView): string {
  if (!open && t.counts.waiting > 0 && t.status !== "review") return liveLine(t) || firstLine(threadLine(t));
  return firstLine(threadLine(t, open));
}

export interface ListFilter {
  q: string;
  /** Groups to show; empty means all. */
  groups: ListGroupId[];
  /** Only threads that published a deliverable. */
  withDeliverable: boolean;
}

export const NO_FILTER: ListFilter = { q: "", groups: [], withDeliverable: false };

export const filterActive = (f: ListFilter) => f.groups.length > 0 || f.withDeliverable;

/** Search by number, title, summary, brief or the lead's latest message; then the popover's filters. */
export function filterThreads(threads: ThreadView[], f: ListFilter): ThreadView[] {
  const q = f.q.trim().toLowerCase().replace(/^#/, "");
  return threads.filter((t) => {
    if (f.withDeliverable && !t.latest_deliverable) return false;
    if (f.groups.length > 0 && !f.groups.includes(listGroupOf(t))) return false;
    if (!q) return true;
    return String(t.no) === q || [t.title, t.summary, t.brief, t.lead_last?.text ?? ""].some((s) => s.toLowerCase().includes(q));
  });
}

/** Folded sections of the list ("deliverables" plus the group ids); 已归档 starts folded. */
export type Folded = Record<string, boolean>;

const DEFAULT_FOLDED: Folded = { archived: true };
/** Keys still in use; folds saved for groups that no longer exist (等你处理 / 空闲) are dropped. */
const FOLD_KEYS = new Set<string>(["deliverables", ...LIST_GROUPS.map((g) => g.id)]);

export const foldedKey = (userId: string) => `at:thread-list-folded:${userId}`;

export function loadFolded(userId: string): Folded {
  const raw = readLocal(foldedKey(userId));
  if (!raw) return { ...DEFAULT_FOLDED };
  try {
    const v = JSON.parse(raw) as unknown;
    if (!v || typeof v !== "object" || Array.isArray(v)) return { ...DEFAULT_FOLDED };
    const out: Folded = { ...DEFAULT_FOLDED };
    for (const [k, b] of Object.entries(v)) if (typeof b === "boolean" && FOLD_KEYS.has(k)) out[k] = b;
    return out;
  } catch {
    return { ...DEFAULT_FOLDED };
  }
}

export function saveFolded(userId: string, folded: Folded): void {
  const diff = Object.fromEntries(Object.entries(folded).filter(([k, v]) => FOLD_KEYS.has(k) && v !== Boolean(DEFAULT_FOLDED[k])));
  writeLocal(foldedKey(userId), Object.keys(diff).length ? JSON.stringify(diff) : null);
}

/** Thread numbers in the order the list shows them (folded groups skipped unless searching). */
export function visibleOrder(threads: ThreadView[], f: ListFilter, waiting?: Map<number, QuestionView>, folded: Folded = {}): number[] {
  return groupThreads(filterThreads(threads, f), waiting).flatMap((g) => (folded[g.id] && !f.q.trim() ? [] : g.items.map((t) => t.no)));
}

/**
 * The thread numbers in the order the panel list shows them: groups in order, filters applied,
 * folded groups skipped. A thread the list hides (folded or filtered out) still gets neighbours:
 * the order is then taken without folds, and failing that without filters.
 */
export function listOrder(threads: ThreadView[], no: number, f: ListFilter, waiting?: Map<number, QuestionView>, folded: Folded = {}): number[] {
  const order = (list: ThreadView[], skip: Folded) =>
    groupThreads(list, waiting).flatMap((g) => (skip[g.id] && !f.q.trim() ? [] : g.items.map((t) => t.no)));
  const shown = filterThreads(threads, f);
  for (const candidate of [order(shown, folded), order(shown, {}), order(threads, {})]) if (candidate.includes(no)) return candidate;
  return [];
}

/** The threads before and after `no` in `order` (null at either end: no wrapping). */
export function neighbours(order: number[], no: number): { prev: number | null; next: number | null } {
  const i = order.indexOf(no);
  if (i < 0) return { prev: null, next: null };
  return { prev: i > 0 ? order[i - 1] : null, next: i < order.length - 1 ? order[i + 1] : null };
}

const pad = (n: number) => String(n).padStart(2, "0");
const WEEKDAYS = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
const dayStart = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();

/** "今天 18:00" / "明天 09:00" / "周一 09:00" (within a week) / "11月20日 09:00", local time. */
export function reminderLabel(iso: string, now: number = Date.now()): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  const time = `${pad(at.getHours())}:${pad(at.getMinutes())}`;
  const days = Math.round((dayStart(at) - dayStart(new Date(now))) / 86_400_000);
  if (days === 0) return `今天 ${time}`;
  if (days === 1) return `明天 ${time}`;
  if (days > 1 && days < 7) return `${WEEKDAYS[at.getDay()]} ${time}`;
  return `${at.getMonth() + 1}月${at.getDate()}日 ${time}`;
}

export type ReminderPreset = "hour" | "later" | "tomorrow" | "monday";

/**
 * The 添加提醒 choices at `now` (local time): 1 小时后; 今天晚些时候 (18:00, or three hours on
 * once it is past 15:00); 明天上午 9 点; 下周一 9 点 (the coming Monday, a week on when today is Monday).
 */
export function reminderPresets(now: Date = new Date()): { id: ReminderPreset; label: string; at: Date }[] {
  const at = (days: number, hour: number) => new Date(now.getFullYear(), now.getMonth(), now.getDate() + days, hour, 0, 0, 0);
  const evening = at(0, 18);
  const later = evening.getTime() - now.getTime() >= 3 * 3_600_000 ? evening : new Date(now.getTime() + 3 * 3_600_000);
  const toMonday = ((8 - now.getDay()) % 7) || 7;
  return [
    { id: "hour", label: "1 小时后", at: new Date(now.getTime() + 3_600_000) },
    { id: "later", label: "今天晚些时候", at: later },
    { id: "tomorrow", label: "明天上午 9 点", at: at(1, 9) },
    { id: "monday", label: "下周一 9 点", at: at(toMonday, 9) },
  ];
}
