// Simulated office for the dev demo (shared members, shared-members.md §9): one coordinator, a
// lead per thread, project members at their own desks working for the threads, and a looping
// script of status events (claiming a task, raising a hand, watching CI, rate limits…) plus a
// thread opening (the coordinator sets up a table) and one ending (its lead leaves).
import type { EmployeeStatus, OfficeEmployee, OfficeStatusEvent, OfficeThread, OfficeWidgetData } from "../types";

const BASE_THREADS: OfficeThread[] = [
  { no: 12, title: "员工通信与审批", status: "running", counts: { main_done: 7, main_total: 13 } },
  { no: 13, title: "Codex 员工接入", status: "running", counts: { main_done: 3, main_total: 6 } },
  { no: 14, title: "移动端适配", status: "waiting", counts: { main_done: 1, main_total: 4 } },
];
const OPENED: OfficeThread = { no: 15, title: "落地页改版", status: "running", counts: { main_done: 0, main_total: 3 } };

export const DEMO_THREADS: OfficeThread[] = BASE_THREADS;

/** Threads at `ms` into the loop: #15 opens at 4 s, #14 ends at 24 s; the loop starts over with #12–#14. */
export function demoThreads(ms: number): OfficeThread[] {
  const list = ms >= 4000 ? [...BASE_THREADS, OPENED] : BASE_THREADS;
  return ms >= 24000 ? list.filter((t) => t.no !== 14) : list;
}

const MIN = 60_000;

function person(id: string, tier: string, role: string, thread_no: number | null, sort: number, status: EmployeeStatus, look?: OfficeEmployee["look"]): OfficeEmployee {
  return { id, name: id, tier, role, thread_no, sort, status, look };
}

export function demoRoster(now: number): OfficeEmployee[] {
  const since = (m: number) => now - m * MIN;
  return [
    person("leader", "coordinator", "coordinator", null, 0, { state: "typing", bubble: "汇总进度给你", since: since(3) }),
    person("lead-12", "lead", "lead", 12, 0, { state: "reading", task_kind: "review", bubble: "审查 dev-1 的提交", since: since(40.4) }),
    person("lead-13", "lead", "lead", 13, 0, { state: "raising_hand", bubble: "问你：key 还是登录？", bubble_kind: "ask", since: since(2) }),
    person("lead-14", "lead", "lead", 14, 0, { state: "thinking", bubble: "等 qa-1 交 t2", since: since(6) }),
    person("lead-15", "lead", "lead", 15, 0, { state: "typing", bubble: "拆任务", since: since(0.5) }),
    person("dev-1", "member", "dev", 12, 1, { state: "thinking", bubble: "等审查", task_id: "t5", since: since(12) }),
    person("dev-2", "member", "dev", 12, 1, { state: "raising_hand", bubble: "请求权限：pnpm add", bubble_kind: "ask", task_id: "t6", since: since(41) }, { color: "#2F8F9D", outfit: "tee", accessory: "none", hair: "#1F1E1C" }),
    person("dev-3", "member", "dev", 12, 1, { state: "reading", bubble: "读 tasks.py", task_id: "t7", since: since(40.5) }),
    person("dev-4", "member", "dev", 13, 1, { state: "typing", bubble: "改 codex_worker.py", task_id: "t3", since: since(18) }),
    person("qa-1", "member", "qa", 14, 2, { state: "running", substate: "test", bubble: "跑 test_mobile.py", task_id: "t2", since: since(40.3) }),
    person("qa-2", "member", "qa", 12, 2, { state: "waiting", since: since(5) }),
    person("architect", "member", "decision", 12, 3, { state: "thinking", task_kind: "decision", bubble: "决策：卡片布局 A / B", task_id: "t9", since: since(8) }),
    person("review-1", "member", "review", 13, 4, { state: "idle", bubble: "空闲 ☕", since: now }),
  ];
}

interface Beat {
  at: number;
  who: string;
  status: EmployeeStatus;
  text: string;
}

/** One loop of the script; `at` is ms from the loop start. */
const SCRIPT: Beat[] = [
  { at: 1500, who: "dev-3", status: { state: "claiming", cause: "task.claimed:t8", task_id: "t8" }, text: "去任务板领取 t8「计划与子任务展示」" },
  { at: 2500, who: "dev-3", status: { state: "typing", bubble: "改 PlanList.tsx", task_id: "t8" }, text: "开始改 PlanList.tsx" },
  { at: 6000, who: "qa-2", status: { state: "reading", bubble: "qa-2 的依赖完成了", task_id: "t10" }, text: "依赖完成，回工位写测试" },
  { at: 9000, who: "dev-2", status: { state: "typing", bubble: "装 @tanstack/react-virtual", task_id: "t6" }, text: "权限已批准：pnpm add -D @tanstack/react-virtual" },
  { at: 11000, who: "dev-1", status: { state: "running", substate: "ci", bubble: "看 CI：verify", task_id: "t5" }, text: "去机房看 CI" },
  { at: 14000, who: "dev-4", status: { state: "thinking", substate: "rate_limited", task_id: "t3" }, text: "额度用完，去休息区等恢复" },
  { at: 18000, who: "qa-2", status: { state: "waiting", bubble: "等 t9 放出" }, text: "等任务放出" },
  { at: 20000, who: "dev-1", status: { state: "running", substate: "git_push", bubble: "推送 thread/12/dev-1", task_id: "t5" }, text: "CI 通过，推送分支" },
  { at: 22000, who: "review-1", status: { state: "reading", task_kind: "review", bubble: "审查 t5 审批工具", task_id: "t11" }, text: "领取审查 t5" },
  { at: 23000, who: "qa-1", status: { state: "idle", bubble: "#14 的活做完了" }, text: "#14 的任务都完成了" },
  { at: 24000, who: "dev-2", status: { state: "raising_hand", bubble: "请求权限：pnpm add", bubble_kind: "ask", task_id: "t6" }, text: "请求权限：pnpm add -D @tanstack/react-virtual" },
  { at: 26000, who: "dev-4", status: { state: "typing", bubble: "改 codex_worker.py", task_id: "t3" }, text: "额度恢复，继续改 codex_worker.py" },
  { at: 28000, who: "dev-1", status: { state: "thinking", bubble: "等审查", task_id: "t5" }, text: "提交 t5，进入待审查" },
  { at: 30000, who: "review-1", status: { state: "idle", bubble: "空闲 ☕" }, text: "审查完成" },
  { at: 32000, who: "dev-3", status: { state: "reading", bubble: "读 tasks.py", task_id: "t7" }, text: "读 tasks.py" },
  { at: 33000, who: "qa-1", status: { state: "running", substate: "test", bubble: "跑 test_mobile.py", task_id: "t2" }, text: "回到 #14 跑测试" },
];

export const LOOP_MS = 34000;

export interface DemoEvent extends OfficeStatusEvent {
  text: string;
  thread: number | null;
}

const threadOf = new Map(demoRoster(0).map((e) => [e.id, e.thread_no ?? null]));

/** Events of loop `n` starting at `t0` (epoch ms), seq continuing from `seq0`. */
export function loopEvents(t0: number, seq0: number): DemoEvent[] {
  return SCRIPT.map((b, i) => ({
    seq: seq0 + i + 1,
    at: t0 + b.at,
    employee: b.who,
    status: b.status,
    text: b.text,
    thread: threadOf.get(b.who) ?? null,
  }));
}

/** A recorded history for replay: `loops` loops ending at `end`. */
export function recordedHistory(end: number, loops: number): DemoEvent[] {
  const start = end - loops * LOOP_MS;
  return Array.from({ length: loops }, (_, i) => loopEvents(start + i * LOOP_MS, i * SCRIPT.length)).flat();
}

export const DEMO_TITLES: Record<string, string> = { t8: "计划与子任务展示", t5: "审批工具", t9: "卡片布局 A / B" };

export const DEMO_DATA: OfficeWidgetData = {
  board: {
    claimable: 6,
    by_thread: { "12": { pending: 3 }, "13": { pending: 2 }, "14": { pending: 1 } },
    queue: [
      { thread_no: 12, id: "t9", role: "decision", locked: false },
      { thread_no: 12, id: "t12", role: "dev", locked: false, reserved_for: "dev-1" },
      { thread_no: 12, id: "t13", role: "qa", locked: true },
      { thread_no: 13, id: "t4", role: "dev", locked: false },
      { thread_no: 13, id: "t5", role: "qa", locked: true },
      { thread_no: 14, id: "t3", role: "dev", locked: true },
    ],
  },
  nodes: [
    { id: "n1", name: "rc-erio", online: true, capacity: 4, active: 3 },
    { id: "n2", name: "cvp", online: true, capacity: 4, active: 3, queued: 1 },
  ],
  mergeQueue: [
    { thread_no: 12, employee: "dev-1", status: "merging", task_id: "t5" },
    { thread_no: 12, employee: "dev-2", status: "queued", task_id: "t6" },
  ],
  ci: [
    { name: "verify", status: "completed", conclusion: "success" },
    { name: "integration", status: "in_progress", conclusion: null },
  ],
};
