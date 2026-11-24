// Pure helpers of the project home (web/src/pages/Overview.tsx).
import type { QuestionView, RoleView, ThreadView } from "@/api/views";
import type { PhaseInput } from "@/lib/phase";
import { questionLine, threadKind, threadLook, type Look, type ThreadKind } from "@/lib/present";

export type Filter = "all" | "doing" | "wait" | "review" | "done";

export const FILTERS: [Filter, string, ThreadKind[]][] = [
  ["all", "全部", []],
  ["doing", "进行中", ["doing", "queued", "paused"]],
  ["wait", "等你", ["wait"]],
  ["review", "待验收", ["review"]],
  ["done", "已完成", ["done", "failed", "finished", "resolved"]],
];

export function filterThreads(threads: ThreadView[], f: Filter): ThreadView[] {
  const live = threads.filter((t) => t.status !== "archived");
  const kinds = FILTERS.find((x) => x[0] === f)?.[2] ?? [];
  return kinds.length ? live.filter((t) => kinds.includes(threadKind(t))) : live;
}

/** “组长 + 2 开发 + 1 测试” from the role templates. */
export function teamSummary(roles: RoleView[]): string {
  const members = roles.filter((r) => r.tier === "member").map((r) => `${r.count} ${r.title || r.role}`);
  return ["组长", ...members].join(" + ");
}

export function prText(t: Pick<ThreadView, "pr_number" | "status">): string {
  if (!t.pr_number) return "PR 未创建";
  return t.status === "done" ? `PR #${t.pr_number} 已合并` : `PR #${t.pr_number}`;
}

/** A card's pill: finished threads (done or failed) read 已完成. */
export function cardLook(t: PhaseInput): Look {
  return threadKind(t) === "done" ? { tone: "#2E7D4F", label: "已完成" } : threadLook(t);
}

/** "#12 · 权限请求" / "统筹 · dev-2 提问": where a waiting question comes from. */
export function waitingFrom(q: Pick<QuestionView, "thread_no" | "kind" | "asker">): string {
  const where = q.thread_no ? `#${q.thread_no}` : "统筹";
  return `${where} · ${q.kind === "approval" ? "权限请求" : q.asker.startsWith("lead") ? "组长提问" : `${q.asker} 提问`}`;
}

export function waitingText(q: QuestionView): string {
  return q.kind === "approval" ? questionLine(q).replace("请求执行", "想执行") : q.text;
}

export function reviewText(t: Pick<ThreadView, "pr_number" | "title">): string {
  return t.pr_number ? `PR #${t.pr_number} ${t.title}，等你验收` : `${t.title}，等你验收`;
}
