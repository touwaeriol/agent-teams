// Relay cards (meta.relay): messages between threads and with the coordinator, a subthread's
// report to its parent, an opened subthread. The header reads like the Claude app's
// "已向协调者发送 1 条消息" / "来自 <讨论串> 的 1 条消息". Pure and unit-tested.
import type { MessageView, RelayMeta } from "@/api/views";

const STATUS: Record<string, string> = { waiting: "等用户处理", review: "待验收", done: "已完成", failed: "失败", running: "进行中" };

const peerName = (r: RelayMeta) => (r.peer === "coordinator" ? "项目统筹" : `#${r.no ?? "?"} ${r.title ?? ""}`.trim());

/** The card's one-line header. */
export function relayHeader(r: RelayMeta): string {
  if (r.kind === "opened") {
    if (r.sub_no != null) return `#${r.no} 开了子讨论串 #${r.sub_no} ${r.sub_title ?? ""}`.trim();
    return `开了子讨论串 ${peerName(r)}`;
  }
  if (r.kind === "report") return `子讨论串 ${peerName(r)} · ${STATUS[r.status ?? ""] ?? r.status ?? ""}`.trim();
  return r.dir === "out" ? `已向 ${peerName(r)} 发送 1 条消息` : `来自 ${peerName(r)} 的 1 条消息`;
}

/** The thread the card links to ("查看讨论串"): the peer, or the opened subthread; null for the coordinator. */
export function relayThreadNo(r: RelayMeta): number | null {
  if (r.kind === "opened" && r.sub_no != null) return r.sub_no;
  return r.peer === "thread" && r.no != null ? r.no : null;
}

/** Whether the card has a body to unfold (an opened subthread has none). */
export const relayHasBody = (m: Pick<MessageView, "content" | "meta">): boolean => m.meta.relay?.kind !== "opened" && Boolean(m.content.trim());
