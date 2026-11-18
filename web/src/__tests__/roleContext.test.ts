// @vitest-environment jsdom
// The role dialog's 会话管理 section: defaults by kind of role, the token hints of the
// percentages (relative to the role's own limit) and edits through v-model.
import { mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, describe, expect, it } from "vitest";
import { defineComponent, h, nextTick, ref } from "vue";
import type { RoleView } from "@/api/views";
import { contextDefaults, contextProblems, contextTokens, DEV_FOCUS, newRole, roleProblems } from "@/lib/roles";
import ContextFields from "@/pages/settings/team/ContextFields.vue";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("context defaults", () => {
  it("fills writing members 60/80/8, gates —/80/1, lead and coordinator —/70/—", () => {
    expect(contextDefaults({ tier: "member", gate: "" })).toEqual({ context_limit: null, compact_between_pct: 60, compact_in_task_pct: 80, clear_after_tasks: 8, compact_focus: DEV_FOCUS });
    expect(contextDefaults({ tier: "member", gate: "review" })).toMatchObject({ compact_between_pct: null, compact_in_task_pct: 80, clear_after_tasks: 1 });
    expect(contextDefaults({ tier: "lead", gate: "" })).toMatchObject({ compact_between_pct: null, compact_in_task_pct: 70, clear_after_tasks: null });
    expect(newRole([], "#000")).toMatchObject({ compact_between_pct: 60, compact_in_task_pct: 80, clear_after_tasks: 8 });
  });

  it("turns percentages into tokens of the role's own limit", () => {
    expect(contextTokens(600000, 60)).toBe("360K");
    expect(contextTokens(600000, 80)).toBe("480K");
    expect(contextTokens(null, 80)).toBeNull();
  });

  it("refuses a limit under 10000, percentages outside 1–100 and a negative clear count", () => {
    const bad = { ...newRole([], "#000"), context_limit: 5000, compact_in_task_pct: 0, clear_after_tasks: -1 };
    expect(contextProblems(bad)).toHaveLength(3);
    expect(roleProblems([bad]).filter((p) => p.includes("上下文") || p.includes("比例") || p.includes("连续"))).toHaveLength(3);
    expect(contextProblems({ ...newRole([], "#000"), context_limit: 600000 })).toEqual([]);
  });
});

describe("ContextFields", () => {
  function mountFields(role: RoleView) {
    const value = ref<RoleView>(role);
    const Host = defineComponent(() => () => h(ContextFields, { modelValue: value.value, "onUpdate:modelValue": (v: RoleView) => (value.value = v) }));
    const w = mount(Host, { global: { plugins: [ElementPlus] }, attachTo: document.body });
    return { w, value };
  }

  // Number inputs in order: limit, in-task %, between-task %, clear count (Field ids are not stable outside an app).
  const FIELDS = ["上下文上限（token）", "任务中自动压缩（%）", "领任务时压缩（%）", "连续几个任务后换新会话"];
  const input = (w: ReturnType<typeof mount>, label: string) => w.findAll("input[type=number]")[FIELDS.indexOf(label)]!;

  it("shows member fields with the tokens the percentages come to and saves edits", async () => {
    const { w, value } = mountFields({ ...newRole([], "#000"), context_limit: 600000 });
    expect(w.text()).toContain("1M 窗口的模型设 600K，60% 就是 360K");
    expect(w.text()).toContain("即 480K");
    expect(w.text()).toContain("即 360K");
    await input(w, "上下文上限（token）").setValue("");
    expect(value.value.context_limit).toBeNull();
    await input(w, "领任务时压缩（%）").setValue("50");
    await input(w, "连续几个任务后换新会话").setValue("0");
    expect(value.value).toMatchObject({ compact_between_pct: 50, clear_after_tasks: 0 });
  });

  it("hides the between-task fields for the lead and restores the defaults of the kind", async () => {
    const { w, value } = mountFields({ ...newRole([], "#000"), tier: "lead", role: "lead", compact_in_task_pct: 95, compact_between_pct: null, clear_after_tasks: null });
    expect(w.findAll("label").map((l) => l.text())).not.toContain("领任务时压缩（%）");
    await w.findAll("button").find((b) => b.text() === "按岗位类型恢复默认")!.trigger("click");
    await nextTick();
    expect(value.value.compact_in_task_pct).toBe(70);
  });
});
