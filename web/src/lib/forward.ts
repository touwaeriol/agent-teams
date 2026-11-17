// Forwarding the user's own project-chat messages to a thread: the selection (shared by the
// chat column and the bar that replaces the composer), the picker's groups and search, and
// how a forwarded copy is drawn in the thread chat. Pure parts are unit-tested.
import { reactive } from "vue";
import type { MessageView, ThreadView } from "@/api/views";
import { groupThreads, type ListGroup } from "./threadList";

/** The most messages one forward carries (server: orchestrator.MaxForwardMessages). */
export const MAX_FORWARD = 20;

/** Only the user's own messages of the project chat can be forwarded. */
export function forwardable(m: Pick<MessageView, "role" | "kind">): boolean {
  return m.role === "user";
}

/** The selection: which conversation it belongs to and the chosen message ids. */
export interface Selection {
  cid: string;
  ids: number[];
  active: boolean;
}

export const selection = reactive<Selection>({ cid: "", ids: [], active: false });

/** Start selecting in a conversation, optionally with a first message. */
export function startSelecting(cid: string, id?: number): void {
  selection.cid = cid;
  selection.ids = id === undefined ? [] : [id];
  selection.active = true;
}

export function stopSelecting(): void {
  selection.ids = [];
  selection.active = false;
}

/** Toggle one id; the list stays sorted and never grows past MAX_FORWARD. */
export function toggled(ids: number[], id: number): number[] {
  if (ids.includes(id)) return ids.filter((x) => x !== id);
  if (ids.length >= MAX_FORWARD) return ids;
  return [...ids, id].sort((a, b) => a - b);
}

export function toggle(id: number): void {
  selection.ids = toggled(selection.ids, id);
}

export function isSelecting(cid: string | undefined): boolean {
  return selection.active && Boolean(cid) && selection.cid === cid;
}

/** The picker's groups: threads matching the search, grouped like the thread list; archived threads are shown but cannot be picked. */
export function pickerGroups(threads: ThreadView[], q: string): ListGroup[] {
  const needle = q.trim().toLowerCase();
  const hits = needle ? threads.filter((t) => `#${t.no} ${t.title}`.toLowerCase().includes(needle)) : threads;
  return groupThreads(hits);
}

export function pickable(t: Pick<ThreadView, "status">): boolean {
  return t.status !== "archived";
}

/** The default title of a thread opened from forwarded messages: the first line of the first one. */
export function titleFrom(messages: Pick<MessageView, "content">[]): string {
  for (const m of messages) {
    const line = m.content.split("\n").map((l) => l.trim()).find(Boolean);
    if (line) return [...line].slice(0, 40).join("");
  }
  return "转发的消息";
}

/** meta.forwarded_from of a copy in a thread chat. */
export interface ForwardedFrom {
  conversation_id: string;
  message_id: number;
  message_ids: number[];
  count: number;
  index: number;
  by: "user" | "coordinator";
  note?: string;
}

/** The grey line under the last copy of a forward: "已从项目对话转发 N 条消息" and where it links. */
export function forwardLine(m: Pick<MessageView, "meta">): { text: string; note: string; anchor: number } | null {
  const f = m.meta?.forwarded_from;
  if (!f || f.index !== f.count - 1) return null;
  const by = f.by === "coordinator" ? "（统筹转发）" : "";
  return { text: `已从项目对话转发 ${f.count} 条消息${by}`, note: f.note ?? "", anchor: f.message_ids[0] ?? f.message_id };
}

/** The DOM id of a message in the chat column (the forward line links to it). */
export const messageAnchor = (id: number | string) => `msg-${id}`;

/**
 * Where the forward line leads: the project chat at the first forwarded message. On the board
 * (thread open in the side panel) the thread stays open; from the full-page thread it goes back
 * to the board.
 */
export function backLink(routePath: string, pid: string, anchor: number | string): { path: string; query: { msg: string } } {
  const onBoard = new RegExp(`^/projects/${pid}(/threads/\\d+(/(?!full$)[a-z]+)?)?$`).test(routePath);
  return { path: onBoard ? routePath : `/projects/${pid}`, query: { msg: String(anchor) } };
}
