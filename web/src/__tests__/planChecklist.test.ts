// @vitest-environment jsdom
// The plan checklist card: status icons and styles, folding to the current step with x/y, live
// updates when the plan changes, and the card above the thread's message box.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import PlanChecklist from "@/components/app/plan/PlanChecklist.vue";
import { CircleDone, stepLook } from "@/components/app/plan/steps";
import ThreadDetail from "@/components/app/thread/ThreadDetail.vue";
import type { MainTaskView } from "@/api/views";
import { currentStep } from "@/lib/threadList";
import { thread } from "./fixtures";

const step = (i: number, status: string, title = `第 ${i} 步`): MainTaskView =>
  ({ id: `m${i}`, pos: i, title, status, auto_done: false, tasks_total: 0, tasks_done: 0 }) as MainTaskView;

beforeEach(() => window.localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("currentStep", () => {
  it("is the first doing step, else the first todo, else null", () => {
    expect(currentStep([step(1, "done"), step(2, "todo"), step(3, "doing")])?.id).toBe("m3");
    expect(currentStep([step(1, "done"), step(2, "skipped"), step(3, "todo"), step(4, "todo")])?.id).toBe("m3");
    expect(currentStep([step(1, "done"), step(2, "failed"), step(3, "skipped")])).toBeNull();
    expect(currentStep([])).toBeNull();
  });
  it("treats unknown statuses as todo", () => {
    expect(currentStep([step(1, "done"), step(2, "blocked")])?.id).toBe("m2");
  });
});

describe("PlanChecklist", () => {
  const plan = [step(1, "done"), step(2, "doing"), step(3, "todo"), step(4, "failed"), step(5, "skipped"), step(6, "whatever")];
  const rows = (w: ReturnType<typeof mount>) => w.findAll("li");

  it("renders every step with its status icon and style", () => {
    const w = mount(PlanChecklist, { props: { steps: plan } });
    expect(rows(w).map((r) => r.attributes("data-status"))).toEqual(["done", "doing", "todo", "failed", "skipped", "todo"]);
    expect(rows(w).map((r) => r.text())).toEqual(plan.map((s) => s.title));
    const title = (i: number) => rows(w)[i].get("span").classes();
    const icon = (i: number) => rows(w)[i].get("svg");
    // done: grey filled tick, grey struck-through text
    expect(title(0)).toEqual(expect.arrayContaining(["text-muted-foreground", "line-through"]));
    expect(icon(0).classes()).toContain("text-muted-foreground");
    expect(icon(0).find("circle[fill=currentColor]").exists()).toBe(true);
    // doing: ⋯ circle, dark normal text
    expect(icon(1).classes()).toContain("lucide-circle-ellipsis");
    expect(title(1)).toContain("text-foreground");
    expect(title(1)).not.toContain("line-through");
    // todo: dashed circle, dark text
    expect(icon(2).classes()).toContain("lucide-circle-dashed");
    expect(title(2)).toContain("text-foreground");
    // failed: red ✕ circle
    expect(icon(3).classes()).toEqual(expect.arrayContaining(["lucide-circle-x", "text-bad"]));
    // skipped: grey skip mark, grey text
    expect(icon(4).classes()).toEqual(expect.arrayContaining(["lucide-circle-slash", "text-muted-foreground"]));
    expect(title(4)).toContain("text-muted-foreground");
    expect(title(4)).not.toContain("line-through");
    // unknown: as todo
    expect(icon(5).classes()).toContain("lucide-circle-dashed");
    // every status icon is a labelled image for screen readers
    expect(rows(w).map((r) => [r.get("svg").attributes("role"), r.get("svg").attributes("aria-label"), r.get("svg").attributes("aria-hidden")])).toEqual([
      ["img", "已完成", undefined],
      ["img", "进行中", undefined],
      ["img", "未开始", undefined],
      ["img", "失败", undefined],
      ["img", "已跳过", undefined],
      ["img", "未开始", undefined],
    ]);
    expect(stepLook("nope").status).toBe("todo");
  });

  it("folds to the current step with x/y and opens again", async () => {
    const w = mount(PlanChecklist, { props: { steps: plan } });
    expect(w.get("[data-plan-progress]").text()).toBe("1/6");
    await w.get("button").trigger("click");
    expect(w.emitted("update:open")).toEqual([[false]]);
    expect(rows(w)).toHaveLength(0);
    expect(w.get("[data-current]").text()).toBe("第 2 步");
    expect(w.get("[data-current] svg").classes()).toContain("lucide-circle-ellipsis");
    expect(w.get("[data-plan-progress]").text()).toBe("1/6");
    expect(w.get("button").text()).toBe("展开");
    await w.get("button").trigger("click");
    expect(rows(w)).toHaveLength(6);
    expect(w.get("button").text()).toBe("收起");
  });

  it("folded: first todo when nothing is doing; 全部完成 only when every step is done, else 已结束", async () => {
    const w = mount(PlanChecklist, { props: { steps: [step(1, "done"), step(2, "todo"), step(3, "todo")], defaultOpen: false } });
    expect(w.get("[data-current]").text()).toBe("第 2 步");
    expect(w.get("[data-current] svg").attributes("role")).toBe("img");
    expect(w.get("[data-plan-progress]").text()).toBe("1/3");
    await w.setProps({ steps: [step(1, "done"), step(2, "done"), step(3, "skipped")] });
    expect(w.get("[data-current]").text()).toBe("已结束");
    expect(w.get("[data-current] svg").attributes()).toMatchObject({ role: "img", "aria-label": "已结束" });
    expect(w.get("[data-plan-progress]").text()).toBe("2/3");
    await w.setProps({ steps: [step(1, "done"), step(2, "failed")] });
    expect(w.get("[data-current]").text()).toBe("已结束");
    await w.setProps({ steps: [step(1, "done"), step(2, "done")] });
    expect(w.get("[data-current]").text()).toBe("全部完成");
    const done = w.get("[data-current] svg");
    expect(done.attributes()).toMatchObject({ role: "img", "aria-label": "已完成" });
    expect(done.attributes("aria-hidden")).toBeUndefined();
    expect(w.get("[data-plan-progress]").text()).toBe("2/2");
  });

  it("CircleDone without a label is decoration hidden from screen readers", () => {
    const w = mount(CircleDone);
    expect(w.attributes("aria-hidden")).toBe("true");
    expect(w.attributes("role")).toBeUndefined();
  });

  it("re-renders when the steps change", async () => {
    const w = mount(PlanChecklist, { props: { steps: [step(1, "doing"), step(2, "todo")] } });
    expect(rows(w).map((r) => r.attributes("data-status"))).toEqual(["doing", "todo"]);
    await w.setProps({ steps: [step(1, "done"), step(2, "doing"), step(3, "todo", "新加的一步")] });
    expect(rows(w).map((r) => r.attributes("data-status"))).toEqual(["done", "doing", "todo"]);
    expect(rows(w)[2].text()).toBe("新加的一步");
    expect(w.get("[data-plan-progress]").text()).toBe("1/3");
  });

  it("follows a bound open prop; not collapsible shows everything without a button", async () => {
    const w = mount(PlanChecklist, { props: { steps: plan, open: false } });
    expect(rows(w)).toHaveLength(0);
    await w.setProps({ open: true });
    expect(rows(w)).toHaveLength(6);
    const fixed = mount(PlanChecklist, { props: { steps: plan, collapsible: false, defaultOpen: false } });
    expect(fixed.findAll("li")).toHaveLength(6);
    expect(fixed.find("button").exists()).toBe(false);
  });
});

describe("PlanChecklist step limit", () => {
  const rows = (w: ReturnType<typeof mount>) => w.findAll("li");
  const titles = (w: ReturnType<typeof mount>) => rows(w).map((r) => r.text());
  /** n steps: the first `done` done, then one doing (if any left), then todo. */
  const planOf = (n: number, done: number) =>
    Array.from({ length: n }, (_, i) => step(i + 1, i < done ? "done" : i === done ? "doing" : "todo"));

  it("shows every step and no button up to 7 steps", () => {
    for (const n of [3, 7]) {
      const w = mount(PlanChecklist, { props: { steps: planOf(n, 1) } });
      expect(rows(w)).toHaveLength(n);
      expect(w.find("[data-plan-more]").exists()).toBe(false);
      expect(w.find("[data-plan-hidden-above]").exists()).toBe(false);
    }
  });

  it("shows 7 of 10 with 查看更多（还有 3 条）, all on click, then folds back", async () => {
    const w = mount(PlanChecklist, { props: { steps: planOf(10, 1) } });
    expect(titles(w)).toEqual([1, 2, 3, 4, 5, 6, 7].map((i) => `第 ${i} 步`));
    const more = w.get("[data-plan-more]");
    expect(more.text()).toBe("查看更多（还有 3 条）");
    expect(more.attributes("aria-expanded")).toBe("false");
    expect(w.find("[data-plan-hidden-above]").exists()).toBe(false);
    await more.trigger("click");
    expect(w.emitted("update:showAll")).toEqual([[true]]);
    expect(rows(w)).toHaveLength(10);
    expect(w.get("[data-plan-more]").text()).toBe("收起（只显示 7 条）");
    // the card's own fold button is separate
    expect(w.findAll("button").at(-1)!.text()).toBe("收起");
    await w.get("[data-plan-more]").trigger("click");
    expect(rows(w)).toHaveLength(7);
    expect(w.emitted("update:showAll")).toEqual([[true], [false]]);
  });

  it("keeps the current step in view with one step before it, and says how many are above", async () => {
    const w = mount(PlanChecklist, { props: { steps: planOf(12, 8) } }); // step 9 doing
    expect(rows(w)).toHaveLength(7);
    expect(titles(w)).toEqual([6, 7, 8, 9, 10, 11, 12].map((i) => `第 ${i} 步`));
    const n10 = mount(PlanChecklist, { props: { steps: planOf(10, 8) } });
    expect(titles(n10)).toEqual([4, 5, 6, 7, 8, 9, 10].map((i) => `第 ${i} 步`));
    expect(n10.get("[data-plan-hidden-above]").text()).toBe("前面还有 3 条已折叠");
    expect(n10.get("[data-plan-more]").text()).toBe("查看更多（还有 3 条）");
    const mid = mount(PlanChecklist, { props: { steps: planOf(20, 8) } });
    expect(titles(mid)).toEqual([8, 9, 10, 11, 12, 13, 14].map((i) => `第 ${i} 步`));
    expect(mid.get("[data-plan-hidden-above]").text()).toBe("前面还有 7 条已折叠");
    expect(mid.get("[data-plan-more]").text()).toBe("查看更多（还有 13 条）");
    await mid.get("[data-plan-hidden-above]").trigger("click");
    expect(rows(mid)).toHaveLength(20);
    expect(mid.find("[data-plan-hidden-above]").exists()).toBe(false);
  });

  it("shows the last 7 when nothing is left", () => {
    const w = mount(PlanChecklist, { props: { steps: planOf(10, 10) } });
    expect(titles(w)).toEqual([4, 5, 6, 7, 8, 9, 10].map((i) => `第 ${i} 步`));
    expect(w.get("[data-plan-hidden-above]").text()).toBe("前面还有 3 条已折叠");
  });

  it("keeps show-all and the card's open state when the steps change", async () => {
    const w = mount(PlanChecklist, { props: { steps: planOf(10, 1) } });
    await w.get("[data-plan-more]").trigger("click");
    await w.setProps({ steps: planOf(11, 2) });
    expect(rows(w)).toHaveLength(11);
    expect(w.get("[data-plan-more]").text()).toBe("收起（只显示 7 条）");
    await w.findAll("button").at(-1)!.trigger("click"); // fold the card
    await w.setProps({ steps: planOf(12, 3) });
    expect(rows(w)).toHaveLength(0);
    expect(w.get("[data-current]").text()).toBe("第 4 步");
    expect(w.find("[data-plan-more]").exists()).toBe(false);
  });

  it("follows a bound showAll; not collapsible (rows in the thread list) limits too", async () => {
    const w = mount(PlanChecklist, { props: { steps: planOf(9, 0), showAll: true } });
    expect(rows(w)).toHaveLength(9);
    await w.setProps({ showAll: false });
    expect(rows(w)).toHaveLength(7);
    const fixed = mount(PlanChecklist, { props: { steps: planOf(9, 0), collapsible: false } });
    expect(fixed.findAll("li")).toHaveLength(7);
    expect(fixed.findAll("button").map((b) => b.text())).toEqual(["查看更多（还有 2 条）"]);
    const wide = mount(PlanChecklist, { props: { steps: planOf(9, 0), limit: 10 } });
    expect(wide.findAll("li")).toHaveLength(9);
  });
});

describe("ThreadDetail plan card", { timeout: 15_000 }, () => {
  function mountDetail(t: ReturnType<typeof thread>) {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = input instanceof Request ? input.url : String(input);
      const body = url.includes("/messages") ? { items: [], next_cursor: null } : url.includes("/questions") ? [] : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return mount(ThreadDetail, { props: { pid: "p1", thread: t, view: null }, global: { plugins: [[VueQueryPlugin, { queryClient: qc }], router] as never[] }, attachTo: document.body });
  }

  it("shows the plan above the message box, and nothing without a plan", async () => {
    const w = mountDetail(thread(12, { main_tasks: [step(1, "done"), step(2, "doing")] }));
    await flushPromises();
    const dock = w.get("[data-plan-dock]");
    expect(dock.findAll("li")).toHaveLength(2);
    // between the chat's scroll area and the composer
    const box = document.body.querySelector("textarea[placeholder^='回复组长']")!;
    expect(dock.element.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(dock.element.previousElementSibling?.classList.contains("overflow-y-auto")).toBe(true);
    await w.setProps({ thread: thread(12, { main_tasks: [] }) });
    expect(w.find("[data-plan-dock]").exists()).toBe(false);
    await w.setProps({ thread: thread(12, { main_tasks: [step(1, "todo")] }) });
    expect(w.get("[data-plan-dock]").findAll("li")).toHaveLength(1);
  });

  it("remembers folding per thread", async () => {
    const w = mountDetail(thread(12, { id: "th_a", main_tasks: [step(1, "doing"), step(2, "todo")] }));
    await flushPromises();
    await w.get("[data-plan-dock] button").trigger("click");
    expect(window.localStorage.getItem("at:plan-open:th_a")).toBe("0");
    expect(w.get("[data-plan-dock] [data-current]").text()).toBe("第 1 步");
    await w.setProps({ thread: thread(13, { id: "th_b", main_tasks: [step(1, "doing")] }) });
    expect(w.get("[data-plan-dock]").findAll("li")).toHaveLength(1);
    await w.setProps({ thread: thread(12, { id: "th_a", main_tasks: [step(1, "doing"), step(2, "todo")] }) });
    expect(w.find("[data-plan-dock] li").exists()).toBe(false);
    await w.get("[data-plan-dock] button").trigger("click");
    expect(window.localStorage.getItem("at:plan-open:th_a")).toBeNull();
  });

  it("remembers show-all per thread, and keeps it when the plan updates", async () => {
    const long = (doing: number, n = 10) => Array.from({ length: n }, (_, i) => step(i + 1, i < doing ? "done" : i === doing ? "doing" : "todo"));
    const w = mountDetail(thread(12, { id: "th_a", main_tasks: long(1) }));
    await flushPromises();
    const dock = () => w.get("[data-plan-dock]");
    expect(dock().findAll("li")).toHaveLength(7);
    await dock().get("[data-plan-more]").trigger("click");
    expect(window.localStorage.getItem("at:plan-all:th_a")).toBe("1");
    expect(dock().findAll("li")).toHaveLength(10);
    await w.setProps({ thread: thread(12, { id: "th_a", main_tasks: long(2, 11) }) });
    expect(dock().findAll("li")).toHaveLength(11);
    await w.setProps({ thread: thread(13, { id: "th_b", main_tasks: long(1) }) });
    expect(dock().findAll("li")).toHaveLength(7);
    await w.setProps({ thread: thread(12, { id: "th_a", main_tasks: long(2, 11) }) });
    expect(dock().findAll("li")).toHaveLength(11);
    await dock().get("[data-plan-more]").trigger("click");
    expect(window.localStorage.getItem("at:plan-all:th_a")).toBeNull();
    expect(dock().findAll("li")).toHaveLength(7);
  });
});
