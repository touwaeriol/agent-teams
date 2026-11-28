// Pure helpers of the project home (web/src/pages/Overview.tsx).
import { $t } from "@/i18n";
import type { QuestionView, RoleView, ThreadView } from "@/api/views";
import type { PhaseInput } from "@/lib/phase";
import { approvalCommand, threadKind, threadLook, type Look, type ThreadKind } from "@/lib/present";

export type Filter = "all" | "doing" | "wait" | "review" | "done";

export const FILTERS: [Filter, string, ThreadKind[]][] = [
  ["all", $t("全部"), []],
  ["doing", $t("进行中"), ["doing", "queued", "paused"]],
  ["wait", $t("等你"), ["wait"]],
  ["review", $t("待验收"), ["review"]],
  ["done", $t("已完成"), ["done", "failed", "finished", "resolved"]],
];

export function filterThreads(threads: ThreadView[], f: Filter): ThreadView[] {
  const live = threads.filter((t) => t.status !== "archived");
  const kinds = FILTERS.find((x) => x[0] === f)?.[2] ?? [];
  return kinds.length ? live.filter((t) => kinds.includes(threadKind(t))) : live;
}

/** “组长 + 2 开发 + 1 测试” from the role templates. */
export function teamSummary(roles: RoleView[]): string {
  const members = roles.filter((r) => r.tier === "member").map((r) => `${r.count} ${r.title || r.role}`);
  return [$t("组长"), ...members].join(" + ");
}

export function prText(t: Pick<ThreadView, "pr_number" | "status">): string {
  if (!t.pr_number) return $t("PR 未创建");
  return t.status === "done" ? $t("PR #{pr_number} 已合并", { pr_number: t.pr_number }) : `PR #${t.pr_number}`;
}

/** A card's pill: finished threads (done or failed) read 已完成. */
export function cardLook(t: PhaseInput): Look {
  return threadKind(t) === "done" ? { tone: "#2E7D4F", label: $t("已完成") } : threadLook(t);
}

/** "#12 · 权限请求" / "统筹 · dev-2 提问": where a waiting question comes from. */
export function waitingFrom(q: Pick<QuestionView, "thread_no" | "kind" | "asker">): string {
  const where = q.thread_no ? `#${q.thread_no}` : $t("统筹");
  return $t("{where} · {n}", { where, n: q.kind === "approval" ? $t("权限请求") : q.asker.startsWith("lead") ? $t("组长提问") : $t("{asker} 提问", { asker: q.asker }) });
}

export function waitingText(q: QuestionView): string {
  return q.kind === "approval" ? $t("{asker} 想执行 {q}", { asker: q.asker, q: approvalCommand(q) }) : q.text;
}

export function reviewText(t: Pick<ThreadView, "pr_number" | "title">): string {
  return t.pr_number ? $t("PR #{pr_number} {title}，等你验收", { pr_number: t.pr_number, title: t.title }) : $t("{title}，等你验收", { title: t.title });
}
