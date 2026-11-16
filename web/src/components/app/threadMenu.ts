import type { ThreadView } from "@/api/views";

/** Views of a thread that open over the chat from the ⋯ menu. */
export type ThreadViewId = "tasks" | "deliverables" | "members";

export const THREAD_VIEWS: [ThreadViewId, string][] = [
  ["tasks", "任务"],
  ["deliverables", "交付物"],
  ["members", "成员"],
];

export function archiveNotice(no: number) {
  return {
    title: `归档还在进行的 #${no}？`,
    description: "归档会结束这个讨论串：员工的会话关掉，节点上的工作目录清理（有未推送改动的会保留）。之后可以取消归档，它会回到“已暂停”，点继续接着做。",
  };
}

export function completeNotice(no: number) {
  return {
    title: `把 #${no} 标记为完成？`,
    description: "讨论串会结束（已完成）：员工的会话关掉，没回答的卡片取消，协调员会收到通知。适合组长已经做完、但没有自己收尾的讨论串。有等待合并的 PR 时请先接受或拒绝。",
  };
}

/** What the menu offers for a thread in this status. */
export function menuItems(t: Pick<ThreadView, "status">): {
  complete: boolean;
  reopen: boolean;
  pause: boolean;
  resume: boolean;
  archive: boolean;
  unarchive: boolean;
} {
  return {
    complete: ["running", "waiting", "queued", "paused"].includes(t.status),
    reopen: t.status === "done" || t.status === "failed",
    pause: ["running", "waiting", "queued"].includes(t.status),
    resume: t.status === "paused",
    archive: t.status !== "archived",
    unarchive: t.status === "archived",
  };
}

