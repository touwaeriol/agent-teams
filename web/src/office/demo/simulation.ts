// Simulated office for the dev demo: a roster like the design canvas and a
// looping script of status events (claiming a task, raising a hand, watching
// CI, rate limits…).
import type { EmployeeStatus, OfficeEmployee, OfficeStatusEvent, OfficeThread, OfficeWidgetData } from "../types";

export const DEMO_THREADS: OfficeThread[] = [
  { no: 12, title: "员工通信与审批", status: "running", counts: { main_done: 7, main_total: 13 } },
  { no: 13, title: "Codex 员工接入", status: "running", counts: { main_done: 3, main_total: 6 } },
];

const MIN = 60_000;

function person(id: string, tier: string, role: string, thread_no: number | null, sort: number, status: EmployeeStatus, look?: OfficeEmployee["look"]): OfficeEmployee {
  return { id, name: id, tier, role, thread_no, sort, status, look };
}

export function demoRoster(now: number): OfficeEmployee[] {
  const since = (m: number) => now - m * MIN;
  return [
    person("leader", "coordinator", "coordinator", null, 0, { state: "typing", bubble: "汇总进度给你", since: since(3) }),
    person("lead-12", "lead", "lead", 12, 0, { state: "reading", task_kind: "review", bubble: "审查 dev-1 的提交", since: since(40.4) }),
    person("dev-1", "member", "dev", 12, 1, { state: "thinking", bubble: "等审查", since: since(12) }),
    person("dev-2", "member", "dev", 12, 1, { state: "raising_hand", bubble: "请求权限：pnpm add", bubble_kind: "ask", since: since(41) }, { color: "#2F8F9D", outfit: "tee", accessory: "none", hair: "#1F1E1C" }),
    person("dev-3", "member", "dev", 12, 1, { state: "reading", bubble: "读 tasks.py", since: since(40.5) }),
    person("qa-2", "member", "qa", 12, 2, { state: "waiting", since: since(5) }),
    person("architect", "member", "decision", 12, 3, { state: "thinking", task_kind: "decision", bubble: "决策：卡片布局 A / B", since: since(8) }),
    person("lead-13", "lead", "lead", 13, 0, { state: "raising_hand", bubble: "问你：key 还是登录？", bubble_kind: "ask", since: since(2) }),
    person("qa-1", "member", "qa", 13, 2, { state: "running", substate: "test", bubble: "跑 test_codex.py", since: since(40.3) }),
    person("dev-4", "member", "dev", 13, 1, { state: "typing", bubble: "改 codex_worker.py", since: since(18) }),
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
  { at: 6000, who: "qa-2", status: { state: "reading", bubble: "qa-2 的依赖完成了" }, text: "依赖完成，回工位写测试" },
  { at: 9000, who: "dev-2", status: { state: "typing", bubble: "装 @tanstack/react-virtual" }, text: "权限已批准：pnpm add -D @tanstack/react-virtual" },
  { at: 11000, who: "dev-1", status: { state: "running", substate: "ci", bubble: "看 CI：verify" }, text: "去机房看 CI" },
  { at: 14000, who: "dev-4", status: { state: "thinking", substate: "rate_limited" }, text: "额度用完，去休息区等恢复" },
  { at: 18000, who: "qa-2", status: { state: "waiting", bubble: "等 t9 放出" }, text: "等任务放出" },
  { at: 20000, who: "dev-1", status: { state: "running", substate: "git_push", bubble: "推送 thread/12/dev-1" }, text: "CI 通过，推送分支" },
  { at: 22000, who: "review-1", status: { state: "reading", task_kind: "review", bubble: "审查 t5 审批工具" }, text: "领取审查 t5" },
  { at: 24000, who: "dev-2", status: { state: "raising_hand", bubble: "请求权限：pnpm add", bubble_kind: "ask" }, text: "请求权限：pnpm add -D @tanstack/react-virtual" },
  { at: 26000, who: "dev-4", status: { state: "typing", bubble: "改 codex_worker.py" }, text: "额度恢复，继续改 codex_worker.py" },
  { at: 28000, who: "dev-1", status: { state: "thinking", bubble: "等审查" }, text: "提交 t5，进入待审查" },
  { at: 30000, who: "review-1", status: { state: "idle", bubble: "空闲 ☕" }, text: "审查完成" },
  { at: 32000, who: "dev-3", status: { state: "reading", bubble: "读 tasks.py" }, text: "读 tasks.py" },
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
  board: { claimable: 3, by_thread: { "12": { pending: 1 }, "13": { pending: 2 } } },
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
