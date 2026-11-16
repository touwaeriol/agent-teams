import type { NodeView } from "@/api/views";

/** Slots of a node: configured max, else what the daemon reported. */
export function nodeSlots(n: Pick<NodeView, "max_active_sessions" | "capacity">): number {
  if (n.max_active_sessions) return n.max_active_sessions;
  const reported = (n.capacity as { max_active_sessions?: unknown })?.max_active_sessions;
  return typeof reported === "number" ? reported : 0;
}

export interface NodeSummary {
  total: number;
  online: number;
  active: number;
  slots: number;
  queued: number;
}

export function summarizeNodes(list: NodeView[]): NodeSummary {
  const online = list.filter((n) => n.online);
  return {
    total: list.length,
    online: online.length,
    active: online.reduce((s, n) => s + n.active_sessions, 0),
    slots: online.reduce((s, n) => s + nodeSlots(n), 0),
    queued: online.reduce((s, n) => s + n.queued, 0),
  };
}
