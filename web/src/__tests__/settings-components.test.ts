// @vitest-environment jsdom
// Settings screens on Vue: the repo list and picker, schedules, tag input, number settings, the
// team draft saved in display order and project settings switches, asserted through the API client.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, ref, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import RepoList from "@/components/app/repo/RepoList.vue";
import type { RepoRef } from "@/components/app/repo/repo";
import ScheduleForm from "@/components/app/schedules/ScheduleForm.vue";
import ScheduleRow from "@/components/app/schedules/ScheduleRow.vue";
import { emptyDraft } from "@/components/app/schedules/schedule";
import ScheduleNoteLine from "@/components/app/ScheduleNoteLine.vue";
import ThreadWakeups from "@/components/app/thread/ThreadWakeups.vue";
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
// Registered after the cleanup above so it runs first: unmount every wrapper (Element Plus selects
// keep listeners that would otherwise fire after the environment is torn down).
enableAutoUnmount(afterEach);

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
  it("asks to connect GitHub or add a Git repository when there is neither", async () => {
    api([["GET", /\/api\/git\/connections$/, []], ["GET", /\/api\/git\/repos$/, []]]);
    const w = mountWith(RepoList, { modelValue: [] });
    await flushPromises();
    expect(w.text()).toContain("还没有可用的仓库。先去连接 GitHub 或添加 Git 仓库");
  });
  it("lists a standalone repository with its source and the push-branch tag", async () => {
    api([
      ["GET", /\/api\/git\/connections$/, []],
      ["GET", /\/api\/git\/repos$/, [{ id: "gr_1", slug: "gitlab.com/acme/web", name: "web", provider: "gitlab", url: "https://gitlab.com/acme/web.git", access: "write", projects: [] }]],
    ]);
    const value = ref<RepoRef[]>([{ repo: "gitlab.com/acme/web", git_connection_id: null, git_repo_id: "gr_1" }]);
    const w = mountWith(RepoList, { modelValue: value.value });
    await flushPromises();
    expect(w.get("li").text()).toBe("1gitlab.com/acme/web推送分支交付GitLab · 单独添加");
    expect(w.text()).toContain("再加一个仓库");
  });
});

describe("ScheduleForm", () => {
  it("saves a new schedule with the browser time zone, enabled", async () => {
    const spy = api([
      ["POST", /\/api\/projects\/p1\/schedules$/, { id: "s1" }],
      ["GET", /\/api\/projects\/p1\/threads$/, []],
    ]);
    const draft = ref(emptyDraft());
    const Host = defineComponent(() => () => h(ScheduleForm, { pid: "p1", modelValue: draft.value, "onUpdate:modelValue": (v: typeof draft.value) => (draft.value = v), onDone: () => (draft.value.name = "done") }));
    const w = mountWith(Host);
    const submit = () => w.get("button[type=submit]");
    expect(submit().attributes("disabled")).toBeDefined();
    await w.get("input").setValue("日报");
    await w.get("textarea").setValue("汇总进展");
    await w.get(".at-mono input").setValue("0 9 * * 1-5");
    expect(w.findAll(".el-select")[1].text()).toContain("工作日 9:00");
    await w.get("form").trigger("submit");
    await flushPromises();
    const [req] = calls(spy, "POST", /schedules$/);
    const body = await req.json();
    expect(body).toMatchObject({ name: "日报", kind: "cron", cron: "0 9 * * 1-5", message: "汇总进展", enabled: true, timezone: expect.any(String) });
    expect(body.thread_no).toBeUndefined();
    expect(draft.value.name).toBe("done");
  });
});

const schedule = {
  id: "sc_1", project_id: "p1", name: "看 CI", kind: "once", cron: "", run_at: "2026-11-23T07:00:00Z", timezone: "UTC", message: "看结果",
  target: "member", thread_id: "th1", thread_no: 3, thread_title: "发版", employee_id: "e1", employee: "dev-1", task_id: "t2",
  created_by: "member", ends_at: null, max_runs: null, run_count: 0, enabled: true, status: "active", next_run_at: "2026-11-23T07:00:00Z",
  last_run_at: null, last_error: "", ended_at: null, created_at: "2026-11-22T07:00:00Z", updated_at: "2026-11-22T07:00:00Z",
};

describe("schedule wake-ups", () => {
  it("lists a schedule's runs from the row", async () => {
    api([["GET", /\/api\/schedules\/sc_1\/runs$/, [
      { id: 2, schedule_id: "sc_1", planned_at: "2026-11-23T07:00:00Z", fired_at: "2026-11-23T07:00:01Z", manual: false, outcome: "skipped", message_id: null, error: "上一次的消息还没送到，这次合并跳过" },
      { id: 1, schedule_id: "sc_1", planned_at: null, fired_at: "2026-11-22T08:00:00Z", manual: true, outcome: "delivered", message_id: 9, error: "" },
    ]]]);
    const w = mountWith(ScheduleRow, { pid: "p1", item: schedule });
    expect(w.text()).toContain("发给dev-1（#3 任务 t2）");
    expect(w.text()).toContain("员工设置");
    await w.get("button[aria-label=运行记录]").trigger("click");
    await flushPromises();
    const runs = w.get("[data-schedule-runs]").text();
    expect(runs).toContain("跳过");
    expect(runs).toContain("还没送到");
    expect(runs).toContain("手动运行");
  });

  it("shows the thread's pending wake-ups and deletes one", async () => {
    const spy = api([
      ["GET", /\/api\/projects\/p1\/schedules$/, [schedule, { ...schedule, id: "sc_2", name: "别的", thread_id: "th9" }, { ...schedule, id: "sc_3", name: "完了", status: "done" }]],
      ["DELETE", /\/api\/schedules\/sc_1$/, {}],
    ]);
    const w = mountWith(ThreadWakeups, { pid: "p1", threadId: "th1" });
    await flushPromises();
    const box = w.get("[data-thread-wakeups]");
    expect(box.text()).toContain("看 CI");
    expect(box.text()).not.toContain("别的");
    expect(box.text()).not.toContain("完了");
    await w.get("button[aria-label='取消 看 CI']").trigger("click");
    await flushPromises();
    expect(calls(spy, "DELETE", /schedules\/sc_1$/)).toHaveLength(1);
  });

  it("undoes a schedule an agent just set from its line", async () => {
    const spy = api([["GET", /\/api\/projects\/p1\/schedules$/, [schedule]], ["DELETE", /\/api\/schedules\/sc_1$/, {}]]);
    const message = { id: "5", role: "system", author: "platform", kind: "note", content: "dev-1设置了定时任务「看 CI」", created_at: "2026-11-22T07:00:00Z",
      meta: { schedule_note: true, schedule_id: "sc_1", schedule_undo: true } };
    const w = mountWith(ScheduleNoteLine, { pid: "p1", message });
    await flushPromises();
    await w.get("[data-schedule-note] button").trigger("click");
    await flushPromises();
    expect(calls(spy, "DELETE", /schedules\/sc_1$/)).toHaveLength(1);
    const cancelled = mountWith(ScheduleNoteLine, { pid: "p1", message: { ...message, meta: { schedule_note: true, schedule_id: "sc_1", schedule_undo: false } } });
    await flushPromises();
    expect(cancelled.find("[data-schedule-note] button").exists()).toBe(false);
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
    // A new member role changes 编制: saving asks first.
    const dialog = document.querySelector("[role=alertdialog]") as HTMLElement;
    expect(dialog.textContent).toContain("0→1");
    [...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存")!.click();
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

  it("has no model list card: models are typed in each role", async () => {
    api([["GET", /\/api\/roles$/, { roles: [], warnings: [] }]]);
    const w = mountWith(Team);
    await flushPromises();
    expect(w.find("[data-testid=model-list-editor]").exists()).toBe(false);
    expect(document.getElementById("my-models")).toBeNull();
    expect(w.text()).not.toContain("我的默认");
    expect(w.text()).not.toContain("我的模型");
  });
});

describe("Team · role model field", () => {
  const roles = () => [
    { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", title: "项目统筹", sort: 0 },
    { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", title: "组长", model: "claude-lead-x", sort: 1 },
    { ...newRole([], "#333"), id: "r2", role: "dev", title: "开发", model: "claude-sonnet-5-5", sort: 2 },
    { ...newRole([], "#444"), id: "r3", role: "rev", title: "审查", runtime: "codex" as const, sort: 3 },
  ];
  async function mountTeam() {
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: roles(), warnings: [] }],
      ["PUT", /\/api\/roles$/, { roles: [], warnings: [] }],
      ["GET", /\/api\/(skills|plugins|nodes|roles\/presets)$/, {}],
    ]);
    const w = mountWith(Team);
    await flushPromises();
    return { w, spy };
  }
  const cards = (w: ReturnType<typeof mountWith>) => w.findAll("article");
  const modelInput = (card: ReturnType<ReturnType<typeof mountWith>["findAll"]>[number]) => card.get<HTMLInputElement>("input[aria-label=模型]");
  /** The option texts of a card's select (by aria-label), from its listbox. */
  function options(card: ReturnType<ReturnType<typeof mountWith>["findAll"]>[number], label: string) {
    const listbox = document.getElementById(card.get(`input[aria-label=${label}]`).attributes("aria-controls")!)!;
    return [...listbox.querySelectorAll<HTMLElement>(".el-select-dropdown__item")];
  }
  async function type(card: ReturnType<ReturnType<typeof mountWith>["findAll"]>[number], value: string) {
    const input = modelInput(card);
    await input.setValue(value);
    await input.trigger("keyup", { key: "Enter" });
    await flushPromises();
  }

  it("gives every tier an editable model box with the built-in default as placeholder", async () => {
    const { w } = await mountTeam();
    const [coordinator, lead, dev, rev] = cards(w);
    for (const c of [coordinator!, lead!, dev!]) {
      expect(modelInput(c).attributes("placeholder")).toBe("留空 = claude-opus-5-5");
    }
    expect(modelInput(rev!).attributes("placeholder")).toBe("留空 = 默认");
    expect(modelInput(coordinator!).element.value).toBe("");
    expect(modelInput(lead!).element.value).toBe("claude-lead-x");
    expect(modelInput(dev!).element.value).toBe("claude-sonnet-5-5");
    expect(w.find("[data-testid=role-model-fixed]").exists()).toBe(false);
    expect(w.text()).not.toContain("我的默认");
  }, 15000);

  it("suggests the roles' models and the common ids for the card's type", async () => {
    const { w } = await mountTeam();
    await modelInput(cards(w)[0]!).trigger("focus");
    await flushPromises();
    await new Promise((r) => setTimeout(r, 400)); // el-autocomplete debounces the fetch
    await flushPromises();
    const items = [...document.querySelectorAll<HTMLElement>(".at-model-picker li .truncate")].map((el) => el.textContent!.trim());
    expect(items).toEqual(["claude-lead-x", "claude-sonnet-5-5", "claude-opus-5-5", "claude-fable-5-2"]);
    expect(items.some((t) => t.startsWith("gpt"))).toBe(false);
  }, 15000);

  it("saves what is in the box when 保存 is clicked right after typing (no Enter, no blur)", async () => {
    const { w, spy } = await mountTeam();
    await modelInput(cards(w)[0]!).setValue("claude-typed-x");
    const save = w.findAll("button").find((b) => b.text() === "保存")!;
    expect(save.attributes("disabled")).toBeUndefined();
    await save.trigger("click");
    await flushPromises();
    const body = (await calls(spy, "PUT", /\/api\/roles$/)[0]!.json()) as { roles: { role: string; model: string }[] };
    expect(body.roles.find((r) => r.role === "coordinator")!.model).toBe("claude-typed-x");
  }, 15000);

  it("will not save a role model in the wrong format", async () => {
    const { w, spy } = await mountTeam();
    await modelInput(cards(w)[1]!).setValue("bad model");
    await flushPromises();
    expect(cards(w)[1]!.find("[data-testid=model-input-error]").exists()).toBe(true);
    const save = w.findAll("button").find((b) => b.text() === "保存")!;
    expect(save.attributes("disabled")).toBeDefined();
    expect(w.text()).toContain("组长 的模型 ID 只能用");
    await save.trigger("click");
    await flushPromises();
    expect(calls(spy, "PUT", /\/api\/roles$/)).toHaveLength(0);
  }, 15000);

  it("saves typed models for the coordinator, the lead and a member, refuses a bad id, and keeps each agent's model", async () => {
    const info = vi.spyOn(toast, "info").mockImplementation(() => {});
    const { w, spy } = await mountTeam();
    await type(cards(w)[0]!, " claude-fable-5-2 ");
    await type(cards(w)[1]!, "");
    await type(cards(w)[2]!, "bad model");
    expect(cards(w)[2]!.find("[data-testid=model-input-error]").exists()).toBe(true);
    await type(cards(w)[2]!, "us.anthropic.claude-x:0[1m]");
    expect(cards(w)[2]!.find("[data-testid=model-input-error]").exists()).toBe(false);
    // rev (codex, ""): add Claude Code → a model box per agent; type the Claude one, then drop Codex
    options(cards(w)[3]!, "agent").find((o) => o.textContent!.includes("Claude Code"))!.click();
    await flushPromises();
    const boxes = cards(w)[3]!.findAll<HTMLInputElement>("input[aria-label=模型]");
    expect(boxes).toHaveLength(2);
    expect(cards(w)[3]!.text()).toContain("每次开新会话用剩余额度最多的");
    await boxes[1]!.setValue("claude-sonnet-5-5");
    await boxes[1]!.trigger("keyup", { key: "Enter" });
    await flushPromises();
    options(cards(w)[3]!, "agent").find((o) => o.textContent!.includes("Codex"))!.click();
    await flushPromises();
    // dev: add Codex next to Claude Code
    options(cards(w)[2]!, "agent").find((o) => o.textContent!.includes("Codex"))!.click();
    await flushPromises();
    expect(info).not.toHaveBeenCalled();
    await w.findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    const body = (await calls(spy, "PUT", /\/api\/roles$/)[0]!.json()) as { roles: { role: string; runtime: string; runtimes?: string[]; model: string }[] };
    expect(body.roles.map((r) => [r.role, r.runtime, (r.runtimes ?? []).join(","), r.model])).toEqual([
      ["coordinator", "claude", "claude", "claude-fable-5-2"],
      ["lead", "claude", "claude", ""],
      ["dev", "claude", "claude,codex", "us.anthropic.claude-x:0[1m]"],
      ["rev", "claude", "claude", "claude-sonnet-5-5"],
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
  const teamRoles = [
    { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", sort: 0 },
    { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", model: "claude-sonnet-5-5", sort: 1 },
    { ...newRole([], "#333"), id: "r2", role: "dev", sort: 2 },
  ];
  const withCodex = [...teamRoles, { ...newRole([], "#444"), id: "r3", role: "cx", runtime: "codex" as const, model: "gpt-x", sort: 3 }];
  const created = { id: "p9", name: "新项目" };
  async function typeIn(root: ReturnType<typeof mountWith>, label: string, value: string) {
    const input = root.get(`input[aria-label='${label}']`);
    await input.setValue(value);
    await input.trigger("keyup", { key: "Enter" });
    await flushPromises();
  }

  async function mountNew(roles: unknown[]) {
    const spy = api([
      ["GET", /\/api\/roles$/, { roles, warnings: [] }],
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
    expect(card.text()).not.toContain("我的默认");
    await submit(w);
    const body = (await calls(spy, "POST", /\/api\/projects$/)[0]!.json()) as Record<string, unknown>;
    expect([body.coordinator_model, body.thread_model, body.thread_codex_model]).toEqual(["", "", ""]);
  }, 15000);

  it("new project: types models for the coordinator and each thread type and sends them", async () => {
    const { w, spy } = await mountNew(withCodex);
    const card = () => w.get("[data-testid=new-project-models]");
    await card().findAll("button").find((b) => b.text() === "按类型指定")!.trigger("click");
    // thread fields cover leads and members: the lead's Sonnet counts
    expect(card().text()).toContain("跟随岗位配置（岗位：Sonnet 5.5、内置默认 Opus 5.5）");
    await typeIn(w, "统筹", "claude-fable-5-2");
    await typeIn(w, "讨论串 · Claude Code", "claude-anything");
    await typeIn(w, "讨论串 · Codex", "gpt-y");
    expect(card().text()).toContain("项目指定：Fable 5.2");
    await submit(w);
    const body = (await calls(spy, "POST", /\/api\/projects$/)[0]!.json()) as Record<string, unknown>;
    expect([body.coordinator_model, body.thread_model, body.thread_codex_model]).toEqual(["claude-fable-5-2", "claude-anything", "gpt-y"]);
  }, 15000);

  it("new project: creating right after typing sends what is in the boxes; a wrong format is not sent", async () => {
    const { w, spy } = await mountNew(withCodex);
    await w.get("[data-testid=new-project-models]").findAll("button").find((b) => b.text() === "按类型指定")!.trigger("click");
    const err = vi.spyOn(toast, "error").mockImplementation(() => {});
    await w.get("input[aria-label='统筹']").setValue("bad id");
    await submit(w);
    expect(err).toHaveBeenCalled();
    expect(calls(spy, "POST", /\/api\/projects$/)).toHaveLength(0);
    await w.get("input[aria-label='统筹']").setValue(" claude-typed-x ");
    await submit(w);
    const body = (await calls(spy, "POST", /\/api\/projects$/)[0]!.json()) as Record<string, unknown>;
    expect(body.coordinator_model).toBe("claude-typed-x");
  }, 15000);

  it("new project: no Codex field without Codex roles", async () => {
    const { w } = await mountNew(teamRoles);
    await w.findAll("button").find((b) => b.text() === "按类型指定")!.trigger("click");
    const card = w.get("[data-testid=new-project-models]");
    expect(card.find("input[aria-label='讨论串 · Claude Code']").exists()).toBe(true);
    expect(card.find("input[aria-label='讨论串 · Codex']").exists()).toBe(false);
  }, 15000);

  it("project settings: coordinator and thread rows show what is in effect and save typed models", async () => {
    const project = { ...settingsProject, coordinator_model: "", thread_model: "", thread_codex_model: "" } as never;
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: withCodex, warnings: [] }],
      ["PATCH", /\/api\/projects\/p1$/, project],
    ]);
    const w = mountWith(ModelFields, { project });
    await flushPromises();
    expect(w.text()).not.toContain("我的默认");
    expect(w.get("[data-testid=model-row-coordinator]").text()).toContain("统筹 · 跟随岗位配置（岗位：内置默认 Opus 5.5）");
    expect(w.get("[data-testid=model-row-claude]").text()).toContain("组长和成员 · 跟随岗位配置（岗位：Sonnet 5.5、内置默认 Opus 5.5）");
    expect(w.get("[data-testid=model-row-codex]").text()).toContain("跟随岗位配置（岗位：gpt-x）");
    await typeIn(w, "统筹模型", "claude-fable-5-2");
    await typeIn(w, "讨论串 Codex 模型", "bad id");
    await typeIn(w, "讨论串 Codex 模型", "gpt-z");
    await typeIn(w, "讨论串 Claude Code 模型", "claude-sonnet-5-5");
    const bodies = await Promise.all(calls(spy, "PATCH", /\/api\/projects\/p1$/).map((r) => r.json()));
    expect(bodies).toEqual([{ coordinator_model: "claude-fable-5-2" }, { thread_codex_model: "gpt-z" }, { thread_model: "claude-sonnet-5-5" }]);
  }, 15000);

  it("project settings: a typed model is saved even when the field goes away right after blur", async () => {
    const project = { ...settingsProject, coordinator_model: "", thread_model: "", thread_codex_model: "" } as never;
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: teamRoles, warnings: [] }],
      ["PATCH", /\/api\/projects\/p1$/, project],
    ]);
    const w = mountWith(ModelFields, { project });
    await flushPromises();
    const input = w.get("input[aria-label='统筹模型']");
    await input.trigger("focus");
    await input.setValue("claude-typed-x");
    await input.trigger("focusout");
    w.unmount();
    await flushPromises();
    const bodies = await Promise.all(calls(spy, "PATCH", /\/api\/projects\/p1$/).map((r) => r.json()));
    expect(bodies).toEqual([{ coordinator_model: "claude-typed-x" }]);
  }, 15000);

  it("project settings: hides the Codex row without Codex roles", async () => {
    api([["GET", /\/api\/roles$/, { roles: teamRoles, warnings: [] }]]);
    const w = mountWith(ModelFields, { project: { ...settingsProject, thread_codex_model: "" } as never });
    await flushPromises();
    expect(w.find("[data-testid=model-row-codex]").exists()).toBe(false);
  }, 15000);
});
