// Workflows (流程图) page logic: drafts, presets, layout and the Vue Flow shapes. Kept free of
// Vue so it is unit-tested (flow.test.ts).
import type { FlowDraft, FlowEdge, FlowStep, FlowView, RoleView } from "@/api/views";

export type StepKind = NonNullable<FlowStep["kind"]>;
export type EdgeKind = NonNullable<FlowEdge["kind"]>;

export const STEP_KINDS: { value: StepKind; label: string; hint: string }[] = [
  { value: "work", label: "做事", hint: "这个岗位完成这一步" },
  { value: "review", label: "审查", hint: "检查上一步的结果，不通过可以退回" },
  { value: "decision", label: "决策", hint: "拍板选方案，不通过可以退回" },
  { value: "approval", label: "等我确认", hint: "组长问你，你确认后才继续" },
];

export const EDGE_KINDS: { value: EdgeKind; label: string }[] = [
  { value: "next", label: "然后" },
  { value: "branch", label: "条件分支" },
  { value: "reject", label: "不通过退回" },
];

export const LEAD = "lead";
const COL = 240;
const ROW = 130;

/** A role a step may name: the team's member roles, then the lead. */
export interface RoleChoice {
  value: string;
  label: string;
}

export function roleChoices(roles: RoleView[]): RoleChoice[] {
  const members = roles.filter((r) => r.tier === "member").map((r) => ({ value: r.role, label: r.title || r.role }));
  return [...members, { value: LEAD, label: "组长" }];
}

export function roleLabel(role: string, choices: RoleChoice[]): string {
  if (!role) return "我";
  return choices.find((c) => c.value === role)?.label ?? `${role}（已不在团队）`;
}

export function kindLabel(kind: FlowStep["kind"]): string {
  return STEP_KINDS.find((k) => k.value === (kind ?? "work"))?.label ?? "做事";
}

/** A fresh step id: s1, s2… not yet in the flow. */
export function newStepId(steps: FlowStep[]): string {
  const used = new Set(steps.map((s) => s.id));
  for (let i = 1; ; i++) if (!used.has(`s${i}`)) return `s${i}`;
}

/** Edge identity (one edge per from, to and kind). */
export function edgeKey(e: FlowEdge): string {
  return `${e.from}->${e.to}:${e.kind ?? "next"}`;
}

/** Adds an edge unless it would point at its own step or repeat one already there. */
export function addEdge(edges: FlowEdge[], e: FlowEdge): FlowEdge[] {
  if (e.from === e.to || edges.some((x) => edgeKey(x) === edgeKey(e))) return edges;
  return [...edges, e];
}

/** Removes a step and every edge touching it. */
export function removeStep(d: FlowDraft, id: string): FlowDraft {
  return { ...d, steps: (d.steps ?? []).filter((s) => s.id !== id), edges: (d.edges ?? []).filter((e) => e.from !== id && e.to !== id) };
}

/**
 * Lays the steps out left to right by depth along the next and branch edges (a step sits one column after
 * its deepest predecessor; parallel steps share a column, one per row). Steps on a cycle keep
 * the depth they reached first.
 */
export function layout(steps: FlowStep[], edges: FlowEdge[]): FlowStep[] {
  const next = edges.filter((e) => (e.kind ?? "next") !== "reject");
  const depth = new Map<string, number>();
  const visit = (id: string, d: number, path: Set<string>) => {
    if (path.has(id) || (depth.get(id) ?? -1) >= d) return;
    depth.set(id, d);
    path.add(id);
    for (const e of next) if (e.from === id) visit(e.to, d + 1, path);
    path.delete(id);
  };
  const roots = steps.filter((s) => !next.some((e) => e.to === s.id));
  for (const s of roots.length ? roots : steps.slice(0, 1)) visit(s.id, 0, new Set());
  const rows = new Map<number, number>();
  return steps.map((s) => {
    const d = depth.get(s.id) ?? 0;
    const r = rows.get(d) ?? 0;
    rows.set(d, r + 1);
    return { ...s, x: d * COL, y: r * ROW };
  });
}

/** Ready-made flows; role keys fall back to the first member role when the team lacks one. */
export function presets(roles: RoleView[]): { label: string; draft: FlowDraft }[] {
  const keys = roles.filter((r) => r.tier === "member").map((r) => r.role);
  const pick = (want: string) => (keys.includes(want) ? want : (keys[0] ?? LEAD));
  const step = (id: string, name: string, role: string, kind: StepKind = "work", instructions = ""): FlowStep => ({ id, name, role, kind, instructions, x: 0, y: 0 });
  const dev: FlowDraft = {
    name: "开发 → 测试 → 审查",
    steps: [
      step("dev", "开发", pick("dev"), "work", "按任务改代码并补上测试"),
      step("test", "测试", pick("qa"), "work", "跑全量测试，复现并记录问题"),
      step("review", "审查", pick("reviewer"), "review", "只读审查改动，不通过写清要改的点"),
      step("pr", "合并开 PR", LEAD, "work"),
    ],
    edges: [
      { from: "dev", to: "test", kind: "next" },
      { from: "test", to: "review", kind: "next" },
      { from: "review", to: "pr", kind: "next" },
      { from: "review", to: "dev", kind: "reject", label: "有问题" },
    ],
  };
  const parallel: FlowDraft = {
    name: "设计 → 并行开发 → 汇合",
    steps: [
      step("design", "方案设计", pick("designer"), "work", "出方案和接口约定"),
      step("ok", "确认方案", "", "approval"),
      step("fe", "前端", pick("dev")),
      step("be", "后端", pick("dev")),
      step("join", "联调验收", pick("qa"), "review"),
    ],
    edges: [
      { from: "design", to: "ok", kind: "next" },
      { from: "ok", to: "fe", kind: "next" },
      { from: "ok", to: "be", kind: "next" },
      { from: "fe", to: "join", kind: "next" },
      { from: "be", to: "join", kind: "next" },
    ],
  };
  const blank: FlowDraft = { name: "新流程", steps: [step("s1", "第一步", pick("dev"))], edges: [] };
  return [dev, parallel, blank].map((d) => ({ label: d.name, draft: { ...d, steps: layout(d.steps ?? [], d.edges ?? []) } }));
}

/** The editable copy of a saved flow. */
export function draftOf(f: FlowView): FlowDraft {
  return { name: f.name, description: f.description, is_default: f.is_default, steps: clone(f.steps ?? []), edges: clone(f.edges ?? []) };
}

/** A name not used by the user's other flows ("新流程", "新流程 2"…). */
export function freeName(base: string, flows: FlowView[]): string {
  const used = new Set(flows.map((f) => f.name));
  if (!used.has(base)) return base;
  for (let i = 2; ; i++) if (!used.has(`${base} ${i}`)) return `${base} ${i}`;
}

/** Steps no next edge leads to and that lead nowhere, in a flow of more than one step. */
export function looseSteps(d: FlowDraft): string[] {
  const steps = d.steps ?? [];
  if (steps.length < 2) return [];
  const edges = d.edges ?? [];
  return steps.filter((s) => !edges.some((e) => e.from === s.id || e.to === s.id)).map((s) => s.name);
}

/** Branch edges still missing their condition (the server refuses them). */
export function unlabeledBranches(d: FlowDraft): number {
  return (d.edges ?? []).filter((e) => e.kind === "branch" && !(e.label ?? "").trim()).length;
}

/** A deep copy that also works on Vue's reactive proxies (structuredClone refuses them). */
function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T;
}

/** The project pick meaning "no flow at all" (alone in at_projects.workflows). */
export const NO_FLOW = "none";

/**
 * A project's flow picks after the multi-select changed from prev to next: choosing 不用流程 clears
 * the flows, choosing a flow drops 不用流程; at most 10.
 */
export function pickFlows(next: string[], prev: string[]): string[] {
  const added = next.filter((v) => !prev.includes(v));
  if (added.includes(NO_FLOW)) return [NO_FLOW];
  return next.filter((v) => v !== NO_FLOW).slice(0, 10);
}
