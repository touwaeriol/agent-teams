// The right panel's thread list before a thread is opened: status groups, the row's mark and
// line, search and filters, relative times, and which groups the viewer folded (per user,
// in localStorage). Pure and unit-tested.
import type { QuestionView, ThreadView } from "@/api/views";
import { isWorking, liveLine } from "./live";
import { readLocal, writeLocal } from "./persist";
import { threadLine } from "./present";

export type ListGroupId = "waiting" | "active" | "idle" | "done" | "archived";

export const LIST_GROUPS: { id: ListGroupId; label: string }[] = [
  { id: "waiting", label: "等你处理" },
  { id: "active", label: "进行中" },
  { id: "idle", label: "空闲" },
  { id: "done", label: "已完成" },
  { id: "archived", label: "已归档" },
];

/**
 * Where a thread goes: 等你处理 (an open card or acceptance waits on the user, or a reminder
 * the user set has fired; archived threads stay archived), 进行中
 * (someone is working, or it waits for a node), 空闲 (running or paused, nobody working and
 * nothing waiting), 已完成 (done or failed), 已归档.
 */
export function listGroupOf(t: ThreadView, waiting?: Map<number, QuestionView>): ListGroupId {
  if (t.status === "archived") return "archived";
  if (t.reminded_at) return "waiting";
  if (t.status === "done" || t.status === "failed") return "done";
  if (t.status === "review" || t.status === "waiting" || t.counts.waiting > 0 || waiting?.has(t.no)) return "waiting";
  if (t.status === "queued") return "active";
  if (t.status === "running" && t.members.some(isWorking)) return "active";
  return "idle";
}

/** The round mark before a row. */
export type RowMark = "wait" | "working" | "queued" | "idle" | "done" | "failed" | "archived";

export function rowMark(t: ThreadView, group: ListGroupId): RowMark {
  if (group === "waiting") return "wait";
  if (group === "active") return t.status === "queued" ? "queued" : "working";
  if (group === "done") return t.status === "failed" ? "failed" : "done";
  return group;
}

/** 空闲, 已完成 and 已归档 rows are one compact line; 等你处理 and 进行中 keep the two-line row. */
export const isCompactGroup = (g: ListGroupId) => g === "idle" || g === "done" || g === "archived";

/** The status mark of a thread on its own (the open thread's header). */
export const threadMark = (t: ThreadView, waiting?: Map<number, QuestionView>): RowMark => rowMark(t, listGroupOf(t, waiting));

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

/** Every thread in exactly one group, pinned ones first, then newest activity first; empty groups are left out. */
export function groupThreads(threads: ThreadView[], waiting?: Map<number, QuestionView>): ListGroup[] {
  const by = new Map<ListGroupId, ThreadView[]>();
  for (const t of threads) {
    const g = listGroupOf(t, waiting);
    by.set(g, [...(by.get(g) ?? []), t]);
  }
  return LIST_GROUPS.filter((g) => by.has(g.id)).map((g) => ({ ...g, items: by.get(g.id)!.sort(byPinThenActivity) }));
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
export function filterThreads(threads: ThreadView[], f: ListFilter, waiting?: Map<number, QuestionView>): ThreadView[] {
  const q = f.q.trim().toLowerCase().replace(/^#/, "");
  return threads.filter((t) => {
    if (f.withDeliverable && !t.latest_deliverable) return false;
    if (f.groups.length > 0 && !f.groups.includes(listGroupOf(t, waiting))) return false;
    if (!q) return true;
    return String(t.no) === q || [t.title, t.summary, t.brief, t.lead_last?.text ?? ""].some((s) => s.toLowerCase().includes(q));
  });
}

/** "刚刚" / "51分钟" / "3小时" / "1天" / "11月2日" (older than 30 days). */
export function shortAgo(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const m = Math.floor(Math.max(0, now - t) / 60_000);
  if (m < 1) return "刚刚";
  if (m < 60) return `${m}分钟`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}小时`;
  const d = Math.floor(h / 24);
  if (d <= 30) return `${d}天`;
  const date = new Date(t);
  return `${date.getMonth() + 1}月${date.getDate()}日`;
}

/** Folded sections of the list ("deliverables" plus the group ids); 已归档 starts folded. */
export type Folded = Record<string, boolean>;

const DEFAULT_FOLDED: Folded = { archived: true };

export const foldedKey = (userId: string) => `at:thread-list-folded:${userId}`;

export function loadFolded(userId: string): Folded {
  const raw = readLocal(foldedKey(userId));
  if (!raw) return { ...DEFAULT_FOLDED };
  try {
    const v = JSON.parse(raw) as unknown;
    if (!v || typeof v !== "object" || Array.isArray(v)) return { ...DEFAULT_FOLDED };
    const out: Folded = { ...DEFAULT_FOLDED };
    for (const [k, b] of Object.entries(v)) if (typeof b === "boolean") out[k] = b;
    return out;
  } catch {
    return { ...DEFAULT_FOLDED };
  }
}

export function saveFolded(userId: string, folded: Folded): void {
  const diff = Object.fromEntries(Object.entries(folded).filter(([k, v]) => v !== Boolean(DEFAULT_FOLDED[k])));
  writeLocal(foldedKey(userId), Object.keys(diff).length ? JSON.stringify(diff) : null);
}

/**
 * The thread numbers in the order the panel list shows them: groups in order, filters applied,
 * folded groups skipped. A thread the list hides (folded or filtered out) still gets neighbours:
 * the order is then taken without folds, and failing that without filters.
 */
/** Thread numbers in the order the list shows them (folded groups skipped unless searching). */
export function visibleOrder(threads: ThreadView[], f: ListFilter, waiting?: Map<number, QuestionView>, folded: Folded = {}): number[] {
  return groupThreads(filterThreads(threads, f, waiting), waiting).flatMap((g) => (folded[g.id] && !f.q.trim() ? [] : g.items.map((t) => t.no)));
}

export function listOrder(threads: ThreadView[], no: number, f: ListFilter, waiting?: Map<number, QuestionView>, folded: Folded = {}): number[] {
  const order = (list: ThreadView[], skip: Folded) =>
    groupThreads(list, waiting).flatMap((g) => (skip[g.id] && !f.q.trim() ? [] : g.items.map((t) => t.no)));
  const shown = filterThreads(threads, f, waiting);
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
