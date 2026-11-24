// @vitest-environment jsdom
// 上下文 breakdown of a session (#53): rows, bar segments, MCP groups, and the panel — hidden for
// runtimes without a breakdown, 刷新 waiting for its context event, auto update on turn events.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyProjectEvent } from "@/api/events";
import { keys } from "@/api/queries";
import { CATEGORY_COLOR, categoryRows, HATCH, mcpGroups, pctText, refreshErrorText, segments, shortTool, swatchStyle, updatedText } from "@/components/app/session/contextDetail";
import SessionContextPanel from "@/components/app/session/SessionContextPanel.vue";
import { toast } from "@/lib/toast";
import { contextDetail } from "./fixtures";

describe("context breakdown helpers", () => {
  it("lists what fills the window in the panel's order, then buffer, free space and deferred rows", () => {
    const { used, rest } = categoryRows(contextDetail());
    expect(used.map((r) => [r.label, r.tokensText, r.pctText])).toEqual([
      ["MCP 工具", "9,600", "1.0%"],
      ["系统工具", "1.7万", "1.7%"],
      ["消息", "4.2万", "4.2%"],
      ["Skills", "1,900", "0.2%"],
      ["记忆文件", "2,400", "0.2%"],
      ["系统提示词", "3,200", "0.3%"],
      ["MCP 服务器指令", "1,100", "0.1%"],
    ]);
    expect(rest.map((r) => [r.label, r.tokensText, r.pctText])).toEqual([
      ["自动压缩缓冲区", "3.3万", "3.3%"],
      ["可用空间", "89万", "89.0%"],
      ["延迟加载的 MCP 工具", "1.2万", "不计入"],
      ["延迟加载的系统工具", "8,700", "不计入"],
    ]);
    // the MCP rows expand into their tools: loaded ones under mcp_tools, deferred ones under the deferred row
    expect(used[0].tools.map((t) => t.name)).toHaveLength(4);
    expect(rest[2].tools.map((t) => t.name)).toEqual(["mcp__plugin_github_github__search_code"]);
  });

  it("keeps an unknown row under the runtime's own name and drops empty used rows", () => {
    const { used, rest } = categoryRows(contextDetail({ categories: [
      { id: "", name: "Mystery", tokens: 5, kind: "used" },
      { id: "custom_agents", name: "Custom agents", tokens: 0, kind: "used" },
      { id: "brand_new", name: "Brand new deferred", tokens: 9, kind: "deferred" },
    ] }));
    expect(used.map((r) => r.label)).toEqual(["Mystery"]);
    expect(rest.map((r) => [r.label, r.pctText])).toEqual([["Brand new deferred", "不计入"]]);
  });

  it("draws a segment per used row and the hatched autocompact reserve", () => {
    const s = segments(contextDetail());
    const want = [0.96, 1.68, 4.2, 0.19, 0.24, 0.32, 0.11, 3.3];
    expect(s).toHaveLength(want.length);
    s.forEach((x, i) => expect(parseFloat(x.width)).toBeCloseTo(want[i], 6));
    expect(s.at(-1)).toMatchObject({ buffer: true, label: "自动压缩缓冲区" });
    expect(segments(contextDetail({ max_tokens: 0 }))).toEqual([]);
  });

  it("groups MCP tools by server, heaviest first, without their prefix", () => {
    const g = mcpGroups(contextDetail().mcp_tools!.filter((t) => t.loaded), 1000000);
    expect(g.map((x) => [x.server, x.tokens])).toEqual([["platform", 3030], ["plugin_github_github", 1580]]);
    expect(g[0].tools.map((t) => [t.short, t.tokensText, t.pctText])).toEqual([["publish_file", "1,380", "0.14%"], ["ask_user", "1,240", "0.12%"], ["set_status", "410", "0.04%"]]);
    expect(g[1].tools[0].name).toBe("mcp__plugin_github_github__create_pull_request_review_comment_with_suggestions");
    expect(shortTool("mcp__claude_ai_Claude_Docs__read", "claude_ai_Claude_Docs")).toBe("read");
    expect(shortTool("mcp__other__x__y", "Other Server")).toBe("x__y");
  });

  it("draws the neutral swatches and ink rows in theme colours (readable in dark mode)", () => {
    expect(CATEGORY_COLOR.messages).toBe("hsl(var(--foreground))");
    for (const kind of ["buffer", "free", "deferred"] as const) expect(JSON.stringify(swatchStyle(kind, "#000"))).not.toMatch(/#[0-9A-Fa-f]{6}/);
    expect(HATCH).toContain("var(--input)");
    expect(swatchStyle("used", "#C8622A")).toEqual({ background: "#C8622A" });
    const unknown = categoryRows(contextDetail({ categories: [{ id: "", name: "X", tokens: 1, kind: "used" }] })).used[0];
    expect(unknown.color).toBe("hsl(var(--ring))");
  });

  it("says a failed refresh in Chinese", () => {
    expect(refreshErrorText(undefined)).toBeNull();
    expect(refreshErrorText("the session has no live process")).toContain("没有在运行");
    expect(refreshErrorText("no such session on this node")).toContain("找不到这个会话");
    expect(refreshErrorText("node offline")).toBe("节点不在线，稍后再试");
    expect(refreshErrorText("getContextUsage timed out")).toBe("节点没能及时统计完，稍后再试");
    expect(refreshErrorText("the runtime did not report its context")).toBe("节点没能及时统计完，稍后再试");
    expect(refreshErrorText("the runtime has no context breakdown")).toBe("这个运行时不提供上下文细分");
    expect(refreshErrorText("boom")).toBe("节点没能统计上下文，稍后再试");
  });

  it("formats shares and the update time", () => {
    expect(pctText(5, 1000000)).toBe("<0.1%");
    expect(pctText(0, 1000000)).toBe("0.0%");
    expect(pctText(1, 0)).toBe("—");
    expect(updatedText({ updated_at: null, detail: "" }, () => "x")).toBeNull();
    expect(updatedText({ updated_at: "t", detail: "summary" }, () => "刚刚")).toBe("更新于 刚刚 · 估算");
    expect(updatedText({ updated_at: "t", detail: "full" }, () => "3 分钟前")).toBe("更新于 3 分钟前 · 精确");
  });
});

type Answer = { status: number; body: unknown };
/** Stubs fetch: GET …/context/detail answers with detail(), POST …/context/refresh with refresh(). */
function api(detail: () => Answer, refresh: () => Answer = () => ({ status: 202, body: { refresh_id: "cr_1", session_id: "s_1" } })) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const path = new URL(req.url).pathname;
    const a = path.endsWith("/context/refresh") ? refresh() : path.endsWith("/context/detail") ? detail() : { status: 404, body: {} };
    return new Response(JSON.stringify(a.body), { status: a.status, headers: { "Content-Type": "application/json" } });
  });
}
const calls = (spy: ReturnType<typeof api>, suffix: string) => spy.mock.calls.map((c) => c[0] as Request).filter((r) => new URL(r.url).pathname.endsWith(suffix));
const session = (patch: object = {}) => ({ context_used: 77000, context_max: 1000000, cwd: "/w", node_id: "n1", runtime: "claude", state: "active", ...patch });

function mountPanel(thread: { id: string; session: ReturnType<typeof session> | null }) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const w = mount(SessionContextPanel, { props: { thread }, global: { plugins: [[VueQueryPlugin, { queryClient }]] as never[] }, attachTo: document.body });
  return { w, queryClient };
}

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  document.body.innerHTML = "";
});
enableAutoUnmount(afterEach);

describe("SessionContextPanel", () => {
  it("shows the total, the bar, the categories and expands MCP tools by server", async () => {
    api(() => ({ status: 200, body: contextDetail() }));
    const { w } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    expect(w.find("[data-testid=context-total]").text()).toBe("已使用 8% · 7.7万 / 100万");
    expect(w.findAll("[data-testid=context-segments] span")).toHaveLength(8);
    expect(w.find("[data-testid=context-row-free_space]").text()).toContain("可用空间");
    expect(w.find("[data-testid=context-row-mcp_tools_deferred]").text()).toContain("不计入");
    expect(w.find("[data-testid=context-divider]").exists()).toBe(true);
    expect(w.find("[data-testid=context-updated]").text()).toContain("· 估算");
    const mcp = w.find("[data-testid=context-row-mcp_tools]");
    expect(mcp.text()).toContain("4 个");
    await mcp.trigger("click");
    const tools = w.find("[data-testid=context-tools-mcp_tools]");
    expect(tools.text()).toContain("platform");
    const long = tools.find('[title="mcp__plugin_github_github__create_pull_request_review_comment_with_suggestions"]');
    expect(long.text()).toContain("create_pull_request_review_comment_with_suggestions");
    expect(long.find(".truncate").exists()).toBe(true);
    await mcp.trigger("click");
    expect(w.find("[data-testid=context-tools-mcp_tools]").exists()).toBe(false);
  });

  it("hides the whole block for Codex without asking, and when the server says unsupported", async () => {
    const spy = api(() => ({ status: 200, body: contextDetail({ supported: false, runtime: "antigravity", categories: [] }) }));
    const codex = mountPanel({ id: "t1", session: session({ runtime: "codex" }) });
    await flushPromises();
    expect(codex.w.find("[data-testid=session-context]").exists()).toBe(false);
    expect(calls(spy, "/context/detail")).toHaveLength(0);
    const agy = mountPanel({ id: "t2", session: session({ runtime: "antigravity", context_used: null, context_max: null }) });
    await flushPromises();
    expect(agy.w.find("[data-testid=session-context]").exists()).toBe(false);
    expect(agy.w.html()).toBe("<!--v-if-->");
  });

  it("uses the breakdown's totals before the session reports any", async () => {
    api(() => ({ status: 200, body: contextDetail({ total_tokens: 15000, max_tokens: 200000 }) }));
    const { w } = mountPanel({ id: "t1", session: session({ context_used: null, context_max: null }) });
    await flushPromises();
    expect(w.find("[data-testid=context-total]").text()).toBe("已使用 8% · 1.5万 / 20万");
  });

  it("刷新 spins until the context event with its refresh_id, then refetches", async () => {
    let total = 77000;
    const spy = api(() => ({ status: 200, body: contextDetail({ total_tokens: total, detail: total === 77000 ? "summary" : "full" }) }));
    const { w, queryClient } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    const btn = w.find("[data-testid=context-refresh]");
    await btn.trigger("click");
    await btn.trigger("click"); // a double click sends one request
    await flushPromises();
    expect(calls(spy, "/context/refresh")).toHaveLength(1);
    expect(btn.text()).toBe("刷新中…");
    expect(btn.attributes("disabled")).toBeDefined();
    // another conversation's answer, or a turn's, does not end it
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "turn", updated_at: "x" });
    await flushPromises();
    expect(btn.text()).toBe("刷新中…");
    total = 80000;
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_1", updated_at: "y" });
    await flushPromises();
    expect(btn.text()).toBe("刷新");
    expect(w.find("[data-testid=context-updated]").text()).toContain("· 精确");
    expect(calls(spy, "/context/detail").length).toBeGreaterThanOrEqual(3);
  });

  it("says 网络错误 when the refresh request itself fails (not an API error)", async () => {
    const ok = api(() => ({ status: 200, body: contextDetail() }));
    ok.mockImplementation(async (input) => {
      const path = new URL((input as Request).url).pathname;
      if (path.endsWith("/context/refresh")) throw new TypeError("Failed to fetch");
      return new Response(JSON.stringify(contextDetail()), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    const { w } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新");
    expect(err).toHaveBeenCalledWith("刷新失败：网络错误，稍后再试");
    const line = w.find("[data-testid=context-refresh-error]");
    expect(line.text()).toBe("刷新失败：网络错误，稍后再试");
    expect(line.attributes("title")).toContain("Failed to fetch");
  });

  it("ends the spinner on a failed or stale refresh and says why", async () => {
    api(() => ({ status: 200, body: contextDetail() }));
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    const { w, queryClient } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_1", error: "the session has no live process" });
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新");
    expect(err).toHaveBeenCalledWith("刷新失败：会话现在没有在运行（已停放），发一条消息后再刷新");
    const line = w.find("[data-testid=context-refresh-error]");
    expect(line.text()).toBe("刷新失败：会话现在没有在运行（已停放），发一条消息后再刷新");
    expect(line.attributes("title")).toBe("the session has no live process");
    // stale: no updated_at, no error — just ends
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_1" });
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新");
    expect(err).toHaveBeenCalledTimes(1);
  });

  it("a 409 says why and refetches; can_refresh=false disables the button", async () => {
    let can = true;
    const spy = api(
      () => ({ status: 200, body: contextDetail({ can_refresh: can }) }),
      () => ({ status: 409, body: { code: "session_not_running", detail: "会话现在没有在运行（已停放或已关闭），发一条消息后再刷新" } }),
    );
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    const { w } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    can = false;
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    expect(err.mock.calls[0][0]).toContain("会话现在没有在运行");
    expect(calls(spy, "/context/detail")).toHaveLength(2);
    expect(w.find("[data-testid=context-refresh]").attributes("disabled")).toBeDefined();
  });

  it("refetches when the session parks or wakes (can_refresh is a snapshot)", async () => {
    const spy = api(() => ({ status: 200, body: contextDetail() }));
    const { w } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    await w.setProps({ thread: { id: "t1", session: session({ state: "parked" }) } });
    await flushPromises();
    expect(calls(spy, "/context/detail")).toHaveLength(2);
  });

  it("gives up waiting after a while", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api(() => ({ status: 200, body: contextDetail() }));
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    const { w } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    vi.advanceTimersByTime(46_000);
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新");
    expect(err).toHaveBeenCalledWith("刷新失败：节点没能及时统计完，稍后再试");
  });

  it("ends at once when the answer came before the POST returned", async () => {
    const held: { qc?: QueryClient } = {};
    api(
      () => ({ status: 200, body: contextDetail() }),
      () => {
        applyProjectEvent(held.qc!, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_1", updated_at: "y" });
        return { status: 202, body: { refresh_id: "cr_1", session_id: "s_1" } };
      },
    );
    const { w, queryClient } = mountPanel({ id: "t1", session: session() });
    held.qc = queryClient;
    await flushPromises();
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新");
  });

  it("forgets a pending refresh and its failure when another session opens", async () => {
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    api(() => ({ status: 200, body: contextDetail() }));
    const { w, queryClient } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_1", error: "boom" });
    await flushPromises();
    expect(w.find("[data-testid=context-refresh-error]").exists()).toBe(true);
    await w.find("[data-testid=context-refresh]").trigger("click");
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新中…");
    await w.setProps({ thread: { id: "t2", session: session() } });
    await flushPromises();
    expect(w.find("[data-testid=context-refresh]").text()).toBe("刷新");
    expect(w.find("[data-testid=context-refresh-error]").exists()).toBe(false);
    expect(err).toHaveBeenCalledTimes(1);
  });

  it("a turn's context event refetches the breakdown (auto update)", async () => {
    const spy = api(() => ({ status: 200, body: contextDetail() }));
    const { queryClient } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "turn", updated_at: "z" });
    await flushPromises();
    expect(calls(spy, "/context/detail")).toHaveLength(2);
    expect(queryClient.getQueryData(keys.contextEvent("t1"))).toMatchObject({ reason: "turn" });
  });

  it("a stored breakdown refetches the thread too (its totals feed the bar); an error or stale answer does not", async () => {
    api(() => ({ status: 200, body: contextDetail() }));
    const { queryClient } = mountPanel({ id: "t1", session: session() });
    await flushPromises();
    const spy = vi.spyOn(queryClient, "invalidateQueries");
    const invalidated = () => spy.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey));
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_1", error: "x" });
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_2" });
    expect(invalidated()).not.toContain(JSON.stringify(keys.threads("p1")));
    applyProjectEvent(queryClient, "p1", "context", { thread_no: 1, conversation_id: "t1", session_id: "s_1", reason: "refresh", refresh_id: "cr_3", updated_at: "y" });
    expect(invalidated()).toContain(JSON.stringify(keys.threads("p1")));
    expect(invalidated()).toContain(JSON.stringify(keys.thread("t1")));
  });
});
