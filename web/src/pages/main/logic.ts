import type { CoordinatorView, QuestionView, ThreadView } from "@/api/views";
import type { ThreadViewId } from "@/components/app/threadMenu";

/** Waiting questions keyed by thread number (first open one wins), for list lines. */
export function waitingIndex(questions: QuestionView[] | undefined): Map<number, QuestionView> {
  const map = new Map<number, QuestionView>();
  for (const q of questions ?? []) if (q.thread_no !== null && !map.has(q.thread_no)) map.set(q.thread_no, q);
  return map;
}

/** What the right-hand side shows: the thread panel (default), the library, or nothing. */
export type SidePanel = "threads" | "library" | "none";

export function sidePanelOf(value: unknown): SidePanel {
  return value === "library" || value === "none" ? value : "threads";
}

/** Where a thread opens: /projects/:pid/threads/:no, plus /tasks, /deliverables or /members when that view is open over the chat. */
export function threadPath(pid: string, no: number, view?: ThreadViewId | null): string {
  return `/projects/${pid}/threads/${no}${view ? `/${view}` : ""}`;
}

export function coordinatorState(c: CoordinatorView | undefined): { tone: string; text: string } {
  const name = c?.employee.name ?? "leader";
  if (!c?.employee.node) return { tone: "#B7B3AA", text: `${name} 待命` };
  if (!c.employee.node.online) return { tone: "#B42318", text: `${name} 离线` };
  return { tone: "#2E7D4F", text: `${name} 在线` };
}

/** Threads waiting on the user (open questions + PRs to accept) and the first such thread. */
export function waitingSummary(threads: ThreadView[]): { count: number; first: ThreadView | undefined } {
  const count = threads.reduce((n, t) => n + t.counts.waiting + (t.status === "review" ? 1 : 0), 0);
  return { count, first: threads.find((t) => t.counts.waiting > 0 || t.status === "review") };
}

export function coordinatorBusy(c: CoordinatorView | undefined): boolean {
  const s = c?.employee.status;
  return Boolean(s && !["idle", "offline"].includes(s.state));
}

/** The phone overview's tabs (?sheet=…): 讨论串 / 资料库 / 定时任务. */
export type OverviewTab = "threads" | "library" | "schedules";

export const OVERVIEW_TABS: { id: OverviewTab; label: string }[] = [
  { id: "threads", label: "讨论串" },
  { id: "library", label: "资料库" },
  { id: "schedules", label: "定时任务" },
];

/** The open overview tab, or null when the overview is closed. */
export function overviewTabOf(value: unknown): OverviewTab | null {
  return OVERVIEW_TABS.find((t) => t.id === value)?.id ?? null;
}
