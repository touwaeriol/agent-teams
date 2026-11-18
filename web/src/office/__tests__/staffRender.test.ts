import { describe, expect, it } from "vitest";
import { compileScene } from "../compile";
import { Director, type ActorView } from "../director";
import { endedThreads, withLeaving } from "../leaving";
import { listGroups } from "../Office";
import { boardQueue, cardSlots, cardText, workTag } from "../overlay";
import { deskSlots, leaveAlpha, participantsByPod, stripPoint, stripSlots } from "../render";
import type { BoardCards, OfficeEmployee, OfficeQueueCard, OfficeThread, Widget } from "../types";
import { defaultLayout } from "./fixtures";

const threads: OfficeThread[] = [
  { no: 12, title: "员工通信与审批", status: "running" },
  { no: 13, title: "Codex 员工接入", status: "running" },
];
const roster: OfficeEmployee[] = [
  { id: "coord", name: "leader", tier: "coordinator", role: "coordinator", thread_no: null, status: { state: "typing" } },
  { id: "lead-12", name: "lead-12", tier: "lead", role: "lead", thread_no: 12, status: { state: "thinking" } },
  { id: "lead-13", name: "lead-13", tier: "lead", role: "lead", thread_no: 13, status: { state: "thinking" } },
  { id: "dev-2", name: "dev-2", tier: "member", role: "dev", thread_no: 12, status: { state: "typing", task_id: "t6" } },
  { id: "dev-1", name: "dev-1", tier: "member", role: "dev", thread_no: 12, status: { state: "reading", task_id: "t5" } },
  { id: "qa-1", name: "qa-1", tier: "member", role: "qa", thread_no: 13, status: { state: "idle" } },
  { id: "qa-2", name: "qa-2", tier: "member", role: "qa", thread_no: 13, status: { state: "running", task_id: "t2" } },
];

function actors(): { scene: ReturnType<typeof compileScene>; list: ActorView[]; d: Director } {
  const scene = compileScene(defaultLayout(), { threads, employees: roster });
  const d = new Director(scene, { now: () => 0 });
  d.setEmployees(roster);
  return { scene, list: d.actors(), d };
}

describe("table participants", () => {
  it("lists the members working for each table now, in seating order; not the lead, not the idle", () => {
    const { scene, list } = actors();
    const strips = participantsByPod(scene, list).map(([p, people]) => [p.threadNo, people.map((a) => a.id)]);
    expect(strips).toEqual([[12, ["dev-1", "dev-2"]], [13, ["qa-2"]]]);
  });

  it("orders the strip by role, then instance number: dev-2 before dev-10", () => {
    const busy = { state: "typing" as const, task_id: "t1" };
    const crew: OfficeEmployee[] = [
      { id: "qa-1", name: "qa-1", tier: "member", role: "qa", sort: 3, thread_no: 12, status: busy },
      { id: "dev-10", name: "dev-10", tier: "member", role: "dev", sort: 2, thread_no: 12, status: busy },
      { id: "dev-2", name: "dev-2", tier: "member", role: "dev", sort: 2, thread_no: 12, status: busy },
    ];
    const scene = compileScene(defaultLayout(), { threads, employees: crew });
    const d = new Director(scene, { now: () => 0 });
    d.setEmployees(crew);
    expect(participantsByPod(scene, d.actors())[0][1].map((a) => a.id)).toEqual(["dev-2", "dev-10", "qa-1"]);
  });

  it("turns the last slot into +N when more work for the thread than the strip holds", () => {
    expect(stripSlots(["a", "b", "c"], 4)).toEqual({ shown: ["a", "b", "c"], more: 0 });
    expect(stripSlots(["a", "b", "c", "d", "e"], 4)).toEqual({ shown: ["a", "b", "c"], more: 2 });
    const strip = { at: [10, 20] as [number, number], size: 16, step: [18, 0] as [number, number], max: 4 };
    expect(stripPoint(strip, 2)).toEqual([46, 20]);
  });
});

describe("desks and tags", () => {
  it("colours a staff desk's @thread slot with the thread of the work at it", () => {
    const { scene, d } = actors();
    const desk = scene.staff.get("dev-1")!;
    expect(deskSlots(scene, desk, d.desks().get(desk.key))).toEqual({ screen: "$screenReading", thread: "#2F6FDB" });
    expect(deskSlots(scene, scene.staff.get("qa-1")!, undefined)).toEqual({ screen: "$screenOff", thread: "$monitorEdge" });
  });

  it("tags a member with its thread and task while it has one", () => {
    const { d } = actors();
    expect(workTag(d.actor("dev-1")!)).toBe("#12 · t5");
    expect(workTag(d.actor("qa-1")!)).toBeUndefined();
    expect(workTag(d.actor("lead-12")!)).toBeUndefined();
  });

  it("fades an ended thread's table out over fadeMs", () => {
    expect(leaveAlpha(1000, 1000, 4000)).toBe(1);
    expect(leaveAlpha(1000, 3000, 4000)).toBe(0.5);
    expect(leaveAlpha(1000, 9000, 4000)).toBe(0);
    expect(leaveAlpha(1000, 1000, 0)).toBe(0);
  });
});

describe("task board cards", () => {
  const queue: OfficeQueueCard[] = [
    { thread_no: 12, id: "t7", locked: false, reserved_for: "dev-1" },
    { thread_no: 12, id: "t8", locked: true },
    { thread_no: 13, id: "t3" },
    { thread_no: 13, id: "t4" },
    { thread_no: 13, id: "t5" },
  ];
  const widget = { id: "board", type: "taskBoard", at: [170, 2] } as Widget;
  const cards: BoardCards = { at: [102, 5], size: [62, 15], gap: [4, 4], columns: 2, max: 4 };

  it("keeps the focused thread's cards only", () => {
    expect(boardQueue({ claimable: 5, queue }, 13).map((c) => c.id)).toEqual(["t3", "t4", "t5"]);
    expect(boardQueue({ claimable: 5, queue }, null)).toHaveLength(5);
    expect(boardQueue(undefined, null)).toEqual([]);
  });

  it("lays cards out in columns and ends with +N", () => {
    const slots = cardSlots(widget, cards, queue);
    expect(slots.map((s) => [s.at, s.card?.id ?? `+${s.more}`])).toEqual([
      [[272, 7], "t7"],
      [[338, 7], "t8"],
      [[272, 26], "t3"],
      [[338, 26], "+2"],
    ]);
  });

  it("writes the reserved member on the card", () => {
    expect(cardText(cards, queue[0])).toBe("t7→dev-1");
    expect(cardText(cards, queue[1])).toBe("t8");
    expect(cardText({ ...cards, text: { value: "#{threadNo} {taskId} {reservedFor}" } }, queue[0])).toBe("#12 t7 dev-1");
  });
});

describe("leaving threads", () => {
  it("finds the tables that went away and adds them (with their leads) back for the fade", () => {
    const l = defaultLayout();
    expect(endedThreads(l, threads, threads.slice(0, 1)).map((t) => t.no)).toEqual([13]);
    expect(endedThreads(l, threads, [threads[0], { ...threads[1], status: "done" }]).map((t) => t.no)).toEqual([13]);
    expect(endedThreads(l, threads, threads)).toEqual([]);
    const lead = roster[2];
    const back = withLeaving(threads.slice(0, 1), roster.filter((e) => e !== lead), new Map([[13, { thread: threads[1], leads: [lead], since: 7 }]]));
    expect(back.threads.map((t) => t.no)).toEqual([12, 13]);
    expect(back.employees.map((e) => e.id)).toContain("lead-13");
    expect([...back.since]).toEqual([[13, 7]]);
  });
});

describe("phone list", () => {
  it("groups the coordinator, each thread's lead and workers, then the free members", () => {
    const { scene, list } = actors();
    expect(listGroups(scene, list).map((g) => [g.title, g.list.map((a) => a.id).sort()])).toEqual([
      ["项目统筹", ["coord"]],
      ["#12 员工通信与审批", ["dev-1", "dev-2", "lead-12"]],
      ["#13 Codex 员工接入", ["lead-13", "qa-2"]],
      ["空闲成员", ["qa-1"]],
    ]);
  });
});
