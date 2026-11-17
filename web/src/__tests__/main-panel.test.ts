// @vitest-environment jsdom
// The main board's thread panel: list mode keeps its width; an open thread fills the board
// but a clickable strip of the coordinator chat (made inert while squeezed).
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { COORDINATOR_PEEK, PANEL_DEFAULT } from "@/lib/panelWidth";
import Main from "@/pages/Main.vue";
import { thread } from "./fixtures";

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
const status = { state: "idle", substate: "", bubble: "", bubble_kind: "", task_id: "", task_kind: "", tier: "", role: "", since: new Date().toISOString(), seq: 1 };
const employee = { id: "e0", name: "leader", tier: "coordinator", role: "coordinator", title: "项目统筹", runtime: "claude", node: { id: "n1", name: "rc", online: true }, look: null, status };
const project = {
  id: "p1", name: "agent team", description: "", context: "", repo: null, branch: "main", git_connection_id: null, pinned: false, archived: false, work_mode: "worktree",
  auto_approve: false, require_acceptance: true, office_layout: "default", waiting: 0, icon: "", coordinator_model: "", thread_model: "", thread_codex_model: "", thread_effort: "", auto_resume: false,
  threads_active: 0, created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
};
/** Answers GETs by URL fragment (first match), everything else with {}. */
const routes: [string, unknown][] = [
  ["/coordinator", { conversation_id: "c0", employee, session_state: "active" }],
  ["/threads", [thread(12, { title: "员工通信" })]],
  ["/messages", { items: [], next_cursor: null }],
  ["/questions", []],
  ["/waiting", { questions: [], reviews: [] }],
  ["/deliverables", { items: [], storage_bytes: 0 }],
  ["/schedules", []],
  ["/api/me", { id: "u1", email: "a@b.c", display_name: "liu", role: "admin", active: true, max_nodes: null }],
  ["/api/projects/p1", project],
  ["/api/projects", [project]],
];

async function mountMain(path: string, openNo: number | null) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = input instanceof Request ? input.url : String(input);
    const hit = routes.find(([k]) => url.includes(k));
    return json(hit ? hit[1] : {});
  });
  const blank = { render: () => null };
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/projects/:pid", component: blank }, { path: "/projects/:pid/threads/:no", component: blank }, { path: "/:p(.*)*", component: blank }] });
  await router.push(path);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const w = mount(Main, { props: { openNo }, global: { plugins: [[VueQueryPlugin, { queryClient }], router, ElementPlus] as never[] }, attachTo: document.body });
  for (let i = 0; i < 5; i++) await flushPromises();
  return { w, router };
}
const aside = () => document.querySelector<HTMLElement>("aside[aria-label=讨论串]")!;

beforeEach(() => {
  window.localStorage.clear();
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("Main thread panel", () => {
  it("list mode: the remembered list width, no strip, nothing inert", async () => {
    const { w } = await mountMain("/projects/p1", null);
    expect(aside().style.width).toBe(`${PANEL_DEFAULT}px`);
    expect(w.find("[data-testid=coordinator-peek]").exists()).toBe(false);
    expect(w.find("main [inert]").exists()).toBe(false);
    expect(w.find("main").classes()).not.toContain("overflow-clip");
    w.unmount();
  }, 20_000);

  it("an open thread fills the board but the strip; the strip is inert and goes back to the list", async () => {
    const { w, router } = await mountMain("/projects/p1/threads/12", 12);
    // jsdom has no layout or ResizeObserver: the board falls back to the window width
    expect(aside().style.width).toBe(`${1440 - COORDINATOR_PEEK}px`);
    const peek = w.get("[data-testid=coordinator-peek]");
    expect(peek.attributes("aria-label")).toBe("回到项目统筹会话");
    expect(w.get("main > div").attributes("inert")).toBeDefined();
    expect(w.find("main").classes()).toContain("overflow-clip");
    await peek.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.fullPath).toBe("/projects/p1");
    w.unmount();
  }, 20_000);

  it("moves focus from the coordinator chat to the panel when the strip takes over", async () => {
    const { w } = await mountMain("/projects/p1", null);
    const box = w.get("main textarea").element as HTMLTextAreaElement;
    box.focus();
    expect(document.activeElement).toBe(box);
    await w.setProps({ openNo: 12 });
    for (let i = 0; i < 3; i++) await flushPromises();
    expect(w.find("[data-testid=coordinator-peek]").exists()).toBe(true);
    expect(document.activeElement).toBe(aside());
    w.unmount();
  }, 20_000);
});
