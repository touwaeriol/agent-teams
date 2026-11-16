// Pure helpers of the thread panel / page (web/src/components/app/ThreadDetail.tsx).
import type { ThreadView } from "@/api/views";
import { isWaking, isWorking, threadActive } from "@/lib/live";
import { leadOf } from "@/lib/present";
import { THREAD_VIEWS, type ThreadViewId } from "../threadMenu";

/** The view a URL segment names (/threads/:no/:tab), or none (just the chat). */
export function detailViewOf(value: unknown): ThreadViewId | null {
  return THREAD_VIEWS.find(([id]) => id === value)?.[0] ?? null;
}

/** The quiet line at the bottom of a thread's chat while it is going. */
export function typingLine(thread: Pick<ThreadView, "status" | "members">): { text: string; spin: boolean } | null {
  if (!threadActive(thread)) return null;
  if (thread.status === "paused") return { text: "讨论串已暂停", spin: false };
  if (thread.status === "queued") return { text: "排队中，等节点空出来", spin: true };
  const lead = leadOf(thread);
  if (lead && isWaking(lead)) return { text: "正在唤醒组长…", spin: true };
  if (lead && isWorking(lead)) return { text: "组长正在回复…", spin: true };
  if (thread.status === "running") return { text: "讨论串进行中", spin: true };
  return null;
}

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
