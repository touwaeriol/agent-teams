// @vitest-environment jsdom
// 共享文件夹 settings: switching on with the platform's storage or the user's own S3 / JuiceFS.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import SharedFolder from "@/pages/settings/SharedFolder.vue";

const EMPTY = { configured: true, project_dir: "projects/{project_id}", shared_dir: "shared" };
const READY_S3 = {
  ...EMPTY,
  volume: { volume: "at-0123456789abcdef", backend: "s3", summary: { endpoint: "https://s3.me", bucket: "mine" }, state: "ready", error: "", capacity_gb: 20, created_at: "", updated_at: "" },
};

function api(get: unknown) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const body = req.method === "POST" ? READY_S3 : get;
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
  it("switches on with the user's own S3 and shows where it is", async () => {
    const spy = api(EMPTY);
    const w = mountPage();
    await flushPromises();
    const radios = w.findAll("label.el-radio-button");
    await radios[1]!.trigger("click");
    await flushPromises();
    const inputs = w.findAll("input:not([type=radio])");
    const values = ["https://s3.me", "mine", "AK", "SK"];
    for (const [i, v] of values.entries()) await inputs[i]!.setValue(v);
    const enable = w.findAll("button").find((b) => b.text() === "开通")!;
    expect(enable.attributes("disabled")).toBeUndefined();
    await enable.trigger("click");
    await flushPromises();
    const post = spy.mock.calls.map((c) => c[0] as Request).find((r) => r.method === "POST")!;
    expect(await post.clone().json()).toEqual({ kind: "s3", s3_endpoint: "https://s3.me", bucket: "mine", access_key: "AK", secret_key: "SK" });
  });

  it("shows the storage of a ready folder", async () => {
    api(READY_S3);
    const w = mountPage();
    await flushPromises();
    expect(w.text()).toContain("自己的 S3");
    expect(w.text()).toContain("https://s3.me/mine");
    expect(w.text()).not.toContain("开通共享文件夹");
  });
});
