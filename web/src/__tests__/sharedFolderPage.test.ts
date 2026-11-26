// @vitest-environment jsdom
// 共享文件夹 settings: switching on (the platform's storage) and the status of a ready folder.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import SharedFolder from "@/pages/settings/SharedFolder.vue";

const EMPTY = { configured: true, project_dir: "projects/{project_id}", shared_dir: "shared" };
const READY = {
  ...EMPTY,
  volume: { volume: "at-0123456789abcdef", state: "ready", error: "", capacity_gb: 0, created_at: "", updated_at: "" },
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

describe("SharedFolder page", () => {
  it("switches the folder on", async () => {
    const spy = api(EMPTY);
    const w = mountPage();
    await flushPromises();
    await w.findAll("button").find((b) => b.text() === "开通")!.trigger("click");
    await flushPromises();
    expect(spy.mock.calls.map((c) => c[0] as Request).some((r) => r.method === "POST" && r.url.endsWith("/api/storage/fs"))).toBe(true);
  });

  it("shows a ready folder", async () => {
    api(READY);
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("at-0123456789abcdef");
    expect(w.text()).toContain("不限");
    expect(w.text()).not.toContain("开通共享文件夹");
  });

  it("says what deleting keeps (published deliverables move back) and what it removes", async () => {
    api(READY);
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("交付物会先移回平台存储");
    expect(w.text()).toContain("其他文件");
  });
});
