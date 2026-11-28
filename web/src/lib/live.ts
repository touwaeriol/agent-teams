// Live progress of a thread, pure and unit-tested: what each employee is doing right now,
// how far its tasks are, and how long it has been running.
import { statusBubble } from "@/lib/bubble";
import { $t } from "@/i18n";
import type { EmployeeBrief, TaskView, ThreadView } from "@/api/views";

/**
 * Why a queued employee is waiting when the server says it is not a free slot (substate
 * no_runtime: no online node has the runtime installed and logged in, or it is still being checked);
 * the reason is the status bubble. "" otherwise.
 */
export function queueReason(m: Pick<EmployeeBrief, "status">): string {
  const s = m.status;
  return s?.state === "queued" && s.substate === "no_runtime" ? statusBubble(s.bubble) : "";
}

/** States in which an employee is actively working (spinner / pulse). */
const WORKING = new Set(["typing", "reading", "running", "thinking", "claiming", "walking_to_board", "returning"]);

export function isWorking(m: Pick<EmployeeBrief, "status">): boolean {
  return WORKING.has(m.status?.state ?? "idle");
}

/**
 * Working for this thread: a shared project member working in another thread (busy_elsewhere in
 * a thread's member list) does not count here.
 */
export function workingHere(m: Pick<EmployeeBrief, "status" | "busy_elsewhere">): boolean {
  return !m.busy_elsewhere && isWorking(m);
}

/** A message is on its way to a parked or closed session, which is coming back up. */
export function isWaking(m: Pick<EmployeeBrief, "status">): boolean {
  return m.status?.state === "thinking" && m.status.substate === "waking";
}

const VERB: Record<string, string> = {
  typing: $t("在写"),
  reading: $t("在看"),
  running: $t("在跑"),
  thinking: $t("思考中"),
  claiming: $t("领任务"),
  raising_hand: $t("等你审批"),
  waiting: $t("等别人"),
  queued: $t("排队等工位"),
  offline: $t("节点离线"),
  broken: $t("出错了"),
  idle: $t("空闲"),
};

/** Short state label shown before the activity line. */
export function stateLabel(m: Pick<EmployeeBrief, "status">): string {
  if (isWaking(m)) return $t("唤醒中");
  if (m.status?.state === "queued" && m.status.substate === "no_runtime") return $t("排队等节点");
  const s = m.status?.state ?? "idle";
  return VERB[s] ?? s;
}

/** The state label next to someone's open cards: a raised hand with no card for the user waits on the lead. */
export function askingLabel(m: Pick<EmployeeBrief, "status">, asksUser: boolean): string {
  if (asksUser) return $t("等你审批");
  return m.status?.state === "raising_hand" ? $t("等组长回复") : stateLabel(m);
}

export interface TaskProgress {
  done: number;
  total: number;
  running: number;
  failed: number;
  pct: number;
}

/** Small-task progress (cancelled tasks are left out). */
export function taskProgress(tasks: Pick<TaskView, "status">[]): TaskProgress {
  const live = tasks.filter((t) => t.status !== "cancelled");
  const done = live.filter((t) => t.status === "done").length;
  const running = live.filter((t) => ["claimed", "running", "review", "decision", "merging"].includes(t.status)).length;
  const failed = live.filter((t) => t.status === "failed").length;
  return { done, total: live.length, running, failed, pct: live.length ? Math.round((done / live.length) * 100) : 0 };
}

/** "45 秒" / "12 分" / "2 小时 5 分" / "3 天 2 小时" between two instants. */
export function elapsed(fromIso: string | null | undefined, now: number): string {
  if (!fromIso) return "";
  const s = Math.max(0, Math.floor((now - Date.parse(fromIso)) / 1000));
  if (s < 60) return $t("{s} 秒", { s });
  const m = Math.floor(s / 60);
  if (m < 60) return $t("{m} 分", { m });
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? $t("{h} 小时 {n} 分", { h, n: m % 60 }) : $t("{h} 小时", { h });
  const d = Math.floor(h / 24);
  return h % 24 ? $t("{d} 天 {n} 小时", { d, n: h % 24 }) : $t("{d} 天", { d });
}

/** Whether a thread is still going (live feedback is shown and the clock ticks). */
export function threadActive(t: Pick<ThreadView, "status">): boolean {
  return ["queued", "running", "waiting", "paused"].includes(t.status);
}

/** The employee to feature in a one-line live status: someone raising a hand, else someone working, else the lead. */
export function featured(t: Pick<ThreadView, "members">): EmployeeBrief | undefined {
  return (
    t.members.find((m) => !m.busy_elsewhere && m.status?.state === "raising_hand") ??
    t.members.find((m) => m.tier === "member" && workingHere(m)) ??
    t.members.find((m) => workingHere(m)) ??
    t.members.find((m) => m.tier === "lead")
  );
}

/** One compact line for chat chips: "dev-1 在跑 · npm test" / "等你审批 2 条命令". */
export function liveLine(t: Pick<ThreadView, "members" | "status" | "counts" | "pr_number" | "pause_reason">): string {
  if (t.counts.approvals_open > 0) return $t("等你审批 {approvals_open} 条命令", { approvals_open: t.counts.approvals_open });
  if (t.counts.questions_open > 0) return $t("等你回答 {questions_open} 个问题", { questions_open: t.counts.questions_open });
  if (!threadActive(t)) return "";
  if (t.status === "queued") return $t("排队等节点");
  if (t.status === "paused") return $t("已暂停");
  const m = featured(t);
  if (!m?.status || m.status.state === "idle") return "";
  const doing = m.status.bubble ? ` · ${statusBubble(m.status.bubble)}` : "";
  return `${m.name} ${stateLabel(m)}${doing}`;
}
