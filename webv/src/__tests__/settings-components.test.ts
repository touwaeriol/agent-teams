// @vitest-environment jsdom
// Settings screens on Vue: the repo list and picker, schedules, tag input, number settings, the
// team draft saved in display order and project settings switches, asserted through the API client.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, ref, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import RepoList from "@/components/app/repo/RepoList.vue";
import type { RepoRef } from "@/components/app/repo/repo";
import ScheduleForm from "@/components/app/schedules/ScheduleForm.vue";
import TagInput from "@/components/common/TagInput.vue";
import { newRole } from "@/lib/roles";
import NumberSetting from "@/pages/settings/admin/NumberSetting.vue";
import GeneralSection from "@/pages/projectSettings/GeneralSection.vue";
import Team from "@/pages/settings/Team.vue";

type Route = [method: string, path: RegExp, body: unknown];

/** Stubs fetch with canned JSON per method + path; returns the spy to inspect requests. */
function api(routes: Route[]) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const path = new URL(req.url).pathname;
    const hit = routes.find(([m, re]) => m === req.method && re.test(path));
    return new Response(JSON.stringify(hit ? hit[2] : {}), { status: hit ? 200 : 404, headers: { "Content-Type": "application/json" } });
  });
}

const calls = (spy: ReturnType<typeof api>, method: string, re: RegExp) => spy.mock.calls.map((c) => c[0] as Request).filter((r) => r.method === method && re.test(new URL(r.url).pathname));

function mountWith(c: Component, props: Record<string, unknown> = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(c, { props, global: { plugins: [[VueQueryPlugin, { queryClient }], router, ElementPlus] as never[] }, attachTo: document.body });
}

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const connection = { id: "g1", kind: "app", login: "touwaeriol", name: "touwaeriol", installation_settings_url: null, needs_permission_approval: false, created_at: "2026-01-01T00:00:00Z" };

describe("RepoList", () => {
  it("lists repos in order and moves, removes them through v-model", async () => {
    api([["GET", /\/api\/git\/connections$/, [connection]], ["GET", /\/repos$/, { items: [], next_cursor: null }]]);
    const value = ref<RepoRef[]>([{ repo: "o/api", git_connection_id: "g1" }, { repo: "o/web", git_connection_id: "gone" }]);
    const Host = defineComponent(() => () => h(RepoList, { modelValue: value.value, "onUpdate:modelValue": (v: RepoRef[]) => (value.value = v) }));
    const w = mountWith(Host);
    await flushPromises();
    expect(w.findAll("li").map((li) => li.text())).toEqual(["1o/apiGitHub · touwaeriol", "2o/web连接已删除"]);
    expect(w.get("[aria-label='上移 o/api']").attributes("disabled")).toBeDefined();
    await w.get("[aria-label='下移 o/api']").trigger("click");
    expect(value.value.map((r) => r.repo)).toEqual(["o/web", "o/api"]);
    await w.get("[aria-label='移除 o/web']").trigger("click");
    expect(value.value.map((r) => r.repo)).toEqual(["o/api"]);
    expect(w.text()).toContain("再加一个仓库");
  });
  it("asks to connect GitHub when there is no connection", async () => {
    api([["GET", /\/api\/git\/connections$/, []]]);
    const w = mountWith(RepoList, { modelValue: [] });
    await flushPromises();
    expect(w.text()).toContain("还没有连接 GitHub。先去连接");
  });
});

describe("ScheduleForm", () => {
  it("saves a new schedule with the browser time zone, enabled", async () => {
    const spy = api([["POST", /\/api\/projects\/p1\/schedules$/, { id: "s1" }]]);
    const draft = ref({ name: "", cron: "0 9 * * *", message: "" });
    const Host = defineComponent(() => () => h(ScheduleForm, { pid: "p1", modelValue: draft.value, "onUpdate:modelValue": (v: typeof draft.value) => (draft.value = v), onDone: () => (draft.value.name = "done") }));
    const w = mountWith(Host);
    const submit = () => w.get("button[type=submit]");
    expect(submit().attributes("disabled")).toBeDefined();
    await w.get("input").setValue("日报");
    await w.get("textarea").setValue("汇总进展");
    await w.get(".at-mono input").setValue("0 9 * * 1-5");
    expect(w.get(".el-select").text()).toContain("工作日 9:00");
    await w.get("form").trigger("submit");
    await flushPromises();
    const [req] = calls(spy, "POST", /schedules$/);
    expect(await req.json()).toMatchObject({ name: "日报", cron: "0 9 * * 1-5", message: "汇总进展", enabled: true, timezone: expect.any(String) });
    expect(draft.value.name).toBe("done");
  });
});

describe("TagInput", () => {
  it("adds trimmed items once on Enter or comma", async () => {
    const value = ref<string[]>(["gpu"]);
    const Host = defineComponent(() => () => h(TagInput, { modelValue: value.value, "onUpdate:modelValue": (v: string[]) => (value.value = v) }));
    const w = mountWith(Host);
    const input = w.get("input");
    await input.setValue(" big-disk ");
    await input.trigger("keydown", { key: "Enter", code: "Enter" });
    await nextTick();
    await input.setValue("gpu，arm");
    await nextTick();
    expect(value.value).toEqual(["gpu", "big-disk", "arm"]);
  });
});

describe("NumberSetting", () => {
  it("saves a changed whole number on blur and puts back anything else", async () => {
    const w = mountWith(NumberSetting, { label: "每人最多节点", value: 3 });
    const input = w.get("input");
    await input.setValue("5");
    await input.trigger("blur");
    expect(w.emitted("save")).toEqual([[5]]);
    await input.setValue("x");
    await input.trigger("blur");
    expect((input.element as HTMLInputElement).value).toBe("3");
    expect(w.emitted("save")).toHaveLength(1);
  });
});

describe("Team", () => {
  it("adds a role and saves the whole draft in display order", async () => {
    const coordinator = { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", sort: 0 };
    const lead = { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", sort: 1 };
    const dev = { ...newRole([], "#333"), id: "r2", role: "dev", title: "开发", sort: 2 };
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: [dev, lead, coordinator], warnings: [] }],
      ["PUT", /\/api\/roles$/, { roles: [], warnings: [] }],
      ["GET", /\/api\/(skills|plugins|nodes|roles\/presets)$/, {}],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    const save = () => w.findAll("button").find((b) => b.text() === "保存")!;
    expect(save().attributes("disabled")).toBeDefined();
    await w.findAll("button").find((b) => b.text() === "添加岗位")!.trigger("click");
    expect(w.text()).toContain("新岗位");
    await save().trigger("click");
    await flushPromises();
    const body = (await calls(spy, "PUT", /\/api\/roles$/)[0].json()) as { roles: { role: string; sort: number }[] };
    expect(body.roles.map((r) => [r.role, r.sort])).toEqual([["coordinator", 0], ["lead", 1], ["dev", 2], ["role-1", 3]]);
  });
});

describe("Project settings · 通用", () => {
  it("saves switches at once and the intro on blur", async () => {
    const project = {
      id: "p1", name: "Agent", icon: "", description: "旧简介", context: "", auto_resume: false, archived: false, coordinator_model: "", thread_model: "", thread_effort: "",
      delivery: { approval_routing: "lead_first", lead_review_timeout_s: 120, notify_on_ask: true, allow_workflow_edits: false, merge_method: "squash" },
    };
    const spy = api([["PATCH", /\/api\/projects\/p1$/, project]]);
    const w = mountWith(GeneralSection, { project });
    await w.get("[aria-label='使用限额重置后自动继续讨论串']").trigger("click");
    await flushPromises();
    const intro = w.get("input[aria-label='一句话简介']");
    await intro.setValue("新简介");
    await intro.trigger("blur");
    await flushPromises();
    const bodies = await Promise.all(calls(spy, "PATCH", /\/api\/projects\/p1$/).map((r) => r.json()));
    expect(bodies).toEqual([{ auto_resume: true }, { description: "新简介" }]);
  });
});
