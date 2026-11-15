// Role template helpers for the Team page (pure, unit-tested).
import type { RoleView } from "@/api/views";

export const LEVEL: Record<RoleView["tier"], { label: string; bg: string; fg: string }> = {
  coordinator: { label: "项目级", bg: "#F4E3D7", fg: "#8A3D14" },
  lead: { label: "讨论串级", bg: "#E3ECFA", fg: "#1F4E9E" },
  member: { label: "成员", bg: "#E6F2EA", fg: "#1F5E3A" },
};

const TIER_ORDER = { coordinator: 0, lead: 1, member: 2 } as const;

export function sortRoles(list: RoleView[]): RoleView[] {
  return list.slice().sort((a, b) => TIER_ORDER[a.tier] - TIER_ORDER[b.tier] || a.sort - b.sort);
}

export function roleName(r: Pick<RoleView, "tier" | "title" | "role">): string {
  if (r.tier === "coordinator") return r.title || "项目统筹";
  if (r.tier === "lead") return r.title || "组长";
  return r.title || r.role;
}

export function countText(r: Pick<RoleView, "tier" | "count" | "gate">): string {
  if (r.tier === "coordinator") return "每个项目 1 个";
  if (r.tier === "lead") return "每个讨论串 1 个";
  return `每个讨论串 ${r.count} 个${r.gate ? "（可选）" : ""}`;
}

/** read_only defaults by gate (api.md §2 RoleView). */
export function isReadOnly(r: Pick<RoleView, "read_only" | "gate">): boolean {
  return r.read_only ?? (r.gate === "review" || r.gate === "decision");
}

export function ruleText(r: Pick<RoleView, "tier" | "runtime" | "sandbox" | "read_only" | "gate">): string {
  if (r.tier === "coordinator") return "只能用：开讨论串、看讨论串、给讨论串发话、问你。没有仓库、不能读写文件或执行命令（平台强制）。";
  if (r.tier === "lead") return "可以只读查看代码来拆任务，不能修改文件；合并成员分支、开 PR 由平台代为执行。";
  if (isReadOnly(r)) return "只读：git diff / log / show、gh pr view，不改代码。";
  if (r.runtime === "codex") return `沙箱：${r.sandbox || "workspace-write"}；权限设成“直接执行”时沙箱完全放开。`;
  return "可以给其他成员发消息、问组长、问你；不能直接推送基准分支。";
}

export type PermissionMode = "bypass" | "ask";

export const PERMISSION_MODES: { id: PermissionMode; label: string }[] = [
  { id: "bypass", label: "直接执行（默认）" },
  { id: "ask", label: "逐条问" },
];

/** The role's mode; anything but ask (including the old auto) is bypass. */
export function permissionMode(r: Pick<RoleView, "permission_mode">): PermissionMode {
  return r.permission_mode === "ask" ? "ask" : "bypass";
}

const MODE_HINT: Record<"claude" | "codex", Record<PermissionMode, string>> = {
  claude: {
    bypass: "员工直接执行，不弹权限确认。",
    ask: "除了读文件，每个操作都先问（组长或你，看项目的审批方式）。",
  },
  codex: {
    bypass: "员工直接执行，不弹权限确认（沙箱完全放开）。",
    ask: "除了它信任的只读命令，每个命令都先申请（untrusted）。",
  },
};

/** One line on what the role's permission mode does. */
export function permissionHint(r: Pick<RoleView, "permission_mode" | "runtime">): string {
  return MODE_HINT[r.runtime][permissionMode(r)];
}

/** Whether any writing member role asks for permission (only then approval routing matters). */
export function usesAsk(list: Pick<RoleView, "tier" | "permission_mode" | "read_only" | "gate">[]): boolean {
  return list.some((r) => r.tier === "member" && !isReadOnly(r) && permissionMode(r) === "ask");
}

export function kindText(r: Pick<RoleView, "runtime" | "model">): string {
  const base = r.runtime === "codex" ? "Codex" : "Claude Code";
  return r.model ? `${base} · ${r.model}` : base;
}

/** The name tags the preview shows: lead-1 / dev-1, dev-2 … */
export function previewTags(r: Pick<RoleView, "tier" | "role">): [string, string] {
  if (r.tier === "coordinator") return ["leader", "leader"];
  const key = r.tier === "lead" ? "lead" : r.role;
  return [`${key}-1`, `${key}-2`];
}

/** A unique role key like role-2 for a new role. */
export function nextRoleKey(list: Pick<RoleView, "role">[]): string {
  const taken = new Set(list.map((r) => r.role));
  let n = 1;
  while (taken.has(`role-${n}`)) n++;
  return `role-${n}`;
}

export const ROLE_KEY = /^[a-z][a-z0-9-]{0,23}$/;

export function newRole(list: RoleView[], color: string): RoleView {
  return {
    id: "",
    tier: "member",
    role: nextRoleKey(list),
    title: "新岗位",
    runtime: "claude",
    count: 1,
    node_ids: [],
    description: "",
    instructions: "",
    model: "",
    effort: "",
    permission_mode: "bypass",
    can_delegate: false,
    read_only: null,
    allowed_tools: [],
    disallowed_tools: [],
    sandbox: "",
    max_turns: null,
    gate: "",
    task_kinds: [],
    skills: [],
    plugins: [],
    look: { color, outfit: "tee", accessory: "none", hair: null },
    sort: list.length,
  };
}

/** The coordinator only has platform tools, so it cannot load skills or plugins (the server refuses too). */
export function canHaveSkills(r: Pick<RoleView, "tier">): boolean {
  return r.tier !== "coordinator";
}

export function toggleSkill(skills: string[] | null | undefined, name: string): string[] {
  const list = skills ?? [];
  return list.includes(name) ? list.filter((s) => s !== name) : [...list, name];
}

/** A preset (GET /api/roles/presets) as a new role of this draft: no id, a free role key, last in order. */
export function fromPreset(list: RoleView[], preset: RoleView): RoleView {
  const taken = new Set(list.map((r) => r.role));
  let role = preset.role;
  for (let n = 2; taken.has(role); n++) role = `${preset.role}-${n}`;
  return { ...preset, id: "", role, sort: list.length, skills: [...(preset.skills ?? [])], plugins: [...(preset.plugins ?? [])] };
}

/** Local validation mirroring PUT /api/roles (the server is the authority). */
export function roleProblems(list: RoleView[]): string[] {
  const problems: string[] = [];
  if (list.filter((r) => r.tier === "coordinator").length !== 1) problems.push("需要恰好一个项目统筹");
  if (list.filter((r) => r.tier === "lead").length !== 1) problems.push("需要恰好一个组长");
  const keys = list.map((r) => r.role);
  keys.forEach((k, i) => {
    if (!ROLE_KEY.test(k)) problems.push(`岗位键“${k}”只能用小写字母、数字和 -，以字母开头`);
    if (keys.indexOf(k) !== i) problems.push(`岗位键“${k}”重复了`);
  });
  list.filter((r) => r.tier === "member" && (r.count < 1 || r.count > 8)).forEach((r) => problems.push(`${roleName(r)} 的数量要在 1–8 之间`));
  list.filter((r) => !canHaveSkills(r) && (r.skills ?? []).length > 0).forEach((r) => problems.push(`${roleName(r)} 不能加载技能`));
  list.filter((r) => !canHaveSkills(r) && (r.plugins ?? []).length > 0).forEach((r) => problems.push(`${roleName(r)} 不能加载插件`));
  return problems;
}
