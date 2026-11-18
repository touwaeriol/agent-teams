// Ended threads stay in the office for a moment (shared-members.md §9): the table fades out while
// its lead stands up and walks out (`threadEnded` sequence). The roster may already have dropped
// the lead, so it is kept from the previous roster until the fade is over.
import { onBeforeUnmount, shallowRef, watch, type ShallowRef } from "vue";
import { visibleThreads } from "./compile";
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

/** Tracks the threads that just ended for `fadeMs`; read `.value` for the current set. */
export function useLeaving(
  layout: () => OfficeLayout,
  threads: () => OfficeThread[],
  employees: () => OfficeEmployee[],
): ShallowRef<Map<number, LeavingThread>> {
  const leaving = shallowRef(new Map<number, LeavingThread>());
  const timers = new Set<ReturnType<typeof setTimeout>>();
  const drop = (no: number) => {
    const next = new Map(leaving.value);
    next.delete(no);
    leaving.value = next;
  };
  watch([threads, employees] as const, ([nextThreads], [prevThreads, prevEmployees]) => {
    const ended = endedThreads(layout(), prevThreads, nextThreads).filter((t) => !leaving.value.has(t.no));
    if (!ended.length) return;
    const fadeMs = layout().threadPods.fadeMs ?? 3000;
    const next = new Map(leaving.value);
    for (const t of ended) {
      next.set(t.no, { thread: t, leads: prevEmployees.filter((e) => e.tier === "lead" && e.thread_no === t.no), since: performance.now() });
      const timer = setTimeout(() => {
        timers.delete(timer);
        drop(t.no);
      }, fadeMs);
      timers.add(timer);
    }
    leaving.value = next;
  });
  onBeforeUnmount(() => timers.forEach(clearTimeout));
  return leaving;
}
