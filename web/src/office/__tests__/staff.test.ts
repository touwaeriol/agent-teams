import { describe, expect, it } from "vitest";
import { compileScene, podPeople } from "../compile";
import { capacity, planStaff } from "../staff";
import type { OfficeEmployee, OfficeLayout, OfficeThread } from "../types";
import { defaultLayout, legacyLayout } from "./fixtures";

const threads: OfficeThread[] = [
  { no: 12, title: "员工通信与审批", status: "running", counts: { main_done: 7, main_total: 13 } },
  { no: 13, title: "Codex 员工接入", status: "running" },
];

const m = (name: string, role: string, sort: number, thread_no: number | null = null): OfficeEmployee => ({ id: name, name, tier: "member", role, sort, thread_no });

/** Coordinator, two leads, dev × 3, qa × 2, review × 1 (all members working for some thread). */
const staff: OfficeEmployee[] = [
  { id: "coord", name: "leader", tier: "coordinator", role: "coordinator", thread_no: null },
  { id: "lead-12", name: "lead-12", tier: "lead", role: "lead", thread_no: 12 },
  { id: "lead-13", name: "lead-13", tier: "lead", role: "lead", thread_no: 13 },
  m("dev-2", "dev", 2, 12),
  m("dev-1", "dev", 2, 13),
  m("dev-3", "dev", 2),
  m("qa-1", "qa", 3, 12),
  m("qa-2", "qa", 3),
  m("review-1", "review", 4, 13),
];

const many = (n: number, role = "dev"): OfficeEmployee[] => Array.from({ length: n }, (_, i) => m(`${role}-${i + 1}`, role, 2));

describe("compileScene · staff area (shared members)", () => {
  it("draws one work table per thread holding only the lead's seat", () => {
    const scene = compileScene(defaultLayout(), { threads, employees: staff });
    expect(scene.pods.map((p) => [p.threadNo, p.template, p.rect])).toEqual([
      [12, "table", [24, 56, 164, 132]],
      [13, "table", [198, 56, 164, 132]],
    ]);
    expect(scene.seats.get("lead-12")).toMatchObject({ point: [48, 108], deskKey: "pod12.lead", accessNode: "pod12.door" });
    expect(scene.seats.get("coord")).toBeUndefined();
    expect(podPeople(defaultLayout(), staff, 12).map((e) => e.id)).toEqual(["lead-12"]);
  });

  it("gives every member a fixed desk, rows by role in template order then by instance, whatever its thread", () => {
    const scene = compileScene(defaultLayout(), { threads, employees: staff });
    const seat = (id: string) => scene.seats.get(id)!;
    expect(seat("dev-1")).toMatchObject({ point: [62, 212], deskKey: "staff.dev-1", accessNode: "staff.row0" });
    expect(seat("dev-2").point).toEqual([146, 212]);
    expect(seat("dev-3").point).toEqual([230, 212]);
    expect(seat("qa-1")).toMatchObject({ point: [62, 300], accessNode: "staff.row1" });
    expect(seat("qa-2").point).toEqual([146, 300]);
    expect(seat("review-1")).toMatchObject({ point: [62, 388], accessNode: "staff.row2" });
    expect(scene.staff.get("dev-1")).toMatchObject({ at: [46, 236], timer: [92, 232], tag: [84, 286], owner: "dev-1" });
    expect(scene.desks.get("staff.dev-1")).toBe(scene.staff.get("dev-1"));
    expect(scene.staff.size).toBe(6);
  });

  it("hangs the desk rows off the layout's entry node along the spine", () => {
    const scene = compileScene(defaultLayout(), { threads, employees: staff });
    expect(scene.graph.nodes.get("staff.row0")).toEqual([28, 212]);
    expect(scene.graph.nodes.get("staff.row2")).toEqual([28, 388]);
    expect([...scene.graph.adj.get("staff.row0")!]).toEqual(expect.arrayContaining(["devWest", "staff.row1"]));
    expect(scene.graph.adj.get("pod12.door")!.has("devMid")).toBe(true);
  });

  it("does not move anyone when a member changes thread (desks are fixed)", () => {
    const a = compileScene(defaultLayout(), { threads, employees: staff });
    const moved = staff.map((e) => (e.id === "dev-3" ? { ...e, thread_no: 12 } : e.id === "dev-1" ? { ...e, thread_no: null } : e));
    const b = compileScene(defaultLayout(), { threads, employees: moved });
    for (const id of ["dev-1", "dev-2", "dev-3", "qa-1"]) expect(b.seats.get(id)).toEqual(a.seats.get(id));
  });

  it("packs the roles together when one row per role does not fit", () => {
    const roles = ["dev", "qa", "review", "decision", "design"].map((r, i) => m(`${r}-1`, r, i));
    const scene = compileScene(defaultLayout(), { threads, employees: roles });
    expect(roles.map((e) => scene.seats.get(e.id)!.point)).toEqual([[62, 212], [146, 212], [230, 212], [314, 212], [398, 212]]);
  });

  it("scales the desks down when even packed rows do not fit", () => {
    const l = defaultLayout();
    const plan = planStaff(l.staffArea!, many(40));
    expect(plan.scale).toBe(0.8);
    expect(plan.rows.map((r) => r.length)).toEqual([10, 10, 10, 10]);
    const scene = compileScene(l, { threads, employees: many(40) });
    const [x, y, w, h] = l.staffArea!.rect;
    for (const d of scene.staff.values()) {
      expect(d.at[0] + d.sprite.w * d.scale).toBeLessThanOrEqual(x + w + 0.01);
      expect(d.at[1] + d.sprite.h * d.scale).toBeLessThanOrEqual(y + h + 0.01);
    }
  });

  it("stacks people beyond the last desk 6 px apart with overflow = stack", () => {
    const l = defaultLayout();
    l.staffArea!.overflow = "stack";
    expect(capacity(l.staffArea!, 1)).toEqual({ perRow: 8, maxRows: 4 });
    const scene = compileScene(l, { threads, employees: many(34) });
    expect(scene.staff.size).toBe(32);
    const last = scene.seats.get("dev-32")!.point;
    expect(scene.seats.get("dev-33")).toMatchObject({ point: [last[0] + 6, last[1] + 6], accessNode: "staff.row3" });
    expect(scene.seats.get("dev-34")!.point).toEqual([last[0] + 12, last[1] + 12]);
    expect(scene.seats.get("dev-34")!.deskKey).toBeUndefined();
  });

  it("members.visitPods seats members at their thread's table, the others at their desks", () => {
    const l = defaultLayout();
    l.members = { visitPods: true };
    const scene = compileScene(l, { threads, employees: staff });
    expect(scene.seats.get("dev-2")).toMatchObject({ deskKey: "pod12.table", threadNo: 12 });
    expect(scene.seats.get("qa-1")!.deskKey).toBeUndefined(); // overflow seat of #12
    expect(scene.seats.get("qa-1")!.threadNo).toBe(12);
    expect(scene.seats.get("dev-3")!.deskKey).toBe("staff.dev-3");
    expect(scene.staff.size).toBe(6); // the desks stay
  });

  it("places the participant strip and the visit spot of each table; marks leaving tables", () => {
    const scene = compileScene(defaultLayout(), { threads, employees: staff, leaving: new Map([[13, 1234]]) });
    const [p12, p13] = scene.pods;
    expect(p12.participants).toEqual({ at: [112, 142], size: 16, step: [18, 0], max: 4 });
    expect(p12.visit).toEqual([136, 96]);
    expect(p12.visitNode).toBe("pod12.door");
    expect(p12.leaving).toBeUndefined();
    expect(p13.leaving).toBe(1234);
  });

  it("an old layout without staffArea keeps seating members in the pods", () => {
    const l: OfficeLayout = legacyLayout();
    expect(l.staffArea).toBeUndefined();
    const scene = compileScene(l, { threads, employees: staff });
    expect(scene.staff.size).toBe(0);
    expect(scene.seats.get("dev-2")).toMatchObject({ deskKey: "pod12.d2", threadNo: 12 });
    expect(scene.seats.get("dev-3")).toBeUndefined(); // no thread: the director sends it to the idle anchor
  });
});
