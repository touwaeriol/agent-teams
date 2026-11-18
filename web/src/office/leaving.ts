// Ended threads stay in the office for a moment (shared-members.md §9): the table fades out while
// its lead stands up and walks out (`threadEnded` sequence). The roster may already have dropped
// the lead, so it is kept from the previous roster until the fade is over and the lead is out.
import { onScopeDispose, shallowRef, watch, type ShallowRef } from "vue";
import { fadeMsOf, visibleThreads } from "./compile";
import type { OfficeEmployee, OfficeLayout, OfficeThread } from "./types";

export interface LeavingThread {
  thread: OfficeThread;
  leads: OfficeEmployee[];
  /** Animation clock (performance.now) when the thread ended. */
  since: number;
}

/** Threads that had a table before and have none now. */
export function endedThreads(layout: OfficeLayout, prev: OfficeThread[], next: OfficeThread[]): OfficeThread[] {
  const still = new Set(visibleThreads(layout, next, null).map((t) => t.no));
  return visibleThreads(layout, prev, null).filter((t) => !still.has(t.no));
}

/** The scene input with the leaving threads (and their leads) added back. */
export function withLeaving(threads: OfficeThread[], employees: OfficeEmployee[], leaving: Map<number, LeavingThread>) {
  const nos = new Set(threads.map((t) => t.no));
  const ids = new Set(employees.map((e) => e.id));
  const extra = [...leaving.values()].filter((l) => !nos.has(l.thread.no));
  return {
    threads: [...threads, ...extra.map((l) => l.thread)],
    employees: [...employees, ...extra.flatMap((l) => l.leads.filter((e) => !ids.has(e.id)))],
    since: new Map(extra.map((l) => [l.thread.no, l.since] as const)),
  };
}

/** After the fade, how often to check whether the leads are out yet, and for how long at most. */
export const LEAVE_POLL_MS = 250;
export const LEAVE_MAX_POLLS = 120;

/**
 * Tracks the threads that just ended until their table has faded (`fadeMs`) and their leads are
 * out (`hasLeft`, e.g. the Director's: the walk to the door can take longer than the fade).
 * One timer per thread: a thread that resumes leaves the set; one that ends again starts over.
 * Read `.value` for the current set.
 */
export function useLeaving(
  layout: () => OfficeLayout,
  threads: () => OfficeThread[],
  employees: () => OfficeEmployee[],
  hasLeft: (employeeId: string) => boolean = () => true,
): ShallowRef<Map<number, LeavingThread>> {
  const leaving = shallowRef(new Map<number, LeavingThread>());
  const timers = new Map<number, ReturnType<typeof setTimeout>>();
  const stop = (no: number) => {
    clearTimeout(timers.get(no));
    timers.delete(no);
  };
  const drop = (no: number) => {
    const next = new Map(leaving.value);
    next.delete(no);
    leaving.value = next;
  };
  const expire = (no: number, delay: number, polls: number) => {
    const timer = setTimeout(() => {
      const out = leaving.value.get(no)?.leads.every((e) => hasLeft(e.id)) ?? true;
      if (!out && polls > 0) return expire(no, LEAVE_POLL_MS, polls - 1);
      timers.delete(no);
      drop(no);
    }, delay);
    timers.set(no, timer);
  };
  watch([threads, employees] as const, ([nextThreads], [prevThreads, prevEmployees]) => {
    const live = new Set(visibleThreads(layout(), nextThreads, null).map((t) => t.no));
    const resumed = [...leaving.value.keys()].filter((no) => live.has(no));
    const ended = endedThreads(layout(), prevThreads, nextThreads);
    if (!resumed.length && !ended.length) return;
    const next = new Map(leaving.value);
    for (const no of resumed) {
      stop(no);
      next.delete(no);
    }
    for (const t of ended) {
      stop(t.no);
      next.set(t.no, { thread: t, leads: prevEmployees.filter((e) => e.tier === "lead" && e.thread_no === t.no), since: performance.now() });
      expire(t.no, fadeMsOf(layout()), LEAVE_MAX_POLLS);
    }
    leaving.value = next;
  });
  onScopeDispose(() => timers.forEach(clearTimeout));
  return leaving;
}
