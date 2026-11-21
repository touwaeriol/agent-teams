// How domain states look on screen (colours and words of the design canvas),
// plus the small derivations pages share. Pure functions, unit-tested.
import type { EmployeeBrief, QuestionView, TaskStatus, TaskView, ThreadView } from "@/api/views";
import { awaitsAcceptance, pauseLine, reviewLine, threadPhase, type PhaseId, type PhaseInput } from "./phase";
import { RUNTIME_SPECS } from "./runtimes";

export { awaitsAcceptance, pauseLine } from "./phase";

export const INK = {
  text: "#1F1E1C",
  muted: "#6B6862",
  faint: "#8A877F",
  line: "#E7E5E0",
  wait: "#C2570C",
  doing: "#2F6FDB",
  review: "#7A4FD1",
  done: "#2E7D4F",
  gray: "#8A877F",
  idle: "#B7B3AA",
  bad: "#B42318",
  decision: "#4A4843",
  merge: "#2F8F9D",
} as const;

export interface Look {
  tone: string;
  label: string;
  /** In progress: drawn as a rotating spinner instead of a static dot. */
  spin?: boolean;
}

export type ThreadKind = "wait" | "doing" | "queued" | "review" | "paused" | "done" | "failed" | "archived";

const KIND_OF: Record<PhaseId, ThreadKind> = {
  archived: "archived",
  done: "done",
  failed: "failed",
  reminded: "wait",
  acceptance: "review",
  asking: "wait",
  replied: "wait",
  queued: "queued",
  working: "doing",
  tasks: "doing",
  reading: "doing",
  lead_queued: "queued",
  lead_waiting: "doing",
  lead_down: "wait",
  checks: "doing",
  paused: "paused",
};

/**
 * A thread's state for marks, chips and the overview / coordinator cards: threadPhase's, the same
 * one the panel list and the open thread use (an open card or approval, a fired reminder, or the
 * lead having replied with nothing left going all make it 等你处理).
 */
export const threadKind = (t: PhaseInput): ThreadKind => KIND_OF[threadPhase(t).id];

export const THREAD_LOOK: Record<ThreadKind, Look> = {
  wait: { tone: INK.wait, label: "等你处理" },
  doing: { tone: INK.doing, label: "进行中", spin: true },
  queued: { tone: INK.gray, label: "排队中" },
  review: { tone: INK.review, label: "待验收" },
  paused: { tone: INK.gray, label: "已暂停" },
  done: { tone: INK.done, label: "已完成" },
  failed: { tone: INK.bad, label: "失败" },
  archived: { tone: INK.gray, label: "已归档" },
};

export const threadLook = (t: PhaseInput): Look => THREAD_LOOK[threadKind(t)];

/** List marks and chips: anything waiting for the user (approvals too) shows in the 等你 colour. */
export function markTone(t: PhaseInput): string {
  const kind = threadKind(t);
  return kind === "done" ? INK.gray : THREAD_LOOK[kind].tone;
}

/** The acceptance card shows while the thread waits on the user's acceptance (pr_state accepting keeps it). */
export const showReviewCard = (t: Pick<ThreadView, "status" | "merge_policy" | "merge_error" | "pr_state">): boolean =>
  awaitsAcceptance(t) || (t.status === "review" && t.pr_state === "accepting");

/** One line under a thread title: what is waiting, else the PR, else what the lead said last. */
export function threadLine(t: ThreadView, open?: QuestionView): string {
  if (open) return questionLine(open);
  if (t.status === "review" && t.pr_number && t.pr_state === "accepting") return `PR #${t.pr_number} 合并中`;
  if (t.status === "review" && t.pr_number && t.merge_error) return `PR #${t.pr_number} 合并失败，等你处理`;
  if (t.status === "review" && t.pr_number) return `PR #${t.pr_number} ${reviewLine(t)}`;
  if (t.status === "done" && t.pr_number) return mergedLine(t);
  if (t.status === "queued") return t.queue_position ? `排队中，前面还有 ${t.queue_position} 个` : "排队等节点";
  if (t.status === "paused") return pauseLine(t.pause_reason);
  return t.lead_last?.text || t.summary || t.brief;
}

export function questionLine(q: QuestionView): string {
  if (q.kind === "approval") return `${q.asker} 请求执行 ${approvalCommand(q)}`;
  return `${q.asker.startsWith("lead") ? "组长" : q.asker}问：${q.text}`;
}

/** Why a card reached the user although the lead reviews first (null: it came straight to the user). */
export function reviewNote(q: Pick<QuestionView, "detail">): string | null {
  const r = q.detail.review;
  if (!r || r.decision !== "ask_user") return null;
  if (r.by.startsWith("lead:")) return `组长 ${r.by.slice(5)} 拿不准：${r.reason}`;
  return `组长 ${q.detail.reviewer ?? ""} 没有及时判断，转给你`;
}

/** The status in the head of a user's approval card: handed over by the lead, or plain. */
export function approvalCardStatus(q: Pick<QuestionView, "detail">): string {
  return reviewNote(q) ? "组长转给你" : "等你处理";
}

/** The command or call an approval card shows in its code block. */
export function approvalCommand(q: Pick<QuestionView, "detail" | "text">): string {
  const input = q.detail.input ?? {};
  const cmd = input.command ?? input.cmd ?? input.file_path ?? input.path;
  if (typeof cmd === "string" && cmd) return cmd;
  if (q.detail.title) return q.detail.title;
  if (q.detail.tool_name && Object.keys(input).length) return `${q.detail.tool_name} ${JSON.stringify(input)}`;
  return q.detail.tool_name || q.text;
}

export interface Progress {
  done: number;
  total: number;
  pct: string;
  text: string;
}

export function mainProgress(t: Pick<ThreadView, "counts" | "main_tasks">): Progress {
  const total = t.counts.main_total || t.main_tasks.length;
  const done = t.counts.main_total ? t.counts.main_done : t.main_tasks.filter((m) => m.status === "done").length;
  const pct = total ? `${Math.round((done / total) * 100)}%` : "0%";
  return { done, total, pct, text: `${done} / ${total}` };
}

export function leadOf(t: Pick<ThreadView, "members">): EmployeeBrief | undefined {
  return t.members.find((m) => m.tier === "lead");
}

export function teamText(t: Pick<ThreadView, "members">): string {
  return t.members.filter((m) => m.tier === "member").map((m) => m.name).join("、") || "—";
}

const TASK_LOOK: Record<TaskStatus, Look> = {
  pending: { tone: INK.gray, label: "待领取" },
  claimed: { tone: INK.doing, label: "已领取", spin: true },
  running: { tone: INK.doing, label: "进行中", spin: true },
  blocked: { tone: INK.wait, label: "等依赖" },
  review: { tone: INK.review, label: "待审查" },
  decision: { tone: INK.decision, label: "待决策" },
  merging: { tone: INK.merge, label: "合并中", spin: true },
  done: { tone: INK.done, label: "完成" },
  failed: { tone: INK.bad, label: "失败" },
  cancelled: { tone: INK.gray, label: "已取消" },
};

/** A pending task whose dependencies are not done yet shows as 等依赖. */
export function taskLook(task: Pick<TaskView, "status" | "depends_on">, all: Pick<TaskView, "id" | "status">[] = []): Look {
  if (task.status === "pending" && task.depends_on.some((d) => all.find((t) => t.id === d)?.status !== "done")) return TASK_LOOK.blocked;
  return TASK_LOOK[task.status] ?? { tone: INK.gray, label: task.status };
}

const STATE_TEXT: Record<string, string> = {
  typing: "写代码",
  reading: "看代码",
  running: "跑命令",
  thinking: "思考中",
  walking_to_board: "去任务板",
  claiming: "领任务",
  returning: "回工位",
  raising_hand: "等你处理",
  idle: "空闲",
  waiting: "等待中",
  queued: "排队等工位",
  offline: "离线",
  broken: "出错",
};

export function memberTone(m: Pick<EmployeeBrief, "status" | "node"> & { busy_elsewhere?: boolean }): string {
  if (m.busy_elsewhere) return INK.gray;
  const s = m.status?.state ?? "idle";
  if (s === "raising_hand") return INK.wait;
  if (s === "waiting") return INK.review;
  if (s === "offline" || s === "broken") return INK.bad;
  if (s === "idle" || s === "queued") return INK.idle;
  return INK.doing;
}

/** “在 #12 忙” for a shared member working in another thread ("" otherwise). */
export function busyElsewhereText(m: Pick<EmployeeBrief, "busy_elsewhere" | "thread_no">): string {
  if (!m.busy_elsewhere) return "";
  return m.thread_no ? `在 #${m.thread_no} 忙` : "在别的讨论串忙";
}

export function memberDoing(m: Pick<EmployeeBrief, "status">): string {
  const s = m.status;
  if (!s) return "空闲";
  if (s.state === "thinking" && s.substate === "waking") return s.bubble || "唤醒中";
  return s.bubble || STATE_TEXT[s.state] || s.state;
}

export const RUNTIME_LABEL: Record<string, string> = Object.fromEntries(RUNTIME_SPECS.map((r) => [r.id, r.label]));

export const TIER_LABEL: Record<string, string> = { coordinator: "统筹", lead: "组长", member: "成员" };

/** Questions still waiting for the user, oldest first. */
export function openForUser(list: QuestionView[] | undefined): QuestionView[] {
  return (list ?? []).filter((q) => q.status === "open" && q.audience === "user").sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
}

export const DELIVERABLE_KINDS: Record<string, { label: string; glyph: string; bg: string; fg: string; block: string }> = {
  design: { label: "设计稿", glyph: "◐", bg: "#EEE9FB", fg: "#6A45C2", block: "#FFF4EC" },
  doc: { label: "文档", glyph: "≡", bg: "#E5EEFB", fg: "#2F6FDB", block: "#F4F3F0" },
  slides: { label: "幻灯片", glyph: "▭", bg: "#FBE9DF", fg: "#B4530B", block: "#FBE9DF" },
  dashboard: { label: "数据看板", glyph: "▥", bg: "#FBE7E6", fg: "#B42318", block: "#EAF0FB" },
  board: { label: "白板", glyph: "◇", bg: "#FBE6F0", fg: "#A2346B", block: "#FBE6F0" },
  data: { label: "数据", glyph: "▦", bg: "#FBEDEC", fg: "#B42318", block: "#EAF0FB" },
};

export function deliverableKind(kind: string) {
  return DELIVERABLE_KINDS[kind] ?? { label: "链接", glyph: "↗", bg: "#F1EFEA", fg: "#4A4843", block: "#F4F3F0" };
}

/** Soft background and ink for a project's initial tile in the sidebar, stable per name. */
const TINTS: [string, string][] = [
  ["#F4E3D7", "#A24E1E"],
  ["#E3ECFA", "#1F4E9E"],
  ["#E6F2EA", "#1F5E3A"],
  ["#EEE9FB", "#5B3BA8"],
  ["#FBE6F0", "#A2346B"],
  ["#F1EFEA", "#4A4843"],
];

export function projectTint(name: string): [string, string] {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.codePointAt(0)!) >>> 0;
  return TINTS[h % TINTS.length];
}

const MERGE_LOOK: Record<string, Look> = {
  queued: { tone: INK.gray, label: "排队" },
  merging: { tone: INK.merge, label: "合并中", spin: true },
  ci_running: { tone: INK.doing, label: "跑 CI" },
  conflict: { tone: INK.wait, label: "有冲突" },
  ci_failed: { tone: INK.bad, label: "CI 失败" },
  merged: { tone: INK.done, label: "已合并" },
  cancelled: { tone: INK.gray, label: "已取消" },
};

export function mergeLook(status: string): Look {
  return MERGE_LOOK[status] ?? { tone: INK.gray, label: status };
}

export function checkLook(c: { status: string; conclusion: string | null }): Look {
  if (c.status === "queued") return { tone: INK.doing, label: "排队" };
  if (c.status !== "completed") return { tone: INK.doing, label: "运行中", spin: true };
  if (c.conclusion === "success" || c.conclusion === "neutral" || c.conclusion === "skipped") return { tone: INK.done, label: "通过" };
  return { tone: INK.bad, label: c.conclusion ?? "失败" };
}

/** The final PR row of the delivery tab (several PRs: one per changed repository). */
export function prLook(t: Pick<ThreadView, "pr_number" | "pr_state" | "status" | "repos"> & { pr_url?: string | null }): Look {
  if (!t.pr_number) return { tone: INK.gray, label: "全部主要任务完成后开（每个有改动的仓库一个）" };
  const n = threadPRs({ ...t, pr_url: t.pr_url ?? null }).length;
  const what = n > 1 ? `${n} 个 PR` : `PR #${t.pr_number}`;
  if (t.pr_state === "merged" || t.status === "done") return { tone: INK.done, label: `${what} 已合并` };
  if (t.pr_state === "closed") return { tone: INK.bad, label: `${what} 有已关闭的` };
  return { tone: INK.review, label: `${what} 等验收` };
}

/** One repository's PR row: its own state, not the thread's summary. */
export function onePRLook(p: Pick<ThreadPR, "state">): Look {
  if (p.state === "merged") return { tone: INK.done, label: "已合并" };
  if (p.state === "closed") return { tone: INK.bad, label: "已关闭" };
  if (p.state === "accepting") return { tone: INK.doing, label: "合并中" };
  return { tone: INK.review, label: "等验收" };
}

export interface ThreadPR {
  repo: string;
  number: number;
  url: string | null;
  base: string;
  state: string;
}

/** A thread's pull requests, one per repository; threads from before that carry only pr_number/pr_url. */
export function threadPRs(t: Pick<ThreadView, "pr_number" | "pr_url" | "pr_state" | "repos"> & { base_branch?: string }): ThreadPR[] {
  const prs = (t.repos ?? []).filter((r) => r.pr_number).map((r) => ({ repo: r.repo, number: r.pr_number as number, url: r.pr_url, base: r.base_branch, state: r.pr_state }));
  if (prs.length > 0 || !t.pr_number) return prs;
  return [{ repo: "", number: t.pr_number, url: t.pr_url, base: t.base_branch ?? "", state: t.pr_state ?? "" }];
}

/** Where the thread's work lands: "o/web → dev、o/r → main", else the legacy single base, else "". */
export function threadBases(t: Pick<ThreadView, "base_branch" | "repos">): string {
  const bases = (t.repos ?? []).filter((r) => r.base_branch).map((r) => `${r.repo} → ${r.base_branch}`);
  return bases.length > 0 ? bases.join("、") : t.base_branch;
}

/** A finished thread's PRs: merged, or (merge_policy pr) still open for a person to merge on GitHub. */
function mergedLine(t: ThreadView): string {
  const prs = threadPRs(t);
  if (t.pr_state === "open") return prs.length > 1 ? `${prs.length} 个 PR 已提交，等合并` : `PR #${t.pr_number} 已提交，等合并`;
  if (prs.length > 1) return `${prs.length} 个 PR 已合并`;
  const base = prs[0]?.base || t.base_branch;
  return base ? `PR #${t.pr_number} 已合并到 ${base}` : `PR #${t.pr_number} 已合并`;
}

/** First line of a task instruction, as its title. */
export function taskTitle(t: Pick<TaskView, "instruction" | "summary">): string {
  return (t.instruction.split("\n")[0] || t.summary || "").trim();
}
