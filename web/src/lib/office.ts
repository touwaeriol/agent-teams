// Maps the office snapshot / SSE status frames (api.md §3.11) onto the
// <Office/> renderer's props (web/src/office). Pure, unit-tested.
import type { StatusFrame } from "@/api/events";
import type { CheckView, NodeBrief, OfficeSnapshot, ReplayEvent, ThreadView } from "@/api/views";
import type { EmployeeStatus, OfficeEmployee, OfficeStatusEvent, OfficeThread, OfficeWidgetData } from "@/office";

export function toOfficeEmployees(s: Pick<OfficeSnapshot, "employees">): OfficeEmployee[] {
  return s.employees.map((e) => ({
    id: e.id,
    name: e.name,
    tier: e.tier,
    role: e.role,
    title: e.title,
    thread_no: e.thread_no,
    look: e.look,
    status: (e.status as EmployeeStatus | null) ?? null,
    ...(e.sort === undefined ? {} : { sort: e.sort }),
    ...(e.instance ? { instance: e.instance } : {}),
  }));
}

/**
 * The roster after the live status frames: a frame that names the employee's thread moves it
 * there (a member claiming a task in another thread), so its desk tag and table strip follow.
 */
export function rosterAfter(employees: OfficeEmployee[], frames: StatusFrame[], after: number): OfficeEmployee[] {
  const latest = new Map<string, number | null>();
  for (const f of frames) {
    if (f.seq <= after || typeof f.employee === "string" || f.employee.thread_no === undefined) continue;
    latest.set(f.employee.id, f.employee.thread_no);
  }
  if (!latest.size) return employees;
  return employees.map((e) => (latest.has(e.id) && e.tier === "member" ? { ...e, thread_no: latest.get(e.id)! } : e));
}

export interface StaffSummary {
  headcount: number;
  working: number;
  idle: number;
  threads: number;
}

const RUNNING_THREAD = new Set(["running", "waiting", "review", "paused"]);

/**
 * The office top bar (shared-members.md §9): headcount = the project's members (the coordinator
 * and the leads are management, not counted); working = members holding a task; threads = threads
 * under way (queued ones not yet).
 */
export function staffSummary(employees: OfficeEmployee[], statuses: Map<string, EmployeeStatus>, threads: { status: string }[]): StaffSummary {
  const members = employees.filter((e) => e.tier === "member");
  const working = members.filter((e) => Boolean((statuses.get(e.id) ?? e.status)?.task_id)).length;
  return { headcount: members.length, working, idle: members.length - working, threads: threads.filter((t) => RUNNING_THREAD.has(t.status)).length };
}

export function staffSummaryText(s: StaffSummary): string {
  return `编制 ${s.headcount} 人 · 干活 ${s.working} · 空闲 ${s.idle} · 讨论串 ${s.threads} 个进行中`;
}

export function toOfficeThreads(threads: ThreadView[]): OfficeThread[] {
  return threads.map((t) => ({ no: t.no, title: t.title, status: t.status, updated_at: t.updated_at, counts: { main_done: t.counts.main_done, main_total: t.counts.main_total } }));
}

/** Status frames after the snapshot position, in seq order, as renderer events. */
export function toOfficeEvents(frames: StatusFrame[], after: number): OfficeStatusEvent[] {
  return frames
    .filter((f) => f.seq > after)
    .sort((a, b) => a.seq - b.seq)
    .map((f) => ({ seq: f.seq, at: f.at, employee: typeof f.employee === "string" ? f.employee : f.employee.id, status: f.status as EmployeeStatus }));
}

/** Replay rows of type `status` as renderer events. */
export function replayEvents(rows: ReplayEvent[]): OfficeStatusEvent[] {
  return rows
    .filter((r) => r.type === "status")
    .map((r) => {
      const d = r.data as { employee: StatusFrame["employee"]; status: EmployeeStatus };
      return { seq: r.id, at: r.at, employee: typeof d.employee === "string" ? d.employee : d.employee.id, status: d.status };
    });
}

function toNode(n: NodeBrief) {
  return { id: n.id, name: n.name, online: n.online, capacity: n.slots ?? 0, active: n.active_sessions ?? 0, parked: n.parked_sessions, queued: n.queued };
}

/** CI lights: the focused thread's checks, else the latest thread that has any. */
export function pickChecks(ci: Record<string, CheckView[]>, focus: number | null): CheckView[] {
  if (focus !== null) return ci[String(focus)] ?? [];
  const nos = Object.keys(ci).map(Number).sort((a, b) => b - a);
  return nos.map((n) => ci[String(n)]).find((c) => c?.length) ?? [];
}

export function toWidgetData(s: OfficeSnapshot, focus: number | null): OfficeWidgetData {
  return {
    board: s.board,
    nodes: s.nodes.map(toNode),
    mergeQueue: s.merge_queue.map((m) => ({ thread_no: m.thread_no, employee: m.employee, task_id: m.task_id, status: m.status })),
    ci: pickChecks(s.ci, focus).map((c) => ({ name: c.name, status: c.status, conclusion: c.conclusion })),
  };
}

export interface BoardSummary {
  title: string;
  count: string;
  pct: string;
}

/** The “调度看板” header box: scope title and small-task progress. */
export function boardSummary(threads: ThreadView[], focus: number | null): BoardSummary {
  const live = threads.filter((t) => !["done", "archived"].includes(t.status));
  const scope = focus === null ? live : live.filter((t) => t.no === focus);
  const done = scope.reduce((n, t) => n + t.counts.tasks_done, 0);
  const total = scope.reduce((n, t) => n + t.counts.tasks_total, 0);
  const one = focus === null ? undefined : scope[0];
  return {
    title: one ? `#${one.no} ${one.title}` : `全项目 · ${scope.length} 个讨论串`,
    count: one ? `${done} / ${total}` : `小任务 ${done} / ${total}`,
    pct: total ? `${Math.round((done / total) * 100)}%` : "0%",
  };
}
