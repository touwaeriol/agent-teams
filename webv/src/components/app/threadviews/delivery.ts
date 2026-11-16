// Pure helpers of the 交付物 view (web/src/components/app/ThreadViews.tsx, Delivery).
import type { ThreadView } from "@/api/views";
import { checkLook, mergeLook, onePRLook, prLook, threadPRs, type Look } from "@/lib/present";
import { safeHref } from "@/lib/safeurl";

export interface DeliveryLine {
  key: string;
  branch: string;
  what: string;
  look: Look;
  href?: string;
}

/** What the 开 PR button does: nothing (finished or already open), open, or ask first (main tasks not all done). */
export function openPrMode(t: Pick<ThreadView, "status" | "pr_url" | "main_tasks">): "none" | "open" | "confirm" {
  if (["done", "failed", "archived"].includes(t.status) || t.pr_url) return "none";
  const allDone = t.main_tasks.length > 0 && t.main_tasks.every((m) => m.status === "done");
  return allDone ? "open" : "confirm";
}

interface QueueEntry {
  id: string;
  repo?: string | null;
  head_branch: string;
  employee: string;
  task_id: string;
  error?: string | null;
  status: string;
}

export function queueLines(thread: Pick<ThreadView, "branch">, queue: QueueEntry[]): DeliveryLine[] {
  return queue.map((e) => ({
    key: e.id,
    branch: `${e.repo ? `${e.repo} · ` : ""}${e.head_branch} → ${thread.branch}`,
    what: `${e.employee} · ${e.task_id}${e.error ? ` · ${e.error}` : ""}`,
    look: mergeLook(e.status),
  }));
}

/** One line per PR (per repo), or a placeholder line for the thread PR to come. */
export function prLines(thread: ThreadView): DeliveryLine[] {
  const prs = threadPRs(thread);
  if (prs.length === 0) return [{ key: "pr", branch: `${thread.branch || "讨论串分支"} → 基准分支`, what: "讨论串 PR", look: prLook(thread) }];
  return prs.map((p) => ({
    key: String(p.url ?? p.number),
    branch: `${p.repo ? `${p.repo} · ` : ""}${thread.branch} → ${p.base}`,
    what: `PR #${p.number}`,
    look: p.repo ? onePRLook(p) : prLook(thread),
    href: safeHref(p.url),
  }));
}

export { checkLook };
