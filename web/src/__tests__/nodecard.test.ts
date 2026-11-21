// @vitest-environment jsdom
// Node card (#42): the CLI boxes in each state and the 重新检测 button (pending, success, stale,
// failure, old daemon), asserted through the API client.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { keys } from "@/api/queries";
import type { NodeView } from "@/api/views";
import { toast } from "@/lib/toast";
import NodeCard from "@/pages/settings/nodes/NodeCard.vue";

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const cli = (runtime: string, auth: string, version: string, patch: object = {}) => ({
  runtime, installed: !!version, ...(version ? { version } : {}), auth, expired: false, checked_at: ago(3), updated_at: ago(3), ready: !!version && auth !== "none" && auth !== "unknown", ...patch,
});
const node = (patch: Partial<NodeView> = {}): NodeView =>
  ({
    id: "n1", name: "box", online: true, since: ago(10), last_seen_at: ago(0), version: "2.1.0", protocol: 2, isolation: "none", runtimes: {}, capacity: {}, active_sessions: 0, parked_sessions: 0, queued: 0,
    isolated: false, labels: [], max_active_sessions: 2, park_after_s: null, created_at: ago(100), latest_version: "", update_available: false, self_update: true, usage_supported: true, usage: null,
    runtime_status: true, clis: [cli("claude", "subscription", "2.1.322"), cli("codex", "chatgpt", "0.169.0")], ...patch,
  }) as NodeView;

type Answer = { status: number; body: unknown } | Promise<{ status: number; body: unknown }>;

/** Stubs fetch: /recheck answers with `recheck` (a promise holds it pending), anything else with []. */
function api(recheck: () => Answer) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const path = new URL(req.url).pathname;
    const a = req.method === "POST" && path.endsWith("/recheck") ? await recheck() : { status: 200, body: [] };
    return new Response(JSON.stringify(a.body), { status: a.status, headers: { "Content-Type": "application/json" } });
  });
}
const rechecks = (spy: ReturnType<typeof api>) => spy.mock.calls.map((c) => c[0] as Request).filter((r) => r.method === "POST" && r.url.endsWith("/recheck"));

function mountCard(n: NodeView) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(keys.nodes, [n]);
  const w = mount(NodeCard, { props: { node: n }, global: { plugins: [[VueQueryPlugin, { queryClient }], ElementPlus] as never[] }, attachTo: document.body });
  return { w, queryClient };
}
const box = (w: ReturnType<typeof mountCard>["w"], rt: string) => w.find(`[data-runtime="${rt}"]`);
const button = (w: ReturnType<typeof mountCard>["w"]) => w.find("button[data-recheck]");

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});
enableAutoUnmount(afterEach);

describe("NodeCard CLI boxes", () => {
  it("renders every reported runtime: logged in, not logged in, not installed, expired, unknown", () => {
    api(() => ({ status: 200, body: {} }));
    const { w } = mountCard(
      node({
        clis: [
          cli("claude", "subscription", "2.1.322"),
          cli("codex", "none", ""),
          cli("gemini", "none", "0.9.1"),
          cli("antigravity", "subscription", "1.0.0", { expired: true, ready: false }),
          cli("newcli", "unknown", "3.0"),
        ] as never,
      }),
    );
    expect(w.findAll("[data-runtime]").map((e) => e.attributes("data-runtime"))).toEqual(["claude", "codex", "gemini", "antigravity", "newcli"]);
    expect(box(w, "claude").text()).toContain("Claude Code");
    expect(box(w, "claude").find("[data-ver]").text()).toBe("2.1.322");
    expect(box(w, "claude").find("[data-login]").text()).toBe("已登录（订阅）");
    expect(box(w, "claude").attributes("data-tone")).toBe("ok");
    expect(box(w, "claude").find("[data-checked]").text()).toBe("检测于 3 分钟前");
    expect(box(w, "codex").find("[data-ver]").text()).toBe("未安装");
    expect(box(w, "codex").attributes("data-tone")).toBe("warn");
    expect(box(w, "gemini").text()).toContain("Gemini CLI");
    expect(box(w, "gemini").find("[data-login]").text()).toBe("未登录");
    expect(box(w, "antigravity").find("[data-login]").text()).toBe("登录已过期");
    expect(box(w, "antigravity").attributes("data-tone")).toBe("bad");
    expect(box(w, "newcli").find("[data-login]").text()).toBe("登录状态未知");
    expect(box(w, "newcli").attributes("data-tone")).toBe("muted");
  });

  it("an old daemon: the hello runtimes as before, no check time, 重新检测 greyed out", async () => {
    const spy = api(() => ({ status: 200, body: {} }));
    const { w } = mountCard(node({ runtime_status: false, clis: [], runtimes: { claude: { cli: "2.1.300", auth: "subscription" }, codex: {} } }));
    expect(box(w, "claude").find("[data-ver]").text()).toBe("2.1.300");
    expect(box(w, "claude").find("[data-login]").text()).toBe("已登录");
    expect(box(w, "codex").find("[data-ver]").text()).toBe("未安装");
    expect(w.find("[data-checked]").exists()).toBe(false);
    expect(button(w).attributes("disabled")).toBeDefined();
    await button(w).trigger("click");
    await flushPromises();
    expect(rechecks(spy)).toHaveLength(0);
  });

  it("an offline node cannot be rechecked", () => {
    api(() => ({ status: 200, body: {} }));
    const { w } = mountCard(node({ online: false }));
    expect(button(w).attributes("disabled")).toBeDefined();
  });
});

describe("NodeCard 重新检测", () => {
  it("shows 检测中 and disables the button while waiting, then puts the answer in the list", async () => {
    let answer!: (a: { status: number; body: unknown }) => void;
    const spy = api(() => new Promise((r) => (answer = r)));
    const ok = vi.spyOn(toast, "success").mockImplementation(() => undefined as never);
    const { w, queryClient } = mountCard(node({ clis: [cli("claude", "none", "2.1.322")] as never }));
    expect(button(w).text()).toBe("重新检测");
    await button(w).trigger("click");
    await button(w).trigger("click");
    await flushPromises();
    expect(rechecks(spy)).toHaveLength(1);
    expect(new URL(rechecks(spy)[0]!.url).pathname).toBe("/api/nodes/n1/recheck");
    expect(button(w).text()).toBe("检测中");
    expect(button(w).attributes("disabled")).toBeDefined();
    expect(w.find("[data-checking]").text()).toBe("检测中…");
    const fresh = node({ clis: [cli("claude", "subscription", "2.1.322", { checked_at: new Date().toISOString() })] as never });
    answer({ status: 200, body: { node: fresh } });
    await flushPromises();
    expect(queryClient.getQueryData<NodeView[]>(keys.nodes)?.[0]?.clis?.[0]?.auth).toBe("subscription");
    expect(ok).toHaveBeenCalledWith("检测完成");
    expect(button(w).text()).toBe("重新检测");
    expect(button(w).attributes("disabled")).toBeUndefined();
    expect(w.find("[data-checking]").exists()).toBe(false);
    // The lock is released: a second press after it finished sends another request.
    await button(w).trigger("click");
    await flushPromises();
    expect(rechecks(spy)).toHaveLength(2);
    answer({ status: 200, body: { node: fresh } });
    await flushPromises();
    expect(button(w).text()).toBe("重新检测");
  });

  it("a stale answer warns that these are the previous results", async () => {
    api(() => ({ status: 200, body: { node: node(), stale: true } }));
    const warn = vi.spyOn(toast, "warning").mockImplementation(() => undefined as never);
    const { w } = mountCard(node());
    await button(w).trigger("click");
    await flushPromises();
    expect(warn).toHaveBeenCalledWith("节点没有及时回应，显示的是上次的检测结果");
  });

  it("a failure shows the server's error and frees the button", async () => {
    api(() => ({ status: 409, body: { detail: "节点不在线", code: "node_offline" } }));
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    const { w } = mountCard(node());
    await button(w).trigger("click");
    await flushPromises();
    expect(err).toHaveBeenCalledWith(expect.stringContaining("节点不在线"));
    expect(button(w).text()).toBe("重新检测");
    expect(button(w).attributes("disabled")).toBeUndefined();
  });
});
