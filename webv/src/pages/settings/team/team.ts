// Team page helpers (pure): the draft list edits and the node picker's summary.
import type { NodeView, RoleView } from "@/api/views";

export const GATES = [
  { value: "none", label: "普通岗位" },
  { value: "review", label: "审查关（合并前审查）" },
  { value: "decision", label: "决策关（方案取舍）" },
];

export const patchAt = (list: RoleView[], i: number, patch: Partial<RoleView>): RoleView[] => list.map((r, j) => (j === i ? { ...r, ...patch } : r));

export const removeAt = (list: RoleView[], i: number): RoleView[] => list.filter((_, j) => j !== i);

/** 任意节点 / the picked node names (+ how many picked nodes were deleted). */
export function nodePickerText(value: string[], nodes: Pick<NodeView, "id" | "name">[]): string {
  if (value.length === 0) return "任意节点";
  const names = nodes.filter((n) => value.includes(n.id)).map((n) => n.name);
  const missing = value.length - names.length;
  return [...names, ...(missing > 0 ? [`${missing} 个已删除`] : [])].join("、");
}

export const toggleId = (value: string[], id: string): string[] => (value.includes(id) ? value.filter((x) => x !== id) : [...value, id]);

/** The draft differs from what was saved (both in display order). */
export const isDirty = (draft: RoleView[], saved: RoleView[]): boolean => JSON.stringify(draft) !== JSON.stringify(saved);

/** PUT /api/roles body: the draft in its display order. */
export const ordered = (draft: RoleView[]): RoleView[] => draft.map((r, i) => ({ ...r, sort: i }));

/** Plugin version pick: "最新 v3" (following latest) or "v2" / "v2（已清理）" (pinned). */
export function versionText(pinned: number, latest: number, gone: boolean): string {
  return pinned ? `v${pinned}${gone ? "（已清理）" : ""}` : `最新 v${latest}`;
}
