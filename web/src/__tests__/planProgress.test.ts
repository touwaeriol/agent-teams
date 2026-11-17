// @vitest-environment jsdom
// Plan progress outside the thread: the list row shows the current step and unfolds the whole
// plan from its "x/y" tag; the coordinator's thread card shows "x/y" and the current step.
import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import type { MainTaskView, ThreadView } from "@/api/views";
import ThreadChip from "@/components/app/ThreadChip.vue";
import ThreadRow from "@/components/app/ThreadRow.vue";
import { thread } from "./fixtures";

const step = (i: number, status: string, title = `第 ${i} 步`): MainTaskView =>
  ({ id: `m${i}`, pos: i, title, status, auto_done: false, tasks_total: 0, tasks_done: 0 }) as MainTaskView;
const planned = (patch: Partial<ThreadView> = {}) =>
  thread(19, { main_tasks: [step(1, "done"), step(2, "doing", "列表行显示当前步骤"), step(3, "todo")], ...patch });

const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { template: "<div/>" } }] });
const row = (t: ThreadView) =>
  mount(ThreadRow, { props: { thread: t, line: "", menu: false, mark: "working", now: Date.parse("2026-11-14T11:00:00Z") }, global: { plugins: [router] } });

afterEach(() => {
  document.body.innerHTML = "";
});

describe("ThreadRow plan", () => {
  it("shows x/y and the current step on one line", () => {
    const w = row(planned());
    expect(w.get("[data-plan-toggle]").text()).toBe("1/3");
    const cur = w.get("[data-current-step]");
    expect(cur.text()).toBe("列表行显示当前步骤");
    expect(cur.classes()).toContain("truncate");
  });

  it("unfolds and folds the checklist from the x/y tag without opening the thread", async () => {
    const w = row(planned());
    const toggle = w.get("[data-plan-toggle]");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(w.find("[data-plan-checklist]").exists()).toBe(false);
    await toggle.trigger("click");
    expect(toggle.attributes("aria-expanded")).toBe("true");
    expect(w.findAll("[data-plan-checklist] li").map((l) => l.text())).toEqual(["第 1 步", "列表行显示当前步骤", "第 3 步"]);
    await toggle.trigger("click");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(w.find("[data-plan-checklist]").exists()).toBe(false);
    expect(w.emitted("open")).toBeUndefined();
  });

  it("shows no current step when everything is finished", () => {
    const w = row(thread(19, { main_tasks: [step(1, "done"), step(2, "skipped")] }));
    expect(w.get("[data-plan-toggle]").text()).toBe("1/2");
    expect(w.find("[data-current-step]").exists()).toBe(false);
  });

  it("shows nothing about a plan without one", () => {
    const w = row(thread(19));
    expect(w.find("[data-plan-toggle]").exists()).toBe(false);
    expect(w.find("[data-current-step]").exists()).toBe(false);
    expect(w.find("[data-tags]").exists()).toBe(false);
  });

  it("keeps a plain tag when only the counts are known", () => {
    const w = row(thread(19, { counts: { ...thread(19).counts, main_total: 4, main_done: 2 } }));
    expect(w.get("[data-tags]").text()).toContain("2/4");
    expect(w.find("[data-plan-toggle]").exists()).toBe(false);
  });

  it("compact rows show no plan", () => {
    const w = mount(ThreadRow, {
      props: { thread: planned(), line: "", menu: false, mark: "done", now: 0, compact: true },
      global: { plugins: [router] },
    });
    expect(w.find("[data-tags]").exists()).toBe(false);
  });
});

describe("ThreadChip plan", () => {
  it("shows x/y and the current step", () => {
    const w = mount(ThreadChip, { props: { thread: planned(), no: 19 } });
    const plan = w.get("[data-chip-plan]");
    expect(plan.text()).toContain("1/3");
    expect(w.get("[data-chip-step]").text()).toBe("列表行显示当前步骤");
    expect(w.get("[data-chip-step]").classes()).toContain("truncate");
  });

  it("shows no plan line without a plan", () => {
    expect(mount(ThreadChip, { props: { thread: thread(19), no: 19 } }).find("[data-chip-plan]").exists()).toBe(false);
    expect(mount(ThreadChip, { props: { no: 19 } }).find("[data-chip-plan]").exists()).toBe(false);
  });
});
