import { describe, expect, it } from "vitest";
import { compileScene } from "../compile";
import { anchorPoint, Director } from "../director";
import type { OfficeEmployee } from "../types";
import { crew, defaultLayout, twoThreads } from "./fixtures";

function setup(employees: OfficeEmployee[] = crew, opts: { titles?: Record<string, string> } = {}) {
  const layout = defaultLayout();
  const scene = compileScene(layout, { threads: twoThreads, employees });
  let now = 0;
  const d = new Director(scene, { now: () => now, taskTitle: (id) => opts.titles?.[id] });
  d.setEmployees(employees);
  const run = (ms: number, step = 50) => {
    for (let t = 0; t < ms; t += step) {
      now += step;
      d.tick(step);
    }
  };
  return { layout, scene, d, run };
}

describe("Director", () => {
  it("places everyone instantly from their initial status", () => {
    const { d } = setup();
    expect(d.actor("lead-12")!.pos).toEqual([76, 96]);
    // idle members go to the lounge (first free slot)
    expect(d.actor("dev-1")!.pos).toEqual([200, 560]);
    expect(d.actor("coord")!.pos).toEqual([1040, 52]);
    expect(d.busy).toBe(false);
  });

  it("walks from the lounge to the desk when work starts, then types", () => {
    const { d, run } = setup();
    d.apply("dev-1", { state: "typing", bubble: "改 PlanList.tsx" });
    const a = d.actor("dev-1")!;
    expect(a.walking).toBe(true);
    expect(a.anim).toBe("walk");
    expect(a.bubble).toEqual({ text: "改 PlanList.tsx", kind: "plain" });
    run(20000);
    const b = d.actor("dev-1")!;
    expect(b.walking).toBe(false);
    expect(b.pos).toEqual([236, 96]);
    expect(b.anim).toBe("typing");
    expect(d.desks().get("pod12.d2")!.screen).toBe("$screenTyping");
  });

  it("a shared member without a thread stays in the lounge instead of vanishing from a seat", () => {
    const free: OfficeEmployee = { id: "dev-9", name: "dev-9", tier: "member", role: "dev", thread_no: null, status: { state: "offline" } };
    const { d } = setup([...crew, free]);
    const a = d.actor("dev-9")!;
    expect(a.hidden).toBe(false);
    expect(a.pos).toEqual(anchorPoint(defaultLayout().anchors.lounge, 5, "dev-9")); // after the five idle members
    d.apply("dev-9", { state: "typing" }, true);
    expect(d.actor("dev-9")!.hidden).toBe(false);
  });

  it("frees anchor slots on leave and reuses them first-come first-served", () => {
    const { d } = setup();
    expect(d.actor("dev-2")!.pos).toEqual([250, 560]);
    d.apply("dev-1", { state: "typing" }, true);
    d.apply("lead-12", { state: "idle" }, true); // leads stay seated
    d.apply("dev-3", { state: "typing" }, true);
    d.apply("qa-1", { state: "typing" }, true);
    d.apply("dev-1", { state: "idle" }, true);
    expect(d.actor("dev-1")!.pos).toEqual([200, 560]);
  });

  it("anchor overflow: standBehind, stack and jitter", () => {
    const bench = defaultLayout().anchors.bench;
    expect(anchorPoint(bench, 5, "x")).toEqual([510, 512]);
    expect(anchorPoint(bench, 11, "x")).toEqual([550, 536]);
    const stack = { points: [[0, 0]] as [number, number][], overflow: "stack" as const };
    expect(anchorPoint(stack, 2, "x")).toEqual([12, 12]);
    const lounge = defaultLayout().anchors.lounge;
    const j = anchorPoint(lounge, 8, "x");
    expect(Math.abs(j[0] - 360)).toBeLessThanOrEqual(12);
  });

  it("raising hand keeps the place and adds the overlay", () => {
    const { d } = setup();
    d.apply("dev-2", { state: "typing" }, true);
    d.apply("dev-2", { state: "raising_hand", bubble: "请求权限：pnpm add", bubble_kind: "ask" });
    const a = d.actor("dev-2")!;
    expect(a.walking).toBe(false);
    expect(a.pos).toEqual([76, 266]);
    expect(a.overlay).toBe("raiseHand");
    expect(a.anim).toBe("typing");
    expect(a.bubble).toEqual({ text: "请求权限：pnpm add", kind: "ask" });
  });

  it("plays the claim sequence: desk → board → wait → desk, then the latest status", () => {
    const { d, run } = setup(crew, { titles: { t8: "计划与子任务展示" } });
    d.apply("dev-3", { state: "reading" }, true);
    const desk = d.actor("dev-3")!.pos;
    d.apply("dev-3", { state: "claiming", cause: "task.claimed:t8", task_id: "t8" });
    expect(d.actor("dev-3")!.bubble!.text).toBe("去任务板");
    d.apply("dev-3", { state: "typing", bubble: "改 tasks.py" }); // queued until the sequence ends
    run(5600);
    const atBoard = d.actor("dev-3")!;
    expect(atBoard.pos).toEqual([352, 46]);
    expect(atBoard.bubble!.text).toBe("领取 t8 计划与子任务展示");
    expect(atBoard.anim).toBe("stand");
    run(1500);
    expect(d.actor("dev-3")!.bubble!.text).toBe("回工位");
    run(10000);
    const back = d.actor("dev-3")!;
    expect(back.pos).toEqual(desk);
    expect(back.anim).toBe("typing");
    expect(back.bubble!.text).toBe("改 tasks.py");
    expect(d.busy).toBe(false);
  });

  it("teleports beyond maxConcurrentWalks and when walking is disabled", () => {
    const { d, layout } = setup();
    layout.states.maxConcurrentWalks = 1;
    d.apply("dev-1", { state: "typing" });
    d.apply("dev-2", { state: "typing" });
    expect(d.actor("dev-1")!.walking).toBe(true);
    expect(d.actor("dev-2")!.walking).toBe(false);
    expect(d.actor("dev-2")!.pos).toEqual([76, 266]);
    d.noWalk = true;
    d.apply("dev-3", { state: "typing" });
    expect(d.actor("dev-3")!.walking).toBe(false);
  });

  it("hot reload keeps positions and walks; moved places teleport", () => {
    const { d, layout, run } = setup();
    d.apply("dev-1", { state: "typing" });
    run(1000);
    const mid = d.actor("dev-1")!.pos;
    d.setScene(compileScene(structuredClone(layout), { threads: twoThreads, employees: crew }));
    expect(d.actor("dev-1")!.pos).toEqual(mid);
    expect(d.actor("dev-1")!.walking).toBe(true);
    const moved = structuredClone(layout);
    moved.anchors.lounge.points[1] = [120, 700];
    d.setScene(compileScene(moved, { threads: twoThreads, employees: crew }));
    expect(d.actor("dev-2")!.pos).toEqual([120, 700]);
  });

  it("off-page people are hidden at their seat and badged at shared anchors", () => {
    const layout = defaultLayout();
    const threads = Array.from({ length: 5 }, (_, i) => ({ no: 12 + i, title: `t${i}`, status: "running" as const }));
    const people: OfficeEmployee[] = [
      { id: "a", name: "dev-1", tier: "member", role: "dev", thread_no: 16, status: { state: "typing" } },
      { id: "b", name: "dev-2", tier: "member", role: "dev", thread_no: 16, status: { state: "idle" } },
    ];
    const d = new Director(compileScene(layout, { threads, employees: people, page: 0 }));
    d.setEmployees(people);
    expect(d.actor("a")!.hidden).toBe(true);
    expect(d.actor("b")!).toMatchObject({ hidden: false, badge: 16 });
    d.setScene(compileScene(layout, { threads, employees: people, page: 1 }));
    expect(d.actor("a")!.hidden).toBe(false);
    expect(d.actor("b")!.badge).toBeUndefined();
  });

  it("reset applies a snapshot instantly", () => {
    const { d } = setup();
    d.reset(new Map([["dev-1", { state: "waiting" as const }]]));
    expect(d.actor("dev-1")!.pos).toEqual([510, 488]);
    expect(d.actor("dev-1")!.bubble!.text).toBe("等任务放出");
  });
});
