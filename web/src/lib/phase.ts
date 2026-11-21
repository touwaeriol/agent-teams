// The one place a thread's state on screen is worked out: the panel list (group, row mark, row
// line), the open thread (header mark, the line at the bottom of the chat) and the coordinator /
// overview cards (threadKind in present.ts) all read threadPhase, so they never disagree.
// Pure and unit-tested.
import type { QuestionView, ThreadView } from "@/api/views";
import { isWaking, isWorking, liveLine, queueReason, workingHere } from "./live";

export type ListGroupId = "waiting" | "active" | "idle" | "done" | "archived";

/**
 * The list's groups. "idle" now only holds paused threads and reads 已暂停 (the id stays "idle"
 * so folds remembered in localStorage keep working).
 */
export const LIST_GROUPS: { id: ListGroupId; label: string }[] = [
  { id: "waiting", label: "等你处理" },
  { id: "active", label: "进行中" },
  { id: "idle", label: "已暂停" },
  { id: "done", label: "已完成" },
  { id: "archived", label: "已归档" },
];

/** The round mark before a row and in the open thread's header. */
export type RowMark = "wait" | "working" | "queued" | "paused" | "done" | "failed" | "archived";

export const MARK_LABEL: Record<RowMark, string> = {
  wait: "等你处理",
  working: "进行中",
  queued: "排队中",
  paused: "已暂停",
  done: "已完成",
  failed: "失败",
  archived: "已归档",
};

export type PhaseId =
  | "archived"
  | "done"
  | "failed"
  /** A reminder the user set fired. */
  | "reminded"
  /** In review and waiting for the user's acceptance (merge_policy manual, or a merge failed). */
  | "acceptance"
  /** An open card (question or approval) for the user, or status waiting. */
  | "asking"
  /** Running, nobody working and no small task left: the lead spoke and stopped, the ball is with the user. */
  | "replied"
  | "queued"
  /** Running and someone works for this thread right now. */
  | "working"
  /** Running, nobody working here, but small tasks are still open (to be claimed, claimed, in review or merging; or the member is busy elsewhere). */
  | "tasks"
  /** Running, nothing else going, and the lead has not said anything yet: it is reading the brief. */
  | "reading"
  /** Running, nothing else going, the lead's session waits for a free slot on its node. */
  | "lead_queued"
  /** Running, nothing else going, the lead waits on someone other than the user (a card to the coordinator, a task in review). */
  | "lead_waiting"
  /** Running, nothing else going, the lead's node is offline or its session crashed: nothing moves until the user looks. */
  | "lead_down"
  /** In review under merge_policy pr / auto: the platform waits on the PR's checks by itself. */
  | "checks"
  | "paused";

export interface ThreadPhase {
  id: PhaseId;
  group: ListGroupId;
  mark: RowMark;
  /** The state's name, the same wherever the thread shows. */
  label: string;
  /** What it is waiting on / doing, in a few words ("" when finished). */
  note: string;
}

/**
 * A thread in review waits on the user only for an acceptance (merge_policy manual) or after a
 * merge failed; under pr and auto the platform waits on the PR's checks by itself (as the server's
 * counts.waiting does).
 */
export const awaitsAcceptance = (t: Pick<ThreadView, "status" | "merge_policy" | "merge_error">): boolean =>
  t.status === "review" && (t.merge_policy === "manual" || Boolean(t.merge_error));

export const REVIEW_LINE: Record<ThreadView["merge_policy"], string> = { pr: "等检查通过", auto: "检查通过后自动合并", manual: "等你验收" };

/** The review line, plus the small tasks that still hold the thread's end (they keep it in review after the checks pass). */
export function reviewLine(t: Pick<ThreadView, "merge_policy" | "counts"> & { pr_state?: string | null }): string {
  const line = t.pr_state === "merged" ? "已合并，等收尾" : (REVIEW_LINE[t.merge_policy] ?? REVIEW_LINE.pr);
  const open = openTasks(t);
  return open > 0 ? `${line}，还有 ${open} 个小任务没结束` : line;
}

/** Small tasks not finished yet (older servers without tasks_open: everything not done). */
export const openTasks = (t: Pick<ThreadView, "counts">): number => t.counts.tasks_open ?? Math.max(0, t.counts.tasks_total - t.counts.tasks_done);

const PAUSE_REASONS: Record<string, string> = { user: "已暂停", rate_limit: "Claude 使用限额用完，已暂停" };

export function pauseLine(reason: string): string {
  if (!reason) return "已暂停";
  return PAUSE_REASONS[reason] ?? `已暂停：${reason}`;
}

export type PhaseInput = Pick<
  ThreadView,
  "no" | "status" | "members" | "counts" | "merge_policy" | "merge_error" | "queue_position" | "pause_reason" | "pr_number" | "reminded_at" | "lead_last"
> & { pr_state?: string | null };

const phase = (id: PhaseId, group: ListGroupId, mark: RowMark, note: string, label: string = MARK_LABEL[mark]): ThreadPhase => ({ id, group, mark, label, note });

/**
 * The thread's state. In order: archived; a fired reminder; done / failed; waiting on the user
 * (acceptance, status waiting, an open card, or one the caller knows about in `waiting`); queued;
 * paused; review under pr / auto (进行中, waiting on checks); running with someone working here
 * (进行中); running with small tasks still open (进行中, members handle them); then, with nothing
 * else going, by the lead's own state: offline or crashed (等你处理: nothing moves until the user
 * looks at the node), queued for a slot (进行中: it comes up by itself), waiting on someone else
 * (进行中); it has not spoken yet (进行中: reading the brief); else it replied and stopped (等你处理).
 */
export function threadPhase(t: PhaseInput, waiting?: Map<number, QuestionView>): ThreadPhase {
  if (t.status === "archived") return phase("archived", "archived", "archived", "");
  if (t.reminded_at) return phase("reminded", "waiting", "wait", "提醒时间到了，等你处理");
  if (t.status === "done") return phase("done", "done", "done", "");
  if (t.status === "failed") return phase("failed", "done", "failed", "");
  if (awaitsAcceptance(t)) return phase("acceptance", "waiting", "wait", t.merge_error ? "合并失败，等你处理" : "等你验收", "待验收");
  if (t.status === "waiting" || t.counts.waiting > 0 || waiting?.has(t.no)) return phase("asking", "waiting", "wait", liveLine(t) || "等你处理");
  if (t.status === "queued") return phase("queued", "active", "queued", t.queue_position ? `排队中，前面还有 ${t.queue_position} 个` : "排队等节点");
  if (t.status === "paused") return phase("paused", "idle", "paused", pauseLine(t.pause_reason));
  if (t.status === "review") return phase("checks", "active", "working", reviewLine(t));
  if (t.members.some(workingHere)) return phase("working", "active", "working", liveLine(t) || "进行中");
  if (openTasks(t) > 0) return phase("tasks", "active", "working", "等成员处理任务");
  const lead = t.members.find((m) => m.tier === "lead");
  switch (lead?.status?.state) {
    case "offline":
      return phase("lead_down", "waiting", "wait", "组长离线：节点不在线，节点上线后才能继续");
    case "broken":
      return phase("lead_down", "waiting", "wait", "组长的会话出错了，需要你看一下");
    case "queued":
      return phase("lead_queued", "active", "queued", lead && queueReason(lead) ? `等组长上线：${queueReason(lead)}` : "等组长上线（节点工位已满，排队中）");
    case "waiting":
    case "raising_hand":
      return phase("lead_waiting", "active", "working", "组长在等别人回复");
  }
  if (!t.lead_last) return phase("reading", "active", "working", "组长正在阅读任务");
  return phase("replied", "waiting", "wait", "组长已回复，等你回复");
}

/** The quiet line at the bottom of a thread's chat while it is going (null when there is nothing to say). */
export function phaseLine(t: PhaseInput): { text: string; spin: boolean } | null {
  const p = threadPhase(t);
  const lead = t.members.find((m) => m.tier === "lead");
  const leadLine = (): { text: string; spin: boolean } | null => {
    if (lead && isWaking(lead)) return { text: "正在唤醒组长…", spin: true };
    if (lead && isWorking(lead)) return { text: "组长正在回复…", spin: true };
    return null;
  };
  switch (p.id) {
    case "paused":
      return { text: "讨论串已暂停", spin: false };
    case "queued":
      return { text: "排队中，等节点空出来", spin: true };
    case "working":
      return leadLine() ?? { text: "成员正在干活…", spin: true };
    case "tasks":
    case "lead_waiting":
      return { text: p.note, spin: true };
    case "reading":
      return { text: "组长正在阅读任务…", spin: true };
    case "lead_queued":
      return { text: p.note, spin: true };
    case "lead_down":
      return { text: p.note, spin: false };
    case "checks":
      return { text: t.pr_number ? `PR #${t.pr_number} ${p.note}` : p.note, spin: true };
    case "replied":
      return { text: p.note, spin: false };
    case "asking":
    case "reminded":
      // The cards show in the chat themselves; only say something while the lead is still going.
      return t.status === "running" || t.status === "waiting" ? leadLine() : null;
    default:
      return null;
  }
}
