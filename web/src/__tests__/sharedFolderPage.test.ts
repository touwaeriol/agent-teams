// @vitest-environment jsdom
// 共享文件夹 settings: on by default (no switch-on, no delete), retry of a failed folder, status.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import SharedFolder from "@/pages/settings/SharedFolder.vue";

const EMPTY = { configured: true, project_dir: "projects/{project_id}", shared_dir: "shared" };
const READY = {
  ...EMPTY,
  volume: { bucket: "agent-teams-fs", prefix: "users/0123456789abcdef/", read_only: false, state: "ready", error: "", capacity_gb: 0, created_at: "", updated_at: "" },
};

function api(get: unknown) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const body = req.method === "POST" ? READY : get;
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
}

function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(SharedFolder, { global: { plugins: [[VueQueryPlugin, { queryClient }], router, ElementPlus] as never[] }, attachTo: document.body });
}

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const FAILED = { ...EMPTY, volume: { ...READY.volume, state: "error", error: "connection refused" } };

describe("SharedFolder page", () => {
  it("says the folder is being switched on by itself", async () => {
    api(EMPTY);
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("正在自动开通");
    expect(w.findAll("button").some((b) => b.text() === "开通")).toBe(false);
  });

  it("retries a failed folder", async () => {
    const spy = api(FAILED);
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("connection refused");
    await w.findAll("button").find((b) => b.text() === "重试")!.trigger("click");
    await flushPromises();
    expect(spy.mock.calls.map((c) => c[0] as Request).some((r) => r.method === "POST" && r.url.endsWith("/api/storage/fs"))).toBe(true);
  });

  it("shows a ready folder and offers no delete", async () => {
    api(READY);
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("agent-teams-fs/users/0123456789abcdef/");
    expect(w.text()).not.toContain("超出容量");
    expect(w.text()).toContain("/dev/fuse");
    expect(w.findAll("button").some((b) => b.text().includes("Docker"))).toBe(false);
    expect(w.text()).toContain("不限");
    expect(w.text()).not.toContain("删除共享文件夹");
    expect(w.text()).not.toContain("已用空间");
  });

  it("shows how much the folder holds, against the cap", async () => {
    api({ ...READY, volume: { ...READY.volume, capacity_gb: 4 }, usage: { used_bytes: 2 ** 30, objects: 1234 } });
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("已用空间");
    expect(w.text()).toContain("1,234 个对象，占上限 25%");
  });

  it("says a folder over its capacity is read-only", async () => {
    api({ ...READY, volume: { ...READY.volume, capacity_gb: 1, read_only: true }, usage: { used_bytes: 2 ** 31, objects: 3 } });
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("已超出容量上限");
    expect(w.text()).toContain("占上限 100%");
  });
});
