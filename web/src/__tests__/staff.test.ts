// @vitest-environment jsdom
// Shared members on the web (shared-members.md §9): the office top bar, the roster following live
// frames, the snapshot mapping, and the team page's 项目编制 panel with its "加一人".
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import type { StatusFrame } from "@/api/events";
import type { EmployeeBrief, OfficeSnapshot } from "@/api/views";
import { officeRoster, rosterAfter, staffSummary, staffSummaryText, toOfficeEmployees, toWidgetData } from "@/lib/office";
import { newRole } from "@/lib/roles";
import Team from "@/pages/settings/Team.vue";
import { addEmployee, countText, headcountChanges, headcountText, saveImpact, staffGroups, staffRow } from "@/pages/settings/team/staff";
import { thread } from "./fixtures";

function brief(name: string, tier: EmployeeBrief["tier"], role: string, patch: Partial<EmployeeBrief> = {}): EmployeeBrief & { thread_no: number | null } {
  return { id: name, name, tier, role, title: role, runtime: "claude", node: null, look: { color: "#4C7BD0", outfit: "tee", accessory: "none", hair: null }, status: null, thread_no: null, ...patch } as EmployeeBrief & { thread_no: number | null };
}

const busy = (task_id: string, bubble = "") => ({ state: "typing", substate: "", bubble, bubble_kind: "", task_id, task_kind: "", tier: "member", role: "dev", since: "", seq: 1 }) as EmployeeBrief["status"];

const employees = [
  brief("leader", "coordinator", "coordinator"),
  brief("lead-12", "lead", "lead", { thread_no: 12 }),
  brief("lead-13", "lead", "lead", { thread_no: 13 }),
  brief("dev-2", "member", "dev", { thread_no: 12, status: busy("t6", "改 QuestionCard.tsx"), sort: 2, instance: 2 }),
  brief("dev-1", "member", "dev", { thread_no: 13, status: busy("t3"), sort: 2, instance: 1 }),
  brief("qa-1", "member", "qa", { thread_no: 12, sort: 3, instance: 1 }),
];

const snapshot = {
  layout_id: "default", layout_version: "1", threads: [thread(12, { title: "员工通信与审批" }), thread(13, { title: "Codex 员工接入", status: "waiting" }), thread(14, { status: "queued" })],
  employees, board: { claimable: 1, by_thread: {}, queue: [{ thread_no: 12, id: "t7", role: "dev", assignee: "", locked: true, reserved_for: "" }] },
  nodes: [], merge_queue: [], ci: {}, last_event_id: 10,
} as unknown as OfficeSnapshot;

describe("office top bar", () => {
  it("counts the members only: working = holding a task; threads under way, not queued", () => {
    const roster = toOfficeEmployees(snapshot);
    const s = staffSummary(roster, new Map(), snapshot.threads);
    expect(s).toEqual({ headcount: 3, working: 2, idle: 1, threads: 2 });
    expect(staffSummaryText(s)).toBe("编制 3 人 · 干活 2 · 空闲 1 · 讨论串 2 个进行中");
    const later = new Map([["dev-1", { state: "idle" as const }]]);
    expect(staffSummary(roster, later, snapshot.threads)).toMatchObject({ working: 1, idle: 2 });
  });

  it("maps the role order, instance and the queue for the office", () => {
    const dev2 = toOfficeEmployees(snapshot).find((e) => e.id === "dev-2")!;
    expect(dev2).toMatchObject({ sort: 2, instance: 2, title: "dev" });
    expect(toWidgetData(snapshot, null).board?.queue).toEqual([{ thread_no: 12, id: "t7", role: "dev", assignee: "", locked: true, reserved_for: "" }]);
  });

  it("moves a member to the thread a newer status frame names; leads and older frames stay", () => {
    const roster = toOfficeEmployees(snapshot);
    const frames: StatusFrame[] = [
      { seq: 9, at: "", employee: { id: "qa-1", thread_no: 13 }, status: busy("t1")! },
      { seq: 11, at: "", employee: { id: "dev-1", thread_no: 12 }, status: busy("t8")! },
      { seq: 12, at: "", employee: "qa-1", status: busy("t2")! },
      { seq: 13, at: "", employee: { id: "lead-12", thread_no: 13 }, status: busy("")! },
    ];
    const after = rosterAfter(roster, frames, 10);
    expect(after.find((e) => e.id === "dev-1")!.thread_no).toBe(12);
    expect(after.find((e) => e.id === "qa-1")!.thread_no).toBe(12);
    expect(after.find((e) => e.id === "lead-12")!.thread_no).toBe(12);
    expect(rosterAfter(roster, [], 10)).toBe(roster);
  });

  it("a refetched snapshot is not overlaid with frames older than it", () => {
    const frames: StatusFrame[] = [{ seq: 11, at: "", employee: { id: "dev-1", thread_no: 13 }, status: busy("t8")! }];
    const fresh = { ...snapshot, last_event_id: 20, employees: snapshot.employees.map((e) => (e.id === "dev-1" ? { ...e, thread_no: 12 } : e)) };
    expect(officeRoster({ ...snapshot, last_event_id: 10 }, frames).find((e) => e.id === "dev-1")!.thread_no).toBe(13);
    expect(officeRoster(fresh, frames).find((e) => e.id === "dev-1")!.thread_no).toBe(12);
  });
});

const roles = () => [
  { ...newRole([], "#111"), id: "r0", tier: "coordinator" as const, role: "coordinator", title: "项目统筹", sort: 0 },
  { ...newRole([], "#222"), id: "r1", tier: "lead" as const, role: "lead", title: "组长", sort: 1 },
  { ...newRole([], "#333"), id: "r2", role: "dev", title: "开发", count: 2, sort: 2 },
  { ...newRole([], "#444"), id: "r3", role: "qa", title: "测试", count: 1, sort: 3 },
  { ...newRole([], "#555"), id: "r4", role: "review", title: "审查", count: 1, sort: 4 },
];

describe("headcount excludes pre-shared-members per-thread members", () => {
  it("counts only staff members in the team panel and the office top bar", () => {
    const legacy = [1, 2, 3].map((no) => brief(`dev-a-1@${no}`, "member", "dev", { name: "dev-a-1", thread_no: no, staff: false }));
    const staffed = employees.map((e) => (e.tier === "member" ? { ...e, staff: true } : e));
    const withLegacy = { ...snapshot, employees: [...staffed, ...legacy] } as unknown as OfficeSnapshot;
    expect(headcountText(staffGroups(withLegacy, roles()))).toBe("在编 3 人 · 干活 2 · 空闲 1");
    expect(staffSummary(toOfficeEmployees(withLegacy), new Map(), snapshot.threads).headcount).toBe(3);
  });
});

describe("项目编制 helpers", () => {
  it("groups the members by template role with their thread and task", () => {
    const groups = staffGroups(snapshot, roles());
    expect(groups.map((g) => [g.title, g.count, g.rows.map((r) => r.name)])).toEqual([
      ["开发", 2, ["dev-1", "dev-2"]],
      ["测试", 1, ["qa-1"]],
      ["审查", 1, []],
    ]);
    expect(groups[0].rows[1]).toMatchObject({ thread: "#12 员工通信与审批", work: "t6 · 改 QuestionCard.tsx", busy: true });
    expect(groups[1].rows[0]).toMatchObject({ thread: "上次在 #12 员工通信与审批", work: "空闲", busy: false });
    expect(headcountText(groups)).toBe("在编 3 人 · 干活 2 · 空闲 1");
  });

  it("orders a role's members by instance number, from the name when the snapshot has none", () => {
    const devs = [brief("dev-10", "member", "dev"), brief("dev-2", "member", "dev"), brief("dev-1", "member", "dev", { instance: 1 })];
    expect(staffGroups({ ...snapshot, employees: devs }, roles())[0].rows.map((r) => r.name)).toEqual(["dev-1", "dev-2", "dev-10"]);
  });

  it("lists members of a role no longer in the template last, without 加一人", () => {
    const groups = staffGroups({ ...snapshot, employees: [...employees, brief("designer-1", "member", "designer")] }, roles());
    expect(groups.at(-1)).toMatchObject({ role: "designer", template: false, count: 0 });
  });

  it("加一人 raises the member role's count only", () => {
    const next = addEmployee(roles(), "dev");
    expect(next.map((r) => r.count)).toEqual([roles()[0].count, roles()[1].count, 3, 1, 1]);
    expect(addEmployee(roles(), "lead")).toEqual(roles());
    expect(staffRow(brief("x-1", "member", "x"), []).thread).toBe("");
  });

  it("shows the unsaved 编制 change and what saving it does", () => {
    const draft = addEmployee(roles(), "dev").filter((r) => r.role !== "review");
    const dev = staffGroups(snapshot, draft, roles())[0];
    expect(countText(dev)).toBe("编制 2→3 人（未保存）");
    expect(countText(staffGroups(snapshot, roles())[0])).toBe("编制 2 人");
    const changes = headcountChanges(draft, roles());
    expect(changes).toEqual(["开发 2→3", "审查 1→0"]);
    expect(headcountChanges(roles(), roles())).toEqual([]);
    expect(saveImpact(changes, 4)).toContain("会影响 4 个项目");
  });
});

type Route = [method: string, path: RegExp, body: unknown];

function api(routes: Route[]) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const path = new URL(req.url).pathname;
    const hit = routes.find(([m, re]) => m === req.method && re.test(path));
    return new Response(JSON.stringify(hit ? hit[2] : {}), { status: hit ? 200 : 404, headers: { "Content-Type": "application/json" } });
  });
}

describe("Team · 项目编制", () => {
  beforeEach(() => {
    document.cookie = "agent_teams_csrf=tok";
  });
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("shows the project's members with thread and task; 加一人 raises the count and saves it", async () => {
    const spy = api([
      ["GET", /\/api\/roles$/, { roles: roles(), warnings: [] }],
      ["PUT", /\/api\/roles$/, { roles: [], warnings: [] }],
      ["GET", /\/api\/projects$/, [{ id: "p1", name: "web" }]],
      ["GET", /\/api\/projects\/p1\/office$/, snapshot],
      ["GET", /\/api\/(skills|plugins|nodes|roles\/presets)$/, {}],
    ]);
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const w = mount(Team, { global: { plugins: [[VueQueryPlugin, { queryClient }], router, ElementPlus] as never[] }, attachTo: document.body });
    await flushPromises();
    const panel = w.find("#project-staff");
    expect(panel.find("[data-testid=staff-headcount]").text()).toBe("在编 3 人 · 干活 2 · 空闲 1");
    expect(panel.text()).toContain("dev-2");
    expect(panel.text()).toContain("#12 员工通信与审批");
    expect(panel.text()).toContain("t6 · 改 QuestionCard.tsx");
    const dev = panel.find("section[aria-label=开发]");
    expect(dev.text()).toContain("编制 2 人");
    await dev.findAll("button").find((b) => b.text() === "加一人（所有项目）")!.trigger("click");
    expect(w.find("section[aria-label=开发]").text()).toContain("编制 2→3 人（未保存）");
    await w.findAll("button").find((b) => b.text() === "保存")!.trigger("click");
    await flushPromises();
    const dialog = document.querySelector("[role=alertdialog]") as HTMLElement;
    expect(dialog.textContent).toContain("开发 2→3");
    expect(dialog.textContent).toContain("会影响 1 个项目");
    expect(spy.mock.calls.some((c) => (c[0] as Request).method === "PUT")).toBe(false);
    [...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存")!.click();
    await flushPromises();
    const put = spy.mock.calls.map((c) => c[0] as Request).find((r) => r.method === "PUT" && /\/api\/roles$/.test(new URL(r.url).pathname))!;
    const body = (await put.json()) as { roles: { role: string; count: number }[] };
    expect(body.roles.find((r) => r.role === "dev")!.count).toBe(3);
  }, 15000);
});
