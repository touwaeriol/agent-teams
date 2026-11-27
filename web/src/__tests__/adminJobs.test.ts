// @vitest-environment jsdom
// 管理后台 · 后台任务: the jobs with their state, run now, pause / resume, the runs of a job, and
// the helpers behind the columns.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { durationText, intervalText, jobState } from "@/pages/admin/admin";
import Jobs from "@/pages/admin/Jobs.vue";

const JOB = {
  name: "blobs", title: "附件存储清理", description: "删除待删的对象", scope: "single", default_seconds: 60, interval_seconds: 60,
  override: false, paused: false, running: false, run_requested: false, last_started_at: "2026-11-27T10:00:00Z",
  last_finished_at: "2026-11-27T10:00:01.5Z", last_ok: false, last_summary: "", last_error: "bucket gone", last_replica: "box/1",
  next_run_at: "2026-11-27T10:01:00Z", runs: 7, failures: 1,
};
const RUNS = [{ id: 1, replica: "box/1", trigger: "manual", started_at: "2026-11-27T10:00:00Z", finished_at: "2026-11-27T10:00:01Z", ok: true, summary: "删除 3（失败 0）", error: "" }];

function api() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const url = new URL(req.url);
    let body: unknown = [JOB];
    if (url.pathname.endsWith("/runs")) body = RUNS;
    else if (req.method !== "GET") body = { ...JOB, paused: req.method === "PATCH" };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
}

function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(Jobs, { global: { plugins: [[VueQueryPlugin, { queryClient }], router, ElementPlus] as never[] }, attachTo: document.body });
}

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const requests = (spy: ReturnType<typeof api>) => spy.mock.calls.map((c) => c[0] as Request).map((r) => `${r.method} ${new URL(r.url).pathname}`);

describe("后台任务 page", () => {
  it("shows a job with its last failure and controls it", async () => {
    const spy = api();
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("附件存储清理");
    expect(w.text()).toContain("上次失败");
    expect(w.text()).toContain("bucket gone");
    expect(w.text()).toContain("每 1 分钟");
    const button = (label: string) => w.findAll("button").find((b) => b.text() === label)!;
    await button("立即运行").trigger("click");
    await button("暂停").trigger("click");
    await flushPromises();
    expect(requests(spy)).toContain("POST /api/admin/jobs/blobs/run");
    expect(requests(spy)).toContain("PATCH /api/admin/jobs/blobs");
    await button("查看运行记录").trigger("click");
    await flushPromises();
    expect(w.find("[data-runs]").text()).toContain("手动");
    expect(w.find("[data-runs]").text()).toContain("删除 3（失败 0）");
  });
});

describe("job helpers", () => {
  it("words intervals, durations and states", () => {
    expect([intervalText(15), intervalText(60), intervalText(7200), intervalText(86400), intervalText(90)]).toEqual(["每 15 秒", "每 1 分钟", "每 2 小时", "每天", "每 90 秒"]);
    expect(durationText("2026-01-01T00:00:00Z", "2026-01-01T00:00:00.4Z")).toBe("0.4 秒");
    expect(durationText("2026-01-01T00:00:00Z", "2026-01-01T00:03:05Z")).toBe("3 分 5 秒");
    const base = { running: false, paused: false, run_requested: false, last_ok: true, last_started_at: "x" };
    expect(jobState(base).label).toBe("正常");
    expect(jobState({ ...base, paused: true }).label).toBe("已暂停");
    expect(jobState({ ...base, running: true, paused: true }).label).toBe("运行中");
    expect(jobState({ ...base, last_started_at: null }).label).toBe("还没运行");
    expect(jobState({ ...base, last_ok: false }).label).toBe("上次失败");
  });
});
