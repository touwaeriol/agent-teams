// @vitest-environment jsdom
// 管理后台 · 平台设置: the node registration address saves on Enter, shows the default as a placeholder.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import PlatformSettings from "@/pages/admin/PlatformSettings.vue";

const SETTINGS = {
  registration_open: true, default_max_nodes: 5, max_running_threads: 0, office_default: "default",
  delivery_default: {}, node_url: "", node_url_default: "https://agent.example.com",
};

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("平台设置", () => {
  it("saves the node registration address", async () => {
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const req = input as Request;
      let body: unknown = SETTINGS;
      if (req.method === "PUT") {
        body = await req.json();
        puts.push(body);
      }
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const w = mount(PlatformSettings, { global: { plugins: [[VueQueryPlugin, { queryClient }], ElementPlus] as never[] }, attachTo: document.body });
    await flushPromises();
    const input = w.find("[data-node-url] input");
    expect(input.attributes("placeholder")).toBe("https://agent.example.com");
    await input.setValue(" https://agent-backend.example.com ");
    await input.trigger("keydown.enter");
    await flushPromises();
    expect(puts).toHaveLength(1);
    expect((puts[0] as { node_url: string }).node_url).toBe("https://agent-backend.example.com");
  });
});
