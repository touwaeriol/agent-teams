// SSE frames (api.md §1) written straight into the vue-query cache, so every
// page that shows a thread, message, task or question updates in place.
import type { QueryClient } from "@tanstack/vue-query";
import { keys, type MessagePages } from "./queries";
import type {
  ActivityView,
  CheckView,
  CoordinatorView,
  DeliverableView,
  EmployeeBrief,
  EmployeeStatusView,
  MailView,
  MainTaskView,
  MergeEntryView,
  MessageView,
  OfficeSnapshot,
  Page,
  QuestionView,
  TaskView,
  ThreadDetail,
  ThreadView,
} from "./views";

export const PROJECT_EVENTS = [
  "message",
  "thread",
  "main_task",
  "task",
  "question",
  "mail",
  "activity",
  "status",
  "deliverable",
  "merge",
  "ci",
  "memory",
  "shared_file",
  "node",
  "layout",
  "reset",
] as const;
export type ProjectEventType = (typeof PROJECT_EVENTS)[number];

export const USER_EVENTS = ["project", "node", "notice", "reset"] as const;

export interface StatusFrame {
  seq: number;
  at: string;
  employee: { id: string; name?: string; thread_no?: number | null } | string;
  status: EmployeeStatusView;
}

export const STATUS_LOG_LIMIT = 500;

/** Inserts or replaces by id, keeping order (new items go last). */
export function upsert<T extends { id: string }>(list: T[] | undefined, item: T): T[] {
  const items = list ?? [];
  const i = items.findIndex((x) => x.id === item.id);
  if (i < 0) return [...items, item];
  const next = items.slice();
  next[i] = item;
  return next;
}

/** Appends a message to the newest page; one already there is replaced in place (a chat card that was updated). */
export function appendMessage(data: MessagePages | undefined, message: MessageView): MessagePages | undefined {
  if (!data || data.pages.length === 0) return data;
  const old = data.pages.flatMap((p) => p.items).find((m) => m.id === message.id);
  if (old && old.content === message.content && JSON.stringify(old.meta) === JSON.stringify(message.meta)) return data;
  if (old)
    return { ...data, pages: data.pages.map((p) => ({ ...p, items: p.items.map((m) => (m.id === message.id ? message : m)) })) };
  const [first, ...rest] = data.pages;
  return { ...data, pages: [{ ...first, items: [...first.items, message] }, ...rest] };
}

function appendPaged<T extends object>(page: Page<T> | undefined, item: T): Page<T> | undefined {
  if (!page) return page;
  const id = (x: object) => ("id" in x ? (x as { id: unknown }).id : undefined);
  if (id(item) !== undefined && page.items.some((x) => id(x) === id(item))) return page;
  return { ...page, items: [...page.items, item] };
}

function threadOf(qc: QueryClient, pid: string, no: number | null | undefined): ThreadView | undefined {
  if (no === null || no === undefined) return undefined;
  return qc.getQueryData<ThreadView[]>(keys.threads(pid))?.find((t) => t.no === no);
}

/** The conversation a thread number belongs to: the thread (cid = tid) or the coordinator. */
function conversationOf(qc: QueryClient, pid: string, no: number | null | undefined): string | undefined {
  if (no === null || no === undefined) return qc.getQueryData<CoordinatorView>(keys.coordinator(pid))?.conversation_id;
  return threadOf(qc, pid, no)?.id;
}

/** Rewrites one thread in the project list and in its own query (live events, optimistic updates). */
export function patchThread(qc: QueryClient, pid: string, tid: string, patch: (t: ThreadView) => ThreadView) {
  qc.setQueryData<ThreadView[]>(keys.threads(pid), (list) => list?.map((t) => (t.id === tid ? patch(t) : t)));
  qc.setQueryData<ThreadDetail>(keys.thread(tid), (t) => (t ? { ...t, ...patch(t) } : t));
}

function onMessage(qc: QueryClient, _pid: string, d: { conversation_id: string; message: MessageView }) {
  qc.setQueryData<MessagePages>(keys.messages(d.conversation_id), (data) => appendMessage(data, d.message));
}

/** A deleted thread leaves every list; its own queries are dropped. */
export function onThreadDeleted(qc: QueryClient, pid: string, d: { id: string; no: number }) {
  qc.setQueryData<ThreadView[]>(keys.threads(pid), (list) => list?.filter((t) => t.id !== d.id));
  qc.setQueryData<OfficeSnapshot>(keys.office(pid), (s) => (s ? { ...s, threads: s.threads.filter((t) => t.id !== d.id) } : s));
  qc.removeQueries({ queryKey: keys.thread(d.id) });
  qc.invalidateQueries({ queryKey: keys.waiting(pid) });
  qc.invalidateQueries({ queryKey: keys.overview(pid) });
  qc.invalidateQueries({ queryKey: keys.allDeliverables(pid) });
}

function onThread(qc: QueryClient, pid: string, d: { thread?: ThreadView; deleted?: { id: string; no: number } }) {
  if (d.deleted) return onThreadDeleted(qc, pid, d.deleted);
  if (!d.thread) return;
  const thread = d.thread;
  const sortByNo = (list: ThreadView[]) => list.slice().sort((a, b) => b.no - a.no);
  qc.setQueryData<ThreadView[]>(keys.threads(pid), (list) => (list ? sortByNo(upsert(list, thread)) : list));
  qc.setQueryData<ThreadDetail>(keys.thread(thread.id), (t) => (t ? { ...t, ...thread } : t));
  qc.setQueryData<OfficeSnapshot>(keys.office(pid), (s) => (s ? { ...s, threads: upsert(s.threads, thread) } : s));
  qc.invalidateQueries({ queryKey: keys.waiting(pid) });
  qc.invalidateQueries({ queryKey: keys.overview(pid) });
}

function onMainTask(qc: QueryClient, pid: string, d: { thread_no: number; main_tasks: MainTaskView[] }) {
  const t = threadOf(qc, pid, d.thread_no);
  if (t) patchThread(qc, pid, t.id, (x) => ({ ...x, main_tasks: d.main_tasks }));
}

/** At most one office snapshot refetch per project in this window (trailing): a burst of task events is one fetch. */
export const OFFICE_REFRESH_MS = 750;
const officeRefresh = new WeakMap<QueryClient, Map<string, ReturnType<typeof setTimeout>>>();

/** Refetches the project's office snapshot at the end of the current window (one is already due: nothing). */
export function refreshOffice(qc: QueryClient, pid: string): void {
  let due = officeRefresh.get(qc);
  if (!due) officeRefresh.set(qc, (due = new Map()));
  if (due.has(pid)) return;
  const pending = due;
  pending.set(
    pid,
    setTimeout(() => {
      pending.delete(pid);
      qc.invalidateQueries({ queryKey: keys.office(pid) });
    }, OFFICE_REFRESH_MS),
  );
}

/**
 * A task changed: its thread's task list, the thread's counts in the project list and its own
 * query (the frame carries them; tasks_open decides 进行中 vs 等你处理), and the office (the board
 * shows the project queue). Older servers send no counts: those stay until the next thread frame.
 */
function onTask(qc: QueryClient, pid: string, d: { thread_no: number; task: TaskView; counts?: ThreadView["counts"] }) {
  const t = threadOf(qc, pid, d.thread_no);
  if (t) qc.setQueryData<TaskView[]>(keys.tasks(t.id), (list) => (list ? upsert(list, d.task) : list));
  if (t && d.counts) {
    const counts = d.counts;
    patchThread(qc, pid, t.id, (x) => ({ ...x, counts: { ...x.counts, ...counts } }));
  }
  refreshOffice(qc, pid);
}

function onQuestion(qc: QueryClient, pid: string, d: { thread_no?: number | null; question: QuestionView }) {
  const cid = conversationOf(qc, pid, d.thread_no ?? d.question.thread_no);
  if (cid) qc.setQueryData<QuestionView[]>(keys.questions(cid), (list) => (list ? upsert(list, d.question) : list));
  const t = threadOf(qc, pid, d.thread_no ?? d.question.thread_no);
  if (t) qc.setQueryData<ThreadDetail>(keys.thread(t.id), (x) => (x?.questions ? { ...x, questions: upsert(x.questions, d.question) } : x));
  qc.invalidateQueries({ queryKey: keys.waiting(pid) });
}

function onMail(qc: QueryClient, pid: string, d: { thread_no: number; mail: MailView }) {
  const t = threadOf(qc, pid, d.thread_no);
  if (t) qc.setQueryData<Page<MailView>>(keys.mail(t.id), (p) => appendPaged(p, d.mail));
}

function onActivity(qc: QueryClient, pid: string, d: ActivityView) {
  const t = threadOf(qc, pid, d.thread_no);
  if (t) qc.setQueryData<Page<ActivityView>>(keys.activity(t.id), (p) => appendPaged(p, d));
}

function employeeId(frame: StatusFrame): string {
  return typeof frame.employee === "string" ? frame.employee : frame.employee.id;
}

/**
 * A member of thread no after a status frame: its status and, when the frame names its thread,
 * where it works (busy_elsewhere: it holds a task in another thread).
 */
export function memberAfter(m: EmployeeBrief, d: StatusFrame, no: number): EmployeeBrief {
  if (m.id !== employeeId(d)) return m;
  const at = typeof d.employee === "string" ? undefined : d.employee.thread_no;
  if (at === undefined) return { ...m, status: d.status };
  return { ...m, status: d.status, thread_no: at, busy_elsewhere: !!d.status.task_id && at !== null && at !== no };
}

function onStatus(qc: QueryClient, pid: string, d: StatusFrame) {
  const id = employeeId(d);
  for (const t of qc.getQueryData<ThreadView[]>(keys.threads(pid)) ?? []) {
    if (!t.members.some((m) => m.id === id)) continue;
    const after = (m: EmployeeBrief) => memberAfter(m, d, t.no);
    patchThread(qc, pid, t.id, (x) => ({ ...x, members: x.members.map(after) }));
    qc.setQueryData<ThreadView["members"]>(keys.members(t.id), (list) => list?.map(after));
  }
  qc.setQueryData<StatusFrame[]>(statusLogKey(pid), (log) => [...(log ?? []), d].slice(-STATUS_LOG_LIMIT));
}

export const statusLogKey = (pid: string) => ["p", pid, "status-log"] as const;

function onMerge(qc: QueryClient, pid: string, d: { thread_no: number; entry: MergeEntryView }) {
  const t = threadOf(qc, pid, d.thread_no);
  if (t) qc.setQueryData<MergeEntryView[]>(keys.mergeQueue(t.id), (list) => (list ? upsert(list, d.entry) : list));
  qc.setQueryData<OfficeSnapshot>(keys.office(pid), (s) => (s ? { ...s, merge_queue: upsert(s.merge_queue, d.entry) } : s));
}

function onCi(qc: QueryClient, pid: string, d: { thread_no: number; checks: CheckView[] }) {
  const t = threadOf(qc, pid, d.thread_no);
  if (t) qc.setQueryData(keys.checks(t.id), d.checks);
  qc.setQueryData<OfficeSnapshot>(keys.office(pid), (s) => (s ? { ...s, ci: { ...s.ci, [d.thread_no]: d.checks } } : s));
}

/** A memory file was written or deleted: refetch the list, the open files and the overview box. */
function onMemory(qc: QueryClient, pid: string, _d: { op: "write" | "delete"; name: string }) {
  qc.invalidateQueries({ queryKey: keys.memory(pid) });
  qc.invalidateQueries({ queryKey: keys.overview(pid) });
}

/** A shared file was written (uploaded or overwritten) or deleted: refetch the list. */
function onSharedFile(qc: QueryClient, pid: string, _d: { op: "write" | "delete"; path: string; by: string }) {
  qc.invalidateQueries({ queryKey: keys.sharedFiles(pid) });
}

function onDeliverable(qc: QueryClient, pid: string, _d: { deliverable: DeliverableView }) {
  qc.invalidateQueries({ queryKey: keys.allDeliverables(pid) });
  qc.invalidateQueries({ queryKey: keys.overview(pid) });
}

function onNode(qc: QueryClient, pid: string) {
  qc.invalidateQueries({ queryKey: keys.nodes });
  refreshOffice(qc, pid);
}

export const layoutTickKey = (pid: string) => ["p", pid, "layout-tick"] as const;

function onLayout(qc: QueryClient, pid: string) {
  qc.setQueryData<number>(layoutTickKey(pid), (n) => (n ?? 0) + 1);
}

function onReset(qc: QueryClient, pid: string) {
  qc.invalidateQueries({ queryKey: keys.projectScope(pid) });
  qc.invalidateQueries({ queryKey: ["t"] });
  qc.invalidateQueries({ queryKey: ["c"] });
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Handler = (qc: QueryClient, pid: string, data: any) => void;

const HANDLERS: Partial<Record<ProjectEventType, Handler>> = {
  message: onMessage,
  thread: onThread,
  main_task: onMainTask,
  task: onTask,
  question: onQuestion,
  mail: onMail,
  activity: onActivity,
  status: onStatus,
  deliverable: onDeliverable,
  merge: onMerge,
  ci: onCi,
  memory: onMemory,
  shared_file: onSharedFile,
  node: onNode,
  layout: onLayout,
  reset: onReset,
};

/** Applies one project SSE frame to the cache. Unknown types are ignored. */
export function applyProjectEvent(qc: QueryClient, pid: string, type: string, data: unknown): void {
  HANDLERS[type as ProjectEventType]?.(qc, pid, data);
}

/** Applies one user SSE frame (`/api/events`). Returns a notice text to show, if any. */
export function applyUserEvent(qc: QueryClient, type: string, data: unknown): string | undefined {
  if (type === "project" || type === "reset") qc.invalidateQueries({ queryKey: keys.projects });
  if (type === "node" || type === "reset") qc.invalidateQueries({ queryKey: keys.nodes });
  if (type === "notice") return (data as { text?: string })?.text;
  return undefined;
}
