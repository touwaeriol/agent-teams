// Ordering of people (pod seating `order`, staff desk rows): role-template order, instance
// number, name; and who counts as headcount (shared-members.md §2).
import type { EmployeeStatus, OfficeEmployee } from "./types";

const NUM_SUFFIX = /(\d+)$/;

export function instanceOf(e: OfficeEmployee): number {
  if (e.instance !== undefined) return e.instance;
  const m = NUM_SUFFIX.exec(e.name);
  return m ? Number(m[1]) : 1;
}

export function sortMembers(list: OfficeEmployee[], order: string[]): OfficeEmployee[] {
  const keys: Record<string, (a: OfficeEmployee, b: OfficeEmployee) => number> = {
    roleSort: (a, b) => (a.sort ?? 0) - (b.sort ?? 0) || a.role.localeCompare(b.role),
    instance: (a, b) => instanceOf(a) - instanceOf(b),
    name: (a, b) => a.name.localeCompare(b.name),
  };
  return [...list].sort((a, b) => {
    for (const k of order) {
      const r = keys[k]?.(a, b) ?? 0;
      if (r) return r;
    }
    return a.id.localeCompare(b.id);
  });
}

/** Members are the headcount; the coordinator and the leads are management, not counted. */
export const isMember = (e: Pick<OfficeEmployee, "tier">): boolean => e.tier === "member";

/** The member holds a task now (its status names one). */
export const hasTask = (s: EmployeeStatus | null | undefined): boolean => Boolean(s?.task_id);

/** The thread a member works for now (null: none, or not a member with a task). */
export function workThread(e: OfficeEmployee, status: EmployeeStatus | null | undefined): number | null {
  return isMember(e) && hasTask(status) ? e.thread_no ?? null : null;
}
