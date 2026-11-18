import { describe, expect, it } from "vitest";
import { compileScene, planPods, sortMembers, visibleThreads } from "../compile";
import type { OfficeThread } from "../types";
import { crew, legacyLayout, twoThreads } from "./fixtures";

const threads = (n: number): OfficeThread[] => Array.from({ length: n }, (_, i) => ({ no: i + 1, title: `t${i + 1}`, status: "running" }));

describe("compileScene · legacy layout without staffArea (members sit in the pods)", () => {
  it("lays out two threads with the full template at the design coordinates", () => {
    const scene = compileScene(legacyLayout(), { threads: twoThreads, employees: crew });
    expect(scene.pods.map((p) => [p.threadNo, p.template, p.rect])).toEqual([
      [12, "full", [24, 48, 336, 386]],
      [13, "full", [376, 48, 336, 386]],
    ]);
    expect(scene.pods[0].plate!.text).toBe("#12 员工通信与审批 · 7/13");
    expect(scene.pods[0].color).toBe("#2F6FDB");
    expect(scene.pods[1].color).toBe("#C2570C");
    expect(scene.desks.get("pod12.d1")!.at).toEqual([40, 120]);
  });

  it("seats the lead at d1 and members by role then instance, overflow after", () => {
    const scene = compileScene(legacyLayout(), { threads: twoThreads, employees: crew });
    const seat = (id: string) => scene.seats.get(id);
    expect(seat("lead-12")).toMatchObject({ point: [76, 96], deskKey: "pod12.d1", accessNode: "pod12.aisleTop" });
    expect(seat("dev-1")).toMatchObject({ point: [236, 96], deskKey: "pod12.d2" });
    expect(seat("dev-2")).toMatchObject({ point: [76, 266], deskKey: "pod12.d3", accessNode: "pod12.aisleMid" });
    expect(seat("dev-3")!.deskKey).toBe("pod12.d4");
    expect(seat("qa-2")!.point).toEqual([324, 168]);
    expect(seat("qa-2")!.deskKey).toBeUndefined();
    expect(seat("lead-13")).toMatchObject({ point: [428, 96] });
    expect(seat("coord")).toBeUndefined();
  });

  it("stacks people beyond the overflow seats 6 px apart", () => {
    const many = Array.from({ length: 9 }, (_, i) => ({ id: `d${i + 1}`, name: `dev-${i + 1}`, tier: "member", role: "dev", thread_no: 12 }));
    const scene = compileScene(legacyLayout(), { threads: twoThreads.slice(0, 1), employees: many });
    // 3 member desks + 3 overflow seats, then stacked next to the last overflow seat [140, 350]
    expect(scene.seats.get("d7")!.point).toEqual([24 + 140 + 6, 48 + 350 + 6]);
    expect(scene.seats.get("d8")!.point).toEqual([24 + 140 + 12, 48 + 350 + 12]);
  });

  it("merges pod graphs with prefixes and exits", () => {
    const scene = compileScene(legacyLayout(), { threads: twoThreads, employees: crew });
    expect(scene.graph.nodes.get("pod12.door")).toEqual([184, 42]);
    expect([...scene.graph.adj.get("pod12.door")!]).toEqual(expect.arrayContaining(["pod12.aisleTop", "hallTop"]));
    expect(scene.graph.adj.get("devSouth")!.has("pod13.doorBottom")).toBe(true);
  });

  it("uses the compact template for 3–4 threads in a 2 × 2 grid", () => {
    const scene = compileScene(legacyLayout(), { threads: threads(4), employees: [] });
    expect(scene.pods.map((p) => p.rect.slice(0, 2))).toEqual([[24, 48], [376, 48], [24, 235], [376, 235]]);
    expect(scene.pods[0].template).toBe("compact");
  });

  it("paginates beyond the last template", () => {
    const l = legacyLayout();
    expect(planPods(l, 9)).toMatchObject({ template: "compact", perPage: 4 });
    const page2 = compileScene(l, { threads: threads(9), employees: [], page: 2 });
    expect(page2.page).toEqual({ index: 2, count: 3, perPage: 4 });
    expect([...page2.onPage]).toEqual([9]);
  });

  it("scales the template down instead when overflow = scale", () => {
    const l = legacyLayout();
    l.threadPods.layout.overflow = "scale";
    const plan = planPods(l, 6);
    expect(plan.scale).toBeLessThan(1);
    expect(plan.scale).toBeGreaterThanOrEqual(0.5);
    const scene = compileScene(l, { threads: threads(6), employees: [] });
    expect(scene.pods).toHaveLength(6);
    const [ax, ay, aw, ah] = l.threadPods.area.rect;
    for (const p of scene.pods) {
      expect(p.rect[0] + p.rect[2]).toBeLessThanOrEqual(ax + aw + 0.01);
      expect(p.rect[1] + p.rect[3]).toBeLessThanOrEqual(ay + ah + 0.01);
    }
  });

  it("filters by layout.show, orders by no and pins the focused thread", () => {
    const l = legacyLayout();
    const list: OfficeThread[] = [
      { no: 5, title: "a", status: "running" },
      { no: 2, title: "b", status: "done" },
      { no: 3, title: "c", status: "waiting" },
      { no: 9, title: "d", status: "review" },
    ];
    expect(visibleThreads(l, list, null).map((t) => t.no)).toEqual([3, 5, 9]);
    expect(visibleThreads(l, list, 9).map((t) => t.no)).toEqual([9, 3, 5]);
  });

  it("sorts members by role order then instance", () => {
    const list = [
      { id: "a", name: "qa-1", tier: "member", role: "qa", sort: 2 },
      { id: "b", name: "dev-2", tier: "member", role: "dev", sort: 1 },
      { id: "c", name: "dev-1", tier: "member", role: "dev", sort: 1 },
    ];
    expect(sortMembers(list, ["roleSort", "instance"]).map((e) => e.name)).toEqual(["dev-1", "dev-2", "qa-1"]);
  });
});
