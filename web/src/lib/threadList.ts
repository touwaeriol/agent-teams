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
 * Where a thread goes: 等你处理 (an open card or acceptance waits on the user), 进行中
 * (someone is working, or it waits for a node), 空闲 (running or paused, nobody working and
 * nothing waiting), 已完成 (done or failed), 已归档.
 */
export function listGroupOf(t: ThreadView, waiting?: Map<number, QuestionView>): ListGroupId {
  if (t.status === "archived") return "archived";
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

/** When anything last happened in the thread (servers before last_activity_at: its last update or lead reply). */
export function lastActivity(t: ThreadView): string {
  if (t.last_activity_at) return t.last_activity_at;
  const lead = t.lead_last?.at;
  return lead && Date.parse(lead) > Date.parse(t.updated_at) ? lead : t.updated_at;
}

const byActivity = (a: ThreadView, b: ThreadView) => Date.parse(lastActivity(b)) - Date.parse(lastActivity(a)) || b.no - a.no;

export interface ListGroup {
  id: ListGroupId;
  label: string;
  items: ThreadView[];
}

/** Every thread in exactly one group, newest activity first; empty groups are left out. */
export function groupThreads(threads: ThreadView[], waiting?: Map<number, QuestionView>): ListGroup[] {
  const by = new Map<ListGroupId, ThreadView[]>();
  for (const t of threads) {
    const g = listGroupOf(t, waiting);
    by.set(g, [...(by.get(g) ?? []), t]);
  }
  return LIST_GROUPS.filter((g) => by.has(g.id)).map((g) => ({ ...g, items: by.get(g.id)!.sort(byActivity) }));
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
