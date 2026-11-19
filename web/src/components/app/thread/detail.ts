// Pure helpers of the thread panel / page (web/src/components/app/ThreadDetail.tsx).
import type { MessageView, ThreadView } from "@/api/views";
import { phaseLine, type PhaseInput } from "@/lib/phase";
import { neighbours } from "@/lib/threadList";
import { THREAD_VIEWS, type ThreadViewId } from "../threadMenu";

/** The view a URL segment names (/threads/:no/:tab), or none (just the chat). */
export function detailViewOf(value: unknown): ThreadViewId | null {
  return THREAD_VIEWS.find(([id]) => id === value)?.[0] ?? null;
}

/**
 * The quiet line at the bottom of a thread's chat while it is going: from threadPhase, so it says
 * what the list and the header say (组长已回复，等你回复 without a spinner once the lead stopped and
 * nothing else is going; 等成员处理任务 while small tasks are open; 组长正在阅读任务 before its first word).
 */
export const typingLine = (thread: PhaseInput): { text: string; spin: boolean } | null => phaseLine(thread);

/** What replaces the message box: only archived threads refuse messages (with 取消归档). */
export function closedNote(t: Pick<ThreadView, "status">): { text: string; unarchive: boolean } | null {
  if (t.status !== "archived") return null;
  return { text: "讨论串已归档，取消归档后再继续", unarchive: true };
}

/** The quiet hint above the message box of a done or failed thread. */
export function reopenHint(t: Pick<ThreadView, "status">): string | null {
  return t.status === "done" || t.status === "failed" ? "发送消息会重新打开讨论串" : null;
}

/** Expand / collapse button of the detail header. */
export interface ExpandLink {
  to: string;
  label: string;
  collapse?: boolean;
}

/** Where 上一个 / 下一个讨论串 go (null at either end of the list). */
export interface ThreadNav {
  prev: string | null;
  next: string | null;
}

/** The neighbours of `no` in `order`, as routes made by `path`. */
export function threadNav(order: number[], no: number, path: (no: number) => string): ThreadNav {
  const n = neighbours(order, no);
  return { prev: n.prev === null ? null : path(n.prev), next: n.next === null ? null : path(n.next) };
}

/** Who handed the brief to the lead: the user when they opened the thread, else the coordinator. */
export function dispatchSpeaker(openedBy: string): string {
  return openedBy === "user" ? "你 → 组长" : "统筹 → 组长";
}

/** Lines a collapsed brief shows. */
export const BRIEF_LINES = 6;

/** A brief long enough to fold to its first lines (more lines than shown, or one long paragraph). */
export function briefFoldable(brief: string): boolean {
  const text = brief.trim();
  return text.split("\n").length > BRIEF_LINES || text.length > 320;
}

/** The empty state of a thread's chat: shown until the lead's first message. */
export function leadReading(messages: Pick<MessageView, "role">[], olderPages: boolean): boolean {
  return !olderPages && !messages.some((m) => m.role === "leader");
}
