// 项目编制 (shared-members.md §9): a project's members — the headcount — with the thread and task
// each one works on now, grouped by member role in template order. Pure helpers of StaffPanel.vue.
import type { EmployeeBrief, OfficeSnapshot, RoleView, ThreadView } from "@/api/views";
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
  /** The template's count (what the project is staffed to). */
  count: number;
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
  const state = e.status ? STATE_LABELS[e.status.state] ?? e.status.state : "空闲";
  const thread = threadText(e.thread_no, threads);
  return {
    id: e.id,
    name: e.name,
    role: e.role,
    thread: task || !thread ? thread : `上次在 ${thread}`,
    work: task ? [task, e.status?.bubble].filter(Boolean).join(" · ") : state,
    busy: Boolean(task),
  };
}

/** Member roles of the template (in its order) with the project's members of each; members of roles no longer in the template come last. */
export function staffGroups(snapshot: Pick<OfficeSnapshot, "employees" | "threads">, roles: RoleView[]): StaffGroup[] {
  const members = sortMembers(snapshot.employees.filter((e) => e.tier === "member"), ["instance", "name"], (e) => ({ ...e, instance: e.instance || undefined }));
  const rowsOf = (role: string) => members.filter((e) => e.role === role).map((e) => staffRow(e, snapshot.threads));
  const templ = roles.filter((r) => r.tier === "member");
  const known = new Set(templ.map((r) => r.role));
  const orphans = [...new Set(members.map((e) => e.role).filter((r) => !known.has(r)))];
  return [
    ...templ.map((r) => ({ role: r.role, title: roleName(r), count: r.count, template: true, rows: rowsOf(r.role) })),
    ...orphans.map((role) => ({ role, title: role, count: 0, template: false, rows: rowsOf(role) })),
  ];
}

/** "加一个员工": the role's headcount + 1 in the draft (saved with the templates). */
export function addEmployee(list: RoleView[], role: string): RoleView[] {
  return list.map((r) => (r.tier === "member" && r.role === role ? { ...r, count: r.count + 1 } : r));
}

export function headcountText(groups: StaffGroup[]): string {
  const now = groups.reduce((n, g) => n + g.rows.length, 0);
  const busy = groups.reduce((n, g) => n + g.rows.filter((r) => r.busy).length, 0);
  return `在编 ${now} 人 · 干活 ${busy} · 空闲 ${now - busy}`;
}
