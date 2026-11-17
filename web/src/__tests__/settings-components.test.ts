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
import { toast } from "@/lib/toast";
import NumberSetting from "@/pages/admin/NumberSetting.vue";
import EnvironmentSection from "@/pages/projectSettings/EnvironmentSection.vue";
import GeneralSection from "@/pages/projectSettings/GeneralSection.vue";
import MemorySection from "@/pages/projectSettings/MemorySection.vue";
import Team from "@/pages/settings/Team.vue";
import ModelFields from "@/pages/projectSettings/ModelFields.vue";
import NewProject from "@/pages/NewProject.vue";

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
  }, 15000);
});

describe("Team · commands and models", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("hides a platform command, adds a prompt command on the lead card and saves them with the roles", async () => {
    const coordinator = { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", title: "项目统筹", sort: 0 };
    const lead = { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", title: "组长", sort: 1 };
    const codex = { ...newRole([], "#333"), id: "r2", role: "dev", title: "开发", runtime: "codex" as const, sort: 2 };
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: [coordinator, lead, codex], warnings: [] }],
      ["PUT", /\/api\/roles$/, { roles: [], warnings: [] }],
      ["GET", /\/api\/models$/, { models: [{ id: "m1", runtime: "claude", model: "claude-opus-5-5", label: "Opus 5.5", default: true, mine: true }] }],
      ["PUT", /\/api\/models\/mine$/, { models: [] }],
      ["GET", /\/api\/(skills|plugins|nodes|roles\/presets)$/, {}],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    const editors = w.findAll("[data-testid=command-editor]");
    expect(editors).toHaveLength(3);
    const leadEditor = editors[1]!;
    expect(leadEditor.findAll("button[aria-pressed]").map((b) => b.text())).toContain("/effort");
    expect(editors[0]!.findAll("button[aria-pressed]").map((b) => b.text())).not.toContain("/effort");
    expect(editors[2]!.findAll("button[aria-pressed]")).toHaveLength(0);
    expect(editors[2]!.text()).toContain("Codex 没有斜杠命令");
    await leadEditor.findAll("button[aria-pressed]").find((b) => b.text() === "/usage")!.trigger("click");
    await w.findAll("[data-testid=command-editor]")[1]!.findAll("button").find((b) => b.text().includes("添加命令"))!.trigger("click");
    await flushPromises();
    const dialog = document.querySelector(".at-dialog")!;
    expect(dialog.textContent).toContain("透传给 Claude Code");
    const inputs = dialog.querySelectorAll<HTMLInputElement>("input.el-input__inner");
    inputs[0]!.value = "plan";
    inputs[0]!.dispatchEvent(new Event("input"));
    const area = dialog.querySelector<HTMLTextAreaElement>("textarea")!;
    area.value = "先列计划：{{args}}";
    area.dispatchEvent(new Event("input"));
    await flushPromises();
    dialog.querySelector("form")!.dispatchEvent(new Event("submit"));
    await flushPromises();
    expect(w.findAll("[data-testid=command-editor]")[1]!.text()).toContain("/plan");
    await w.findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    const body = (await calls(spy, "PUT", /\/api\/roles$/)[0]!.json()) as { roles: { role: string; hidden_commands: string[]; commands: unknown[] }[] };
    const saved = body.roles.find((r) => r.role === "lead")!;
    expect(saved.hidden_commands).toEqual(["usage"]);
    expect(saved.commands).toEqual([{ name: "plan", description: "", mode: "prompt", prompt: "先列计划：{{args}}" }]);
  });

  it("offers only prompt templates for a Codex role's command", async () => {
    const coordinator = { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", sort: 0 };
    const lead = { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", sort: 1 };
    const codex = { ...newRole([], "#333"), id: "r2", role: "dev", runtime: "codex" as const, sort: 2 };
    api([
      ["GET", /\/api\/roles$/, { roles: [coordinator, lead, codex], warnings: [] }],
      ["GET", /\/api\/models$/, { models: [] }],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    await w.findAll("[data-testid=command-editor]")[2]!.findAll("button").find((b) => b.text().includes("添加命令"))!.trigger("click");
    await flushPromises();
    const dialog = document.querySelector(".at-dialog")!;
    expect(dialog.textContent).toContain("提示词模板");
    expect(dialog.textContent).not.toContain("透传给 Claude Code");
    expect(dialog.textContent).toContain("Codex 没有斜杠命令");
  });

  it("edits the user's model list: add, rename, reorder, default per type, remove, save", async () => {
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: [], warnings: [] }],
      [
        "GET",
        /\/api\/models$/,
        {
          models: [
            { id: "m1", runtime: "claude", model: "claude-opus-5-5", label: "Opus 5.5", default: true, mine: true },
            { id: "m2", runtime: "claude", model: "claude-sonnet-x", label: "Sonnet", default: false, mine: true },
            { id: "m3", runtime: "codex", model: "gpt-x", label: "", default: false, mine: true },
          ],
        },
      ],
      ["PUT", /\/api\/models\/mine$/, { models: [] }],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    expect(w.text()).toContain("/model 和岗位模型字段按这个顺序列出这里的模型");
    expect(w.text()).toContain("岗位和项目都没指定模型时，会话用它");
    expect(w.text()).not.toContain("平台");
    const editor = () => w.get("[data-testid=model-list-editor]");
    const btn = (label: string, i = 0) => editor().findAll(`button[aria-label=${label}]`)[i]!;
    const ids = () => editor().findAll("input[aria-label='模型 ID']").map((i) => (i.element as HTMLInputElement).value);
    expect(ids()).toEqual(["claude-opus-5-5", "claude-sonnet-x", "gpt-x"]);
    // add a Claude model, rename it, move it to the top and make it the Claude default
    await editor().findAll("button").find((b) => b.text() === "添加模型")!.trigger("click");
    await editor().findAll("input[aria-label='模型 ID']")[3]!.setValue("claude-haiku-x");
    await editor().findAll("input[aria-label='显示名']")[3]!.setValue("Haiku");
    for (let i = 3; i > 0; i--) await btn("上移", i).trigger("click");
    expect(ids()).toEqual(["claude-haiku-x", "claude-opus-5-5", "claude-sonnet-x", "gpt-x"]);
    expect(btn("上移", 0).attributes("disabled")).toBeDefined();
    expect(btn("下移", 3).attributes("disabled")).toBeDefined();
    const defaults = () => editor().findAll(".el-checkbox");
    await defaults()[0]!.trigger("click");
    await defaults()[3]!.trigger("click");
    // move sonnet below gpt, then remove it
    await btn("下移", 2).trigger("click");
    expect(ids()).toEqual(["claude-haiku-x", "claude-opus-5-5", "gpt-x", "claude-sonnet-x"]);
    await btn("删除", 3).trigger("click");
    await editor().findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    const body = (await calls(spy, "PUT", /\/api\/models\/mine$/)[0]!.json()) as { models: { runtime: string; model: string; label: string; default: boolean }[] };
    expect(body.models.map((m) => [m.runtime, m.model, m.label, m.default])).toEqual([
      ["claude", "claude-haiku-x", "Haiku", true],
      ["claude", "claude-opus-5-5", "Opus 5.5", false],
      ["codex", "gpt-x", "", true],
    ]);
  });

  it("will not save an empty list", async () => {
    api([
      ["GET", /\/api\/roles$/, { roles: [], warnings: [] }],
      ["GET", /\/api\/models$/, { models: [{ id: "m1", runtime: "claude", model: "claude-opus-5-5", label: "", default: true, mine: true }] }],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    const editor = () => w.get("[data-testid=model-list-editor]");
    const save = () => editor().findAll("button").find((b) => b.text() === "保存")!;
    await editor().get("button[aria-label=删除]").trigger("click");
    expect(editor().text()).toContain("至少保留一个模型");
    expect(save().attributes("disabled")).toBeDefined();
  });
});

describe("Team · role model field", () => {
  const models = [
    { id: "m1", runtime: "claude", model: "claude-opus-5-5", label: "Opus 5.5", default: true, mine: true },
    { id: "m2", runtime: "claude", model: "claude-sonnet-x", label: "Sonnet", default: false, mine: true },
    { id: "m3", runtime: "codex", model: "gpt-x", label: "", default: false, mine: true },
  ];
  const roles = () => [
    { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", title: "项目统筹", sort: 0 },
    { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", title: "组长", model: "old-model", sort: 1 },
    { ...newRole([], "#333"), id: "r2", role: "dev", title: "开发", model: "claude-sonnet-x", sort: 2 },
    { ...newRole([], "#444"), id: "r3", role: "rev", title: "审查", runtime: "codex" as const, sort: 3 },
  ];
  async function mountTeam() {
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: roles(), warnings: [] }],
      ["PUT", /\/api\/roles$/, { roles: [], warnings: [] }],
      ["GET", /\/api\/models$/, { models }],
      ["GET", /\/api\/(skills|plugins|nodes|roles\/presets)$/, {}],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    return { w, spy };
  }
  const cards = (w: ReturnType<typeof mountWith>) => w.findAll("article");
  /** The option texts of a card's select (by aria-label), from its listbox. */
  function options(card: ReturnType<ReturnType<typeof mountWith>["findAll"]>[number], label: string) {
    const listbox = document.getElementById(card.get(`input[aria-label=${label}]`).attributes("aria-controls")!)!;
    return [...listbox.querySelectorAll<HTMLElement>(".el-select-dropdown__item")];
  }

  it("shows follow-my-default per type, an old value marked 不在列表, and only the type's models", async () => {
    const { w } = await mountTeam();
    const [coordinator, lead, dev, rev] = cards(w);
    expect(coordinator!.text()).toContain("默认（Opus 5.5）");
    expect(lead!.text()).toContain("old-model（不在列表）");
    expect(dev!.text()).toContain("Sonnet");
    expect(rev!.text()).toContain("默认（未设置）");
    // type label no longer carries the model
    expect(dev!.text()).not.toContain("Claude Code · ");
    expect(options(dev!, "模型").map((o) => o.textContent!.trim())).toEqual(["跟随我的默认（当前：Opus 5.5）", "Opus 5.5默认", "Sonnet"]);
    expect(options(rev!, "模型").map((o) => o.textContent!.trim())).toEqual(["跟随我的默认（未设置）", "gpt-x"]);
    expect(options(lead!, "模型").map((o) => o.textContent!.trim())).toContain("old-model不在列表");
    const listbox = document.getElementById(dev!.get("input[aria-label=模型]").attributes("aria-controls")!)!;
    expect(listbox.closest(".el-select-dropdown")!.textContent).toContain("管理我的模型");
    expect(document.getElementById("my-models")).not.toBeNull();
  }, 15000);

  it("picks a model, follows the default again, and resets the model when the type changes", async () => {
    const info = vi.spyOn(toast, "info").mockImplementation(() => {});
    const { w, spy } = await mountTeam();
    // coordinator: pick Sonnet
    options(cards(w)[0]!, "模型").find((o) => o.textContent!.includes("Sonnet"))!.click();
    await flushPromises();
    // lead: back to following the default
    options(cards(w)[1]!, "模型")[0]!.click();
    await flushPromises();
    // dev (claude-sonnet-x) → Codex: the model is dropped
    options(cards(w)[2]!, "类型").find((o) => o.textContent!.includes("Codex"))!.click();
    await flushPromises();
    expect(info).toHaveBeenCalledWith("已改为跟随默认模型");
    expect(cards(w)[2]!.text()).toContain("默认（未设置）");
    // rev (codex, "") → Claude Code keeps following without a toast
    info.mockClear();
    options(cards(w)[3]!, "类型").find((o) => o.textContent!.includes("Claude Code"))!.click();
    await flushPromises();
    expect(info).not.toHaveBeenCalled();
    await w.findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    const body = (await calls(spy, "PUT", /\/api\/roles$/)[0]!.json()) as { roles: { role: string; runtime: string; model: string }[] };
    expect(body.roles.map((r) => [r.role, r.runtime, r.model])).toEqual([
      ["coordinator", "claude", "claude-sonnet-x"],
      ["lead", "claude", ""],
      ["dev", "codex", ""],
      ["rev", "claude", ""],
    ]);
  }, 15000);
});

describe("Project settings · 通用", () => {
  it("saves switches at once and the intro on blur", async () => {
    const project = {
      id: "p1", name: "Agent", icon: "", description: "旧简介", context: "", auto_resume: false, archived: false, coordinator_model: "", thread_model: "", thread_codex_model: "", thread_effort: "",
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

const settingsProject = {
  id: "p1", name: "Agent", icon: "", description: "", context: "旧指令", auto_resume: false, archived: false, coordinator_model: "", thread_model: "", thread_codex_model: "", thread_effort: "",
  work_mode: "worktree", repos: [{ repo: "o/api", git_connection_id: "g1" }],
  delivery: { approval_routing: "lead_first", lead_review_timeout_s: 120, notify_on_ask: true, allow_workflow_edits: false, merge_method: "squash" },
};
const brief = (name: string, description = "", by = "user") => ({ name, description, size: 3, updated_by: by, updated_at: new Date(Date.now() - 2 * 3600_000).toISOString() });

describe("Project settings · 记忆", () => {
  it("saves 项目指令 with its button and lists MEMORY.md and the memory files", async () => {
    const spy = api([
      ["GET", /\/memory$/, [brief("MEMORY.md"), brief("testing.md", "怎么跑测试", "lead-1 · #2")]],
      ["PATCH", /\/api\/projects\/p1$/, settingsProject],
    ]);
    const w = mountWith(MemorySection, { project: settingsProject });
    await flushPromises();
    expect(w.text()).toContain("3 / 16,000");
    await w.get("#project-instructions").setValue("回答都以【测】开头");
    await w.findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    expect(await calls(spy, "PATCH", /\/api\/projects\/p1$/)[0].json()).toEqual({ context: "回答都以【测】开头" });
    const index = w.get("[aria-label='打开 MEMORY.md']");
    expect(index.text()).toContain("Agent 项目");
    expect(index.text()).toContain("更新于 2 小时前");
    expect(w.get("[aria-label='打开 testing.md']").text()).toContain("怎么跑测试");
    expect(w.text()).not.toContain("暂无其他文件");
  });

  it("opens a file, edits it and saves through PUT; deletes a topic file", async () => {
    const file = { name: "testing.md", description: "怎么跑测试", content: "# 测试\nmake test", updated_by: "lead-1 · #2", updated_at: new Date().toISOString(), created_at: "" };
    const spy = api([
      ["GET", /\/memory$/, [brief("MEMORY.md"), brief("testing.md", "怎么跑测试")]],
      ["GET", /\/memory\/testing\.md$/, file],
      ["PUT", /\/memory\/testing\.md$/, file],
      ["DELETE", /\/memory\/testing\.md$/, {}],
    ]);
    const w = mountWith(MemorySection, { project: settingsProject });
    await flushPromises();
    await w.get("[aria-label='打开 testing.md']").trigger("click");
    await flushPromises();
    expect(document.body.textContent).toContain("make test");
    const edit = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.includes("编辑"))!;
    edit.click();
    await flushPromises();
    const area = document.body.querySelector<HTMLTextAreaElement>(".el-dialog textarea")!;
    area.value = "用 docker 跑 make test";
    area.dispatchEvent(new Event("input"));
    await flushPromises();
    [...document.body.querySelectorAll<HTMLButtonElement>(".el-dialog button")].find((b) => b.textContent?.trim() === "保存")!.click();
    await flushPromises();
    expect(await calls(spy, "PUT", /\/memory\/testing\.md$/)[0].json()).toEqual({ description: "怎么跑测试", content: "用 docker 跑 make test" });
    await w.get("[aria-label='打开 testing.md']").trigger("click");
    await flushPromises();
    [...document.body.querySelectorAll<HTMLButtonElement>(".el-dialog button")].find((b) => b.textContent?.includes("删除"))!.click();
    await flushPromises();
    [...document.body.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === "删除").at(-1)!.click();
    await flushPromises();
    expect(calls(spy, "DELETE", /\/memory\/testing\.md$/)).toHaveLength(1);
  });

  it("shows 暂无其他文件 when only MEMORY.md exists", async () => {
    api([["GET", /\/memory$/, []]]);
    const w = mountWith(MemorySection, { project: settingsProject });
    await flushPromises();
    expect(w.text()).toContain("暂无其他文件。");
    expect(w.text()).toContain("还没有内容");
  });
});

describe("Project settings · 环境", () => {
  it("lists the repositories every thread includes and maps the worktree choice to work_mode", async () => {
    const spy = api([["GET", /\/api\/git\/connections$/, [connection]], ["GET", /\/repos$/, { items: [], next_cursor: null }], ["PATCH", /\/api\/projects\/p1$/, settingsProject]]);
    const w = mountWith(EnvironmentSection, { project: settingsProject });
    await flushPromises();
    expect(w.text()).toContain("每个讨论串中都包含的仓库");
    expect(w.text()).not.toMatch(/云环境|管理节点|分支命名/);
    expect(w.findAll("li").map((li) => li.text())).toEqual(["1o/apiGitHub · touwaeriol"]);
    await w.findAll("button").find((b) => b.text().includes("添加"))!.trigger("click");
    expect(w.text()).toContain("再加一个仓库");
    await w.get("[aria-label='移除 o/api']").trigger("click");
    await w.findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    expect(await calls(spy, "PATCH", /\/api\/projects\/p1$/)[0].json()).toEqual({ repos: [] });
    expect(w.text()).toContain("每个节点克隆一份仓库");
    const select = w.findAllComponents({ name: "ElSelect" }).find((c) => c.attributes("aria-label") === "工作树" || c.props("ariaLabel") === "工作树")!;
    select.vm.$emit("update:modelValue", "clone");
    await flushPromises();
    expect(await calls(spy, "PATCH", /\/api\/projects\/p1$/)[1].json()).toEqual({ work_mode: "clone" });
  });
});

describe("Project models", () => {
  const claudeOnly = [
    { id: "m1", runtime: "claude", model: "claude-opus-5-5", label: "Opus 5.5", default: true, mine: true },
    { id: "m2", runtime: "claude", model: "claude-sonnet-x", label: "Sonnet", default: false, mine: true },
  ];
  const withCodex = [...claudeOnly, { id: "m3", runtime: "codex", model: "gpt-x", label: "", default: true, mine: true }];
  const teamRoles = [
    { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", sort: 0 },
    { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", model: "claude-sonnet-x", sort: 1 },
    { ...newRole([], "#333"), id: "r2", role: "dev", sort: 2 },
  ];
  const listbox = (root: Element, label: string) => document.getElementById(root.querySelector(`input[aria-label='${label}']`)!.getAttribute("aria-controls")!)!;
  const optionTexts = (root: Element, label: string) => [...listbox(root, label).querySelectorAll(".el-select-dropdown__item")].map((o) => o.textContent!.trim());
  const pick = (root: Element, label: string, text: string) => [...listbox(root, label).querySelectorAll<HTMLElement>(".el-select-dropdown__item")].find((o) => o.textContent!.includes(text))!.click();
  const created = { id: "p9", name: "新项目" };

  async function mountNew(models: unknown[]) {
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: teamRoles, warnings: [] }],
      ["GET", /\/api\/models$/, { models }],
      ["POST", /\/api\/projects$/, created],
      ["GET", /\/api\/projects$/, []],
    ]);
    const w = mountWith(NewProject);
    await flushPromises();
    await w.get("input[placeholder='例如：agent team']").setValue("新项目");
    return { w, spy };
  }
  const submit = async (w: ReturnType<typeof mountWith>) => {
    await w.get("form").trigger("submit");
    await flushPromises();
  };

  it("new project: follows the roles by default and sends empty model fields", async () => {
    const { w, spy } = await mountNew(withCodex);
    const card = w.get("[data-testid=new-project-models]");
    expect(card.text()).toContain("默认跟随岗位配置");
    expect(card.find("input[aria-label='统筹 · Claude Code']").exists()).toBe(false);
    await submit(w);
    const body = (await calls(spy, "POST", /\/api\/projects$/)[0]!.json()) as Record<string, unknown>;
    expect([body.coordinator_model, body.thread_model, body.thread_codex_model]).toEqual(["", "", ""]);
  }, 15000);

  it("new project: picks models per type from that type's list only and sends them", async () => {
    const { w, spy } = await mountNew(withCodex);
    const card = () => w.get("[data-testid=new-project-models]");
    await card().findAll("button").find((b) => b.text() === "按类型指定")!.trigger("click");
    const el = card().element;
    expect(optionTexts(el, "统筹 · Claude Code")).toEqual(["跟随岗位配置", "Opus 5.5默认", "Sonnet"]);
    expect(optionTexts(el, "讨论串 · Codex")).toEqual(["跟随岗位配置", "gpt-x默认"]);
    expect(listbox(el, "讨论串 · Codex").closest(".el-select-dropdown")!.textContent).toContain("管理我的模型");
    expect(card().text()).toContain("跟随岗位配置（岗位：Sonnet、Opus 5.5）");
    pick(el, "讨论串 · Claude Code", "Sonnet");
    pick(el, "讨论串 · Codex", "gpt-x");
    await flushPromises();
    expect(card().text()).toContain("项目指定：Sonnet");
    await submit(w);
    const body = (await calls(spy, "POST", /\/api\/projects$/)[0]!.json()) as Record<string, unknown>;
    expect([body.coordinator_model, body.thread_model, body.thread_codex_model]).toEqual(["", "claude-sonnet-x", "gpt-x"]);
  }, 15000);

  it("new project: no Codex field without Codex models or roles", async () => {
    const { w } = await mountNew(claudeOnly);
    await w.findAll("button").find((b) => b.text() === "按类型指定")!.trigger("click");
    const card = w.get("[data-testid=new-project-models]");
    expect(card.find("input[aria-label='讨论串 · Claude Code']").exists()).toBe(true);
    expect(card.find("input[aria-label='讨论串 · Codex']").exists()).toBe(false);
  }, 15000);

  it("project settings: shows what is in effect per row and saves a model per type", async () => {
    const project = { ...settingsProject, coordinator_model: "claude-opus-5-5", thread_model: "", thread_codex_model: "" } as never;
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: teamRoles, warnings: [] }],
      ["GET", /\/api\/models$/, { models: withCodex }],
      ["PATCH", /\/api\/projects\/p1$/, project],
    ]);
    const w = mountWith(ModelFields, { project });
    await flushPromises();
    expect(w.get("[data-testid=model-row-coordinator]").text()).toContain("项目指定：Opus 5.5");
    expect(w.get("[data-testid=model-row-claude]").text()).toContain("跟随岗位配置（岗位：Sonnet、Opus 5.5）");
    expect(w.get("[data-testid=model-row-codex]").text()).toContain("跟随岗位配置（没有这类岗位）");
    expect(optionTexts(w.element, "讨论串 Codex 模型")).toEqual(["跟随岗位配置", "gpt-x默认"]);
    expect(optionTexts(w.element, "统筹模型")).toEqual(["跟随岗位配置", "Opus 5.5默认", "Sonnet"]);
    pick(w.element, "讨论串 Codex 模型", "gpt-x");
    await flushPromises();
    pick(w.element, "统筹模型", "跟随岗位配置");
    await flushPromises();
    const bodies = await Promise.all(calls(spy, "PATCH", /\/api\/projects\/p1$/).map((r) => r.json()));
    expect(bodies).toEqual([{ thread_codex_model: "gpt-x" }, { coordinator_model: "" }]);
  }, 15000);

  it("project settings: hides the Codex row without Codex models or roles", async () => {
    api([
      ["GET", /\/api\/roles$/, { roles: teamRoles, warnings: [] }],
      ["GET", /\/api\/models$/, { models: claudeOnly }],
    ]);
    const w = mountWith(ModelFields, { project: { ...settingsProject, thread_codex_model: "" } as never });
    await flushPromises();
    expect(w.find("[data-testid=model-row-codex]").exists()).toBe(false);
  }, 15000);
});
