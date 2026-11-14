// How domain states map to colors and words, in one place.
import type { NodeInfo, TaskStatus } from "@/lib/api";
import type { Tone } from "./data";

export const TASK_STATUS: Record<TaskStatus, { label: string; tone: Tone }> = {
  pending: { label: "排队", tone: "warn" },
  running: { label: "进行中", tone: "info" },
  done: { label: "完成", tone: "ok" },
  failed: { label: "失败", tone: "bad" },
  cancelled: { label: "已取消", tone: "neutral" },
};

export function nodeStatus(node: NodeInfo | undefined): { label: string; tone: Tone } {
  if (!node) return { label: "未知节点", tone: "neutral" };
  if (!node.online) return { label: "离线", tone: "bad" };
  const busy = node.active?.length ?? 0;
  if (node.max_runs && busy >= node.max_runs) return { label: "在线 · 槽位已满", tone: "warn" };
  return { label: "在线", tone: "ok" };
}
