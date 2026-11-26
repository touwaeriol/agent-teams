// 定时任务的 schedule 项目事件：负责人用 schedule_cancel 取消、讨论串结束自动取消、页面上删除时，
// 网页都重新拉定时任务列表（连同运行历史），讨论串输入框上方的待触发唤醒随之消失，不用手动点 ✕。
import { QueryClient } from "@tanstack/vue-query";
import { describe, expect, it, vi } from "vitest";
import { applyProjectEvent, PROJECT_EVENTS } from "@/api/events";
import { keys } from "@/api/queries";

describe("schedule events", () => {
  it("are subscribed to and refetch the project's schedules", () => {
    expect(PROJECT_EVENTS).toContain("schedule");
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    applyProjectEvent(qc, "p1", "schedule", { id: "sc_1", thread_id: "t1" });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.schedules("p1") });
  });
});
