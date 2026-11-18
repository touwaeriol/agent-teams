// Ordering of people (pod seating `order`, staff desk rows): role-template order, instance
// number, name; and who counts as headcount (shared-members.md §2).
import type { EmployeeStatus, OfficeEmployee } from "./types";

const NUM_SUFFIX = /(\d+)$/;

/** What ordering looks at (an office employee, an API EmployeeBrief…). */
export type Sortable = Pick<OfficeEmployee, "id" | "name" | "role" | "sort" | "instance">;

export function instanceOf(e: Pick<OfficeEmployee, "name" | "instance">): number {
  if (e.instance !== undefined) return e.instance;
  const m = NUM_SUFFIX.exec(e.name);
  return m ? Number(m[1]) : 1;
}

const KEYS: Record<string, (a: Sortable, b: Sortable) => number> = {
  roleSort: (a, b) => (a.sort ?? 0) - (b.sort ?? 0) || a.role.localeCompare(b.role),
  instance: (a, b) => instanceOf(a) - instanceOf(b),
  name: (a, b) => a.name.localeCompare(b.name),
};

/**
 * Sorts people by `order` (`roleSort`, `instance`, `name`), then id: dev-2 before dev-10.
 * `person` picks the employee out of each item (default: the item itself).
 */
export function sortMembers<T>(list: T[], order: string[], person: (x: T) => Sortable = (x) => x as Sortable): T[] {
  return [...list].sort((x, y) => {
    const a = person(x);
    const b = person(y);
    for (const k of order) {
      const r = KEYS[k]?.(a, b) ?? 0;
      if (r) return r;
    }
    return a.id.localeCompare(b.id);
  });
}

/** The default seating order: role-template order, then instance number. */
export const MEMBER_ORDER = ["roleSort", "instance"];

/** Members are the headcount; the coordinator and the leads are management, not counted. */
export const isMember = (e: Pick<OfficeEmployee, "tier">): boolean => e.tier === "member";

/** The member holds a task now (its status names one). */
export const hasTask = (s: EmployeeStatus | null | undefined): boolean => Boolean(s?.task_id);

/** The thread a member works for now (null: none, or not a member with a task). */
export function workThread(e: OfficeEmployee, status: EmployeeStatus | null | undefined): number | null {
  return isMember(e) && hasTask(status) ? e.thread_no ?? null : null;
}
