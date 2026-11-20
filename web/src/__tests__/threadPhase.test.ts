// threadPhase is the one source of a thread's state: the panel list (group, row mark, row line),
// the open thread (header mark, the bottom line) and the overview / coordinator cards must agree.
import { describe, expect, it } from "vitest";
import type { EmployeeBrief, ThreadView } from "@/api/views";
import { typingLine } from "@/components/app/thread/detail";
import { LIST_GROUPS, MARK_LABEL, threadPhase, type ListGroupId } from "@/lib/phase";
import { markTone, threadKind, threadLook, INK, type ThreadKind } from "@/lib/present";
import { listGroupOf, rowLine, threadMark } from "@/lib/threadList";
import { cardLook } from "@/pages/overview/logic";
import { question, thread } from "./fixtures";

const person = (name: string, state: string, patch: Partial<EmployeeBrief> = {}): EmployeeBrief =>
  ({ id: `e-${name}`, name, tier: name.startsWith("lead") ? "lead" : "member", status: { state, bubble: "", since: "" }, ...patch }) as unknown as EmployeeBrief;
const counts = (c: Partial<ThreadView["counts"]>) => ({ ...thread(1).counts, ...c });
const leadIdle = person("lead-20", "idle");
const replied = { text: "好了", at: "" };

/** The list group a card / chip kind belongs to. */
const GROUP_OF_KIND: Record<ThreadKind, ListGroupId> = {
  wait: "waiting",
  review: "waiting",
  doing: "active",
  queued: "active",
  paused: "idle",
  done: "done",
  failed: "done",
  archived: "archived",
};

/** Every screen's verdict on one thread. */
function everywhere(t: ThreadView) {
  const p = threadPhase(t);
  const group = listGroupOf(t);
  return {
    phase: p.id,
    group,
    groupLabel: LIST_GROUPS.find((g) => g.id === group)!.label,
    rowMark: threadMark(t),
    headerLabel: threadPhase(t).mark === threadMark(t) ? threadPhase(t).label : MARK_LABEL[threadMark(t)],
    card: cardLook(t).label,
    look: threadLook(t).label,
    kindGroup: GROUP_OF_KIND[threadKind(t)],
    typing: typingLine(t),
    line: rowLine(t),
  };
}

const CASES: [string, ThreadView, { phase: string; group: ListGroupId; label: string }][] = [
  ["archived", thread(1, { status: "archived" }), { phase: "archived", group: "archived", label: "已归档" }],
  ["done", thread(1, { status: "done" }), { phase: "done", group: "done", label: "已完成" }],
  ["failed", thread(1, { status: "failed" }), { phase: "failed", group: "done", label: "失败" }],
  ["fired reminder", thread(1, { members: [person("lead-1", "typing")], reminded_at: "2026-11-19T00:00:00Z" }), { phase: "reminded", group: "waiting", label: "等你处理" }],
  ["acceptance (manual)", thread(1, { status: "review", merge_policy: "manual" }), { phase: "acceptance", group: "waiting", label: "待验收" }],
  ["merge failed (auto)", thread(1, { status: "review", merge_policy: "auto", merge_error: "冲突" }), { phase: "acceptance", group: "waiting", label: "待验收" }],
  ["status waiting", thread(1, { status: "waiting", members: [leadIdle] }), { phase: "asking", group: "waiting", label: "等你处理" }],
  ["open approval", thread(1, { members: [person("dev-1", "typing")], counts: counts({ approvals_open: 1, waiting: 1 }) }), { phase: "asking", group: "waiting", label: "等你处理" }],
  ["queued", thread(1, { status: "queued" }), { phase: "queued", group: "active", label: "排队中" }],
  ["paused", thread(1, { status: "paused", members: [leadIdle] }), { phase: "paused", group: "idle", label: "已暂停" }],
  ["review, pr checks", thread(1, { status: "review", merge_policy: "pr", pr_number: 9 }), { phase: "checks", group: "active", label: "进行中" }],
  ["review, auto merge", thread(1, { status: "review", merge_policy: "auto" }), { phase: "checks", group: "active", label: "进行中" }],
  ["lead working", thread(1, { members: [person("lead-1", "typing")] }), { phase: "working", group: "active", label: "进行中" }],
  ["member working", thread(1, { members: [leadIdle, person("dev-1", "running")] }), { phase: "working", group: "active", label: "进行中" }],
  ["tasks waiting for members", thread(1, { members: [leadIdle], counts: counts({ tasks_total: 2, tasks_open: 2 }) }), { phase: "tasks", group: "active", label: "进行中" }],
  [
    "member busy elsewhere, task open here",
    thread(1, { members: [leadIdle, person("dev-1", "typing", { busy_elsewhere: true, thread_no: 3 })], counts: counts({ tasks_total: 1, tasks_open: 1 }) }),
    { phase: "tasks", group: "active", label: "进行中" },
  ],
  ["new thread, lead has not spoken (reading the brief)", thread(1, { members: [leadIdle], lead_last: null }), { phase: "reading", group: "active", label: "进行中" }],
  ["lead queued for a slot", thread(1, { members: [person("lead-1", "queued")], lead_last: replied }), { phase: "lead_queued", group: "active", label: "排队中" }],
  ["lead offline", thread(1, { members: [person("lead-1", "offline")], lead_last: replied }), { phase: "lead_down", group: "waiting", label: "等你处理" }],
  ["lead crashed", thread(1, { members: [person("lead-1", "broken")] }), { phase: "lead_down", group: "waiting", label: "等你处理" }],
  ["lead waiting on others", thread(1, { members: [person("lead-1", "waiting")], lead_last: replied }), { phase: "lead_waiting", group: "active", label: "进行中" }],
  ["lead asked the coordinator (no card for the user)", thread(1, { members: [person("lead-1", "raising_hand")], lead_last: replied }), { phase: "lead_waiting", group: "active", label: "进行中" }],
  ["tasks claimed / in review / merging", thread(1, { members: [leadIdle, person("dev-1", "waiting")], counts: counts({ tasks_total: 3, tasks_open: 3 }), lead_last: replied }), { phase: "tasks", group: "active", label: "进行中" }],
  [
    "lead replied, nothing left (#20)",
    thread(20, { members: [leadIdle, person("dev-1", "idle")], counts: counts({ tasks_total: 3, tasks_done: 2, tasks_open: 0 }), lead_last: { text: "改好了，你看看", at: "" } }),
    { phase: "replied", group: "waiting", label: "等你处理" },
  ],
];

describe("threadPhase", () => {
  it.each(CASES)("%s", (_, t, want) => {
    const v = everywhere(t);
    expect(v.phase).toBe(want.phase);
    expect(v.group).toBe(want.group);
    // List mark, header mark, overview card and chip look all name the state the same way.
    expect(v.headerLabel).toBe(want.label);
    expect(v.card).toBe(want.label);
    expect(v.look).toBe(want.label);
    // The card / chip kind lands in the same list group.
    expect(v.kindGroup).toBe(v.group);
  });

  it("the #20 screenshot: running, lead idle, nothing left — list, bottom line and cards all say 等你处理", () => {
    const t = thread(20, { status: "running", members: [leadIdle], counts: counts({ tasks_total: 1, tasks_done: 1, tasks_open: 0 }), lead_last: { text: "改好了", at: "" } });
    const v = everywhere(t);
    expect(v.groupLabel).toBe("等你处理");
    expect(v.rowMark).toBe("wait");
    expect(v.headerLabel).toBe("等你处理");
    expect(v.card).toBe("等你处理");
    expect(markTone(t)).toBe(INK.wait);
    expect(threadLook(t).spin).toBeFalsy();
    expect(v.typing).toEqual({ text: "组长已回复，等你回复", spin: false });
    expect(v.line).toBe("组长已回复：改好了");
    expect(JSON.stringify(v)).not.toContain("讨论串进行中");
  });

  it("by the lead's own state when nothing else is going: the bottom line, the row line and the group agree", () => {
    const at = (state: string, lead_last: ThreadView["lead_last"] = replied) => thread(1, { members: [person("lead-1", state)], lead_last });
    const expectAll = (t: ThreadView, group: ListGroupId, text: string, spin: boolean) => {
      const v = everywhere(t);
      expect(v.group).toBe(group);
      expect(v.typing).toEqual({ text, spin });
      expect(v.line).toBe(text.replace(/…$/, ""));
      expect(v.kindGroup).toBe(group);
      expect(JSON.stringify(v)).not.toContain("组长已回复");
    };
    expectAll(at("queued"), "active", "等组长上线（节点工位已满，排队中）", true);
    expectAll(at("offline"), "waiting", "组长离线：节点不在线，节点上线后才能继续", false);
    expectAll(at("broken"), "waiting", "组长的会话出错了，需要你看一下", false);
    expectAll(at("waiting"), "active", "组长在等别人回复", true);
    // A new thread: the list says what the detail page says (组长正在阅读任务), never 组长已回复.
    expectAll(at("idle", null), "active", "组长正在阅读任务…", true);
    expectAll(thread(1, { lead_last: null }), "active", "组长正在阅读任务…", true);
    // Open tasks in any unfinished state: 等成员处理任务.
    expectAll(thread(1, { members: [person("lead-1", "idle")], counts: counts({ tasks_open: 1 }), lead_last: replied }), "active", "等成员处理任务", true);
    // Once the lead spoke and is idle: the #20 case stays 等你处理.
    expect(everywhere(at("idle")).typing).toEqual({ text: "组长已回复，等你回复", spin: false });
  });

  it("an open card the caller knows about (not yet in counts) also makes it 等你处理", () => {
    const t = thread(1, { members: [person("dev-1", "typing")] });
    expect(threadPhase(t).group).toBe("active");
    expect(threadPhase(t, new Map([[1, question("q", { thread_no: 1 })]])).group).toBe("waiting");
  });

  it("notes say what it waits on", () => {
    expect(threadPhase(thread(1, { status: "review", merge_policy: "pr" })).note).toBe("等检查通过");
    expect(threadPhase(thread(1, { status: "review", merge_policy: "auto" })).note).toBe("检查通过后自动合并");
    expect(threadPhase(thread(1, { status: "review", merge_policy: "pr", counts: counts({ tasks_open: 1 }) })).note).toBe("等检查通过，还有 1 个小任务没结束");
    expect(threadPhase(thread(1, { status: "review", merge_policy: "pr", pr_state: "merged" })).note).toBe("已合并，等收尾");
    expect(threadPhase(thread(1, { counts: counts({ tasks_open: 1 }) })).note).toBe("等成员处理任务");
    expect(threadPhase(thread(1, { lead_last: { text: "好了", at: "" } })).note).toBe("组长已回复，等你回复");
    expect(threadPhase(thread(1)).note).toBe("组长正在阅读任务");
    expect(threadPhase(thread(1, { status: "queued", queue_position: 2 })).note).toBe("排队中，前面还有 2 个");
    expect(threadPhase(thread(1, { status: "paused", pause_reason: "rate_limit" })).note).toBe("Claude 使用限额用完，已暂停");
  });
});
