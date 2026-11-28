import { $t } from "@/i18n";
import type { EmployeeBrief } from "@/api/views";

/** "正在编辑 · 2 分钟 · 任务 t5": what a member is doing, for how long, on which task. */
export function memberStatusLine(label: string, status: EmployeeBrief["status"], since: (iso: string) => string): string {
  return $t("{label}{n}{n2}", { label, n: status?.since ? ` · ${since(status.since)}` : "", n2: status?.task_id ? $t(" · 任务 {task_id}", { task_id: status.task_id }) : "" });
}
