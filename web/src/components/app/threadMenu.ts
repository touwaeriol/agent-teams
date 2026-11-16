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

/** What the menu offers for a thread in this status. */
export function menuItems(t: Pick<ThreadView, "status">): { pause: boolean; resume: boolean; archive: boolean; unarchive: boolean } {
  return {
    pause: ["running", "waiting", "queued"].includes(t.status),
    resume: t.status === "paused",
    archive: t.status !== "archived",
    unarchive: t.status === "archived",
  };
}

