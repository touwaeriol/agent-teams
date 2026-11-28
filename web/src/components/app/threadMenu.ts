import { $t } from "@/i18n";
import type { ThreadView } from "@/api/views";

/** Views of a thread that open over the chat from the ⋯ menu. */
export type ThreadViewId = "tasks" | "deliverables" | "members";

export const THREAD_VIEWS: [ThreadViewId, string][] = [
  ["tasks", $t("任务")],
  ["deliverables", $t("交付物")],
  ["members", $t("成员")],
];

export function archiveNotice(no: number) {
  return {
    title: $t("归档还在进行的 #{no}？", { no }),
    description: $t("归档会结束这个讨论串：员工的会话关掉，节点上的工作目录清理（有未推送改动的会保留）。之后可以取消归档，它会回到“已暂停”，点继续接着做。"),
  };
}

export function completeNotice(no: number) {
  return {
    title: $t("把 #{no} 标记为完成？", { no }),
    description: $t("讨论串会结束（已完成）：员工的会话关掉，没回答的卡片取消，协调员会收到通知。适合组长已经做完、但没有自己收尾的讨论串。有等待合并的 PR 时请先接受或拒绝。"),
  };
}

/** What the menu offers for a thread in this status. 解决 is `complete` (标记为完成). */
export function menuItems(t: Pick<ThreadView, "status"> & Partial<Pick<ThreadView, "pinned_at" | "remind_at" | "reminded_at">>): {
  complete: boolean;
  reopen: boolean;
  pause: boolean;
  resume: boolean;
  archive: boolean;
  unarchive: boolean;
  pin: boolean;
  unpin: boolean;
  remind: boolean;
  clearReminder: boolean;
} {
  return {
    complete: ["running", "waiting", "queued", "paused"].includes(t.status),
    reopen: t.status === "done" || t.status === "failed",
    pause: ["running", "waiting", "queued"].includes(t.status),
    resume: t.status === "paused",
    archive: t.status !== "archived",
    unarchive: t.status === "archived",
    pin: !t.pinned_at,
    unpin: Boolean(t.pinned_at),
    remind: t.status !== "archived",
    clearReminder: Boolean(t.remind_at || t.reminded_at),
  };
}

/** The thread's link to share (the panel route; narrow screens get the thread page there). */
export const threadLink = (t: Pick<ThreadView, "project_id" | "no">, origin: string = window.location.origin) =>
  `${origin}/projects/${t.project_id}/threads/${t.no}`;

/** The full-page route of a thread (在新窗口中打开 / 全屏打开). */
export const threadFullPath = (t: Pick<ThreadView, "project_id" | "no">) => `/projects/${t.project_id}/threads/${t.no}/full`;
