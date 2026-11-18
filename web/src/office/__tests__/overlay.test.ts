import { describe, expect, it } from "vitest";
import { compileScene } from "../compile";
import { claimableCount, staffBubbleFit, token } from "../overlay";
import type { OfficeEmployee } from "../types";
import { defaultLayout } from "./fixtures";

describe("claimableCount", () => {
  const board = { claimable: 5, by_thread: { "12": { pending: 2, done: 7 }, "13": { running: 1 } } };
  it("counts the focused thread's pending tasks", () => {
    expect(claimableCount(board, 12)).toBe(2);
    expect(claimableCount(board, 13)).toBe(0);
    expect(claimableCount(board, 99)).toBe(0);
  });
  it("uses the project total without focus", () => {
    expect(claimableCount(board, null)).toBe(5);
    expect(claimableCount(undefined, null)).toBe(0);
  });
});

describe("staff desk bubbles and overlay colours", () => {
  const members: OfficeEmployee[] = ["dev-1", "dev-2", "dev-3"].map((id) => ({ id, name: id, tier: "member", role: "dev", sort: 2, thread_no: null }));

  it("caps a desk bubble at about two cells and raises every other column", () => {
    const scene = compileScene(defaultLayout(), { threads: [], employees: members });
    const fits = members.map((e) => staffBubbleFit(scene, scene.staff.get(e.id)!));
    expect(fits).toEqual([{ maxWidth: 162, lift: 0 }, { maxWidth: 162, lift: 20 }, { maxWidth: 162, lift: 0 }]);
    expect(members.map((e) => scene.staff.get(e.id)!.col)).toEqual([0, 1, 2]);
  });

  it("takes overlay colours from the palette, with built-in defaults", () => {
    const l = defaultLayout();
    expect(token(l, "labelDark")).toBe("#23262D");
    l.palette = { ...l.palette, labelDark: "#000000" };
    expect(token(l, "labelDark")).toBe("#000000");
    delete l.palette!.labelMuted;
    expect(token(l, "labelMuted")).toBe("#5C5F6B");
  });
});
