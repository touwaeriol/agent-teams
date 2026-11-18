import { describe, expect, it } from "vitest";
import { compileScene, type CompileInput } from "../compile";
import { Director, sceneEvents } from "../director";
import type { OfficeEmployee, OfficeThread } from "../types";
import { defaultLayout, legacyLayout } from "./fixtures";

const twelve: OfficeThread = { no: 12, title: "员工通信与审批", status: "running" };
const thirteen: OfficeThread = { no: 13, title: "Codex 员工接入", status: "running" };

const roster: OfficeEmployee[] = [
  { id: "coord", name: "leader", tier: "coordinator", role: "coordinator", thread_no: null, status: { state: "typing" } },
  { id: "lead-12", name: "lead-12", tier: "lead", role: "lead", thread_no: 12, status: { state: "thinking" } },
  { id: "dev-1", name: "dev-1", tier: "member", role: "dev", sort: 2, thread_no: 12, status: { state: "typing", task_id: "t5" } },
  { id: "dev-2", name: "dev-2", tier: "member", role: "dev", sort: 2, thread_no: 12, status: { state: "idle" } },
  { id: "qa-1", name: "qa-1", tier: "member", role: "qa", sort: 3, thread_no: 16, status: { state: "reading", task_id: "t2" } },
];

function setup(threads: OfficeThread[] = [twelve], employees = roster) {
  const layout = defaultLayout();
  const compile = (input: Partial<CompileInput> = {}) => compileScene(layout, { threads, employees, ...input });
  let now = 0;
  const d = new Director(compile(), { now: () => now });
  d.setEmployees(employees);
  const run = (ms: number, step = 50) => {
    for (let t = 0; t < ms; t += step) {
      now += step;
      d.tick(step);
    }
  };
  return { layout, compile, d, run };
}

describe("Director · staff desks", () => {
  it("seats working members at their own desk, the lead at its table, idle members in the lounge", () => {
    const { d } = setup();
    expect(d.actor("dev-1")).toMatchObject({ pos: [62, 212], thread: 12, atDesk: true });
    expect(d.actor("lead-12")).toMatchObject({ pos: [48, 108], thread: 12 });
    expect(d.actor("dev-2")).toMatchObject({ pos: [200, 692], thread: null, atDesk: false });
    expect(d.actor("coord")!.pos).toEqual([1040, 52]);
  });

  it("a member working for a thread without a table on this page stays visible at its desk", () => {
    const { d } = setup();
    expect(d.actor("qa-1")).toMatchObject({ hidden: false, badge: undefined, thread: 16, pos: [62, 300] });
  });

  it("the desk reports the thread colour slot and the screen while the member sits there", () => {
    const { d } = setup();
    expect(d.desks().get("staff.dev-1")).toMatchObject({ screen: "$screenTyping", thread: 12 });
    expect(d.desks().has("staff.dev-2")).toBe(false);
  });

  it("a raised hand moves with the scene: from a pod seat of an old layout to the member's desk", () => {
    const old = compileScene(legacyLayout(), { threads: [twelve], employees: roster });
    const d = new Director(old, { now: () => 0 });
    d.setEmployees(roster);
    d.apply("dev-1", { state: "raising_hand", task_id: "t5", bubble_kind: "ask" }, true);
    expect(d.actor("dev-1")!.pos).toEqual(old.seats.get("dev-1")!.point);
    d.setScene(compileScene(defaultLayout(), { threads: [twelve], employees: roster }));
    expect(d.actor("dev-1")).toMatchObject({ pos: [62, 212], overlay: "raiseHand", walking: false });
  });

  it("walks a member who finishes to the lounge and back to the same desk on the next task", () => {
    const { d, run } = setup();
    d.apply("dev-1", { state: "idle" });
    run(30000);
    expect(d.actor("dev-1")).toMatchObject({ walking: false, atDesk: false, thread: null });
    d.apply("dev-1", { state: "typing", task_id: "t9" });
    run(30000);
    expect(d.actor("dev-1")).toMatchObject({ pos: [62, 212], atDesk: true });
  });
});

describe("Director · thread tables opening and closing", () => {
  it("the coordinator walks to a new table, says what it set up, then returns to its desk", () => {
    const { d, compile, run } = setup([twelve]);
    d.setScene(compile({ threads: [twelve, thirteen] }));
    const c = d.actor("coord")!;
    expect(c.walking).toBe(true);
    expect(c.bubble!.text).toBe("给 #13 放一张工作台");
    run(60000, 100);
    // back at its desk once the sequence is over
    expect(d.actor("coord")!.pos).toEqual([1040, 52]);
    expect(d.busy).toBe(false);
  });

  it("stops at the table's visit spot with the thread title", () => {
    const { d, compile, run } = setup([twelve]);
    const scene = compile({ threads: [twelve, thirteen] });
    d.setScene(scene);
    let seen = false;
    for (let i = 0; i < 600 && !seen; i++) {
      run(50);
      const c = d.actor("coord")!;
      seen = !c.walking && c.bubble?.text === "#13 Codex 员工接入 开工";
      if (seen) expect(c.pos).toEqual(scene.pods[1].visit);
    }
    expect(seen).toBe(true);
  });

  it("the lead of an ended thread stands up, walks out and is gone; it comes back if the thread does", () => {
    const { d, compile, run } = setup([twelve]);
    d.setScene(compile({ leaving: new Map([[12, 0]]) }));
    expect(d.actor("lead-12")!.bubble!.text).toBe("#12 收工");
    run(800);
    expect(d.actor("lead-12")!.walking).toBe(true);
    run(30000);
    expect(d.actor("lead-12")!.hidden).toBe(true);
    d.setScene(compile());
    expect(d.actor("lead-12")!.hidden).toBe(false);
  });

  it("page flips, focus and hot reloads are not openings", () => {
    const { compile } = setup([twelve, thirteen]);
    expect(sceneEvents(compile(), compile({ focus: 13 }))).toEqual([]);
    const opened = sceneEvents(compile({ threads: [twelve] }), compile());
    expect(opened.map(([e, p]) => [e, p.threadNo])).toEqual([["threadOpened", 13]]);
    const ended = sceneEvents(compile(), compile({ leaving: new Map([[13, 5]]) }));
    expect(ended.map(([e, p]) => [e, p.threadNo])).toEqual([["threadEnded", 13]]);
  });
});
