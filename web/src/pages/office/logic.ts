// Pure helpers of the office page (web/src/pages/Office.tsx).
import { $t } from "@/i18n";
import type { OfficeSnapshot, QuestionView, ThreadView } from "@/api/views";
import type { OfficeAction, OfficeStatusEvent } from "@/office";

export const WHO_COLOR: Record<string, string> = { coordinator: "#B49CF2", lead: "#F2A65E", dev: "#7AA8FF", qa: "#8FD3A2" };
export const DARK = { page: "#1E2027", panel: "#262931", line: "#353844", soft: "#A8A49B", text: "#D8D4CB" };

export function hhmmss(at: string | number): string {
  const d = new Date(at);
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}

export interface BoardItem {
  state: string;
  text: string;
  color: string;
}

export function boardItems(threads: ThreadView[], questions: QuestionView[], claimable: number, focus: number | null): BoardItem[] {
  const inScope = (no: number | null) => focus === null || no === focus;
  const items: BoardItem[] = [];
  if (claimable && focus === null) items.push({ state: $t("可领"), text: $t("任务板上 {claimable} 个小任务", { claimable }), color: "#7AB8FF" });
  questions
    .filter((q) => inScope(q.thread_no))
    .forEach((q) => items.push({ state: $t("等你"), text: $t("{n}{n2}", { n: q.thread_no ? `#${q.thread_no} ` : "", n2: q.kind === "approval" ? $t("{asker} 请求权限", { asker: q.asker }) : q.text }), color: "#F2A65E" }));
  threads.filter((t) => t.status === "review" && inScope(t.no)).forEach((t) => items.push({ state: $t("待验收"), text: `#${t.no} ${t.title}`, color: "#B49CF2" }));
  threads.filter((t) => t.status === "queued" && inScope(t.no)).forEach((t) => items.push({ state: $t("排队"), text: `#${t.no} ${t.title}`, color: "#E6E3DC" }));
  return items.slice(0, 6);
}

/** Threads still going (the office draws a work area for these). */
export function liveThreads(threads: ThreadView[]): ThreadView[] {
  return threads.filter((t) => !["done", "archived", "failed"].includes(t.status));
}

/** The scope switch: 全部讨论串 then one button per live thread. */
export function scopes(threads: ThreadView[]): [number | null, string][] {
  return [[null, $t("全部讨论串")], ...threads.map((t) => [t.no, `#${t.no}`] as [number, string])];
}

/** Where a click in the office goes (null: nowhere). */
export function actionTarget(pid: string, a: OfficeAction, focus: number | null): string | null {
  if (a.type === "openThread") return `/projects/${pid}/threads/${a.threadNo}`;
  if (a.type === "openTaskPool" && focus !== null) return `/projects/${pid}/threads/${focus}`;
  if (a.type === "openNode") return "/settings/nodes";
  return null;
}

type Roster = OfficeSnapshot["employees"][number];

/** The newest events first, in scope, at most 14. */
export function recentEvents(events: OfficeStatusEvent[], roster: Map<string, Roster>, focus: number | null): OfficeStatusEvent[] {
  return [...events]
    .reverse()
    .filter((e) => focus === null || roster.get(String(e.employee))?.thread_no === focus)
    .slice(0, 14);
}

export function whoColor(who: Pick<Roster, "tier" | "role"> | undefined): string {
  return WHO_COLOR[who?.tier === "member" ? who.role : (who?.tier ?? "")] ?? "#7AA8FF";
}

/** The last hour, for /office/replay. */
export function lastHour(now = Date.now()): { from: string; to: string } {
  return { from: new Date(now - 3600_000).toISOString(), to: new Date(now).toISOString() };
}
