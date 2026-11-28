// 项目编制 (shared-members.md §9): a project's members — the headcount — with the thread and task
// each one works on now, grouped by member role in template order. Pure helpers of StaffPanel.vue.
import { statusBubble } from "@/lib/bubble";
import { $t } from "@/i18n";
import type { EmployeeBrief, OfficeSnapshot, RoleView, ThreadView } from "@/api/views";
import { isStaff } from "@/lib/office";
import { roleName } from "@/lib/roles";
import { STATE_LABELS } from "@/office/overlay";
import { sortMembers } from "@/office/people";

export interface StaffRow {
  id: string;
  name: string;
  role: string;
  /** "#12 员工通信与审批" for the thread of its current task, or of its last one when free. */
  thread: string;
  /** "t5 · 改 PlanList.tsx" while it holds a task, else the state ("空闲"). */
  work: string;
  busy: boolean;
}

export interface StaffGroup {
  role: string;
  title: string;
  /** The draft template's count (what the projects get once saved). */
  count: number;
  /** The saved template's count (what the projects are staffed to now; 0 for a new role). */
  saved: number;
  /** Still a role of the template (members of a deleted role are listed but cannot be added to). */
  template: boolean;
  rows: StaffRow[];
}

function threadText(no: number | null | undefined, threads: Pick<ThreadView, "no" | "title">[]): string {
  if (no == null) return "";
  const t = threads.find((x) => x.no === no);
  return t ? `#${no} ${t.title}` : `#${no}`;
}

export function staffRow(e: EmployeeBrief, threads: Pick<ThreadView, "no" | "title">[]): StaffRow {
  const task = e.status?.task_id ?? "";
  const state = e.status ? STATE_LABELS[e.status.state] ?? e.status.state : $t("空闲");
  const thread = threadText(e.thread_no, threads);
  return {
    id: e.id,
    name: e.name,
    role: e.role,
    thread: task || !thread ? thread : $t("上次在 {thread}", { thread }),
    work: task ? [task, statusBubble(e.status?.bubble)].filter(Boolean).join(" · ") : state,
    busy: Boolean(task),
  };
}

const memberCount = (list: RoleView[], role: string): number => list.find((r) => r.tier === "member" && r.role === role)?.count ?? 0;

/**
 * Member roles of the draft template (in its order) with the project's members of each; members of
 * roles no longer in the template come last. `saved`: the saved templates (default: the draft).
 */
export function staffGroups(snapshot: Pick<OfficeSnapshot, "employees" | "threads">, roles: RoleView[], saved: RoleView[] = roles): StaffGroup[] {
  const members = sortMembers(snapshot.employees.filter(isStaff), ["instance", "name"], (e) => ({ ...e, instance: e.instance || undefined }));
  const rowsOf = (role: string) => members.filter((e) => e.role === role).map((e) => staffRow(e, snapshot.threads));
  const templ = roles.filter((r) => r.tier === "member");
  const known = new Set(templ.map((r) => r.role));
  const orphans = [...new Set(members.map((e) => e.role).filter((r) => !known.has(r)))];
  return [
    ...templ.map((r) => ({ role: r.role, title: roleName(r), count: r.count, saved: memberCount(saved, r.role), template: true, rows: rowsOf(r.role) })),
    ...orphans.map((role) => ({ role, title: role, count: 0, saved: memberCount(saved, role), template: false, rows: rowsOf(role) })),
  ];
}

/** "编制 2 人", or "编制 2→3 人（未保存）" while the draft changes it. */
export function countText(g: Pick<StaffGroup, "count" | "saved">): string {
  return g.count === g.saved ? $t("编制 {count} 人", { count: g.count }) : $t("编制 {saved}→{count} 人（未保存）", { saved: g.saved, count: g.count });
}

/** The member roles whose 编制 the draft changes, e.g. "开发 2→3" (added roles from 0, removed ones to 0). */
export function headcountChanges(draft: RoleView[], saved: RoleView[]): string[] {
  const members = (list: RoleView[]) => list.filter((r) => r.tier === "member");
  const roles = [...members(draft), ...members(saved).filter((r) => !memberCount(draft, r.role))];
  return roles
    .filter((r) => memberCount(saved, r.role) !== memberCount(draft, r.role))
    .map((r) => `${roleName(r)} ${memberCount(saved, r.role)}→${memberCount(draft, r.role)}`);
}

/** What saving changed 编制 does: every project gets the new counts. */
export function saveImpact(changes: string[], projects: number): string {
  return $t("{changes}。保存后会影响 {projects} 个项目：每个项目都按新编制增减成员（还没有成员的项目在开下一个讨论串时配齐）。", { changes: changes.join("、"), projects });
}

/** "加一人": the role's 编制 + 1 in the draft (saved with the templates, for every project). */
export function addEmployee(list: RoleView[], role: string): RoleView[] {
  return list.map((r) => (r.tier === "member" && r.role === role ? { ...r, count: r.count + 1 } : r));
}

export function headcountText(groups: StaffGroup[]): string {
  const now = groups.reduce((n, g) => n + g.rows.length, 0);
  const busy = groups.reduce((n, g) => n + g.rows.filter((r) => r.busy).length, 0);
  return $t("在编 {now} 人 · 干活 {busy} · 空闲 {n}", { now, busy, n: now - busy });
}
