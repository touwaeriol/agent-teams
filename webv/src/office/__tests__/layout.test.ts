import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { mergeLayouts, schemaProblems, validateLayout, LayoutError } from "../load";
import { validateSemantics, wallHits } from "../validate";
import type { OfficeLayout } from "../types";
import { defaultLayout, readJson, repoPath } from "./fixtures";

describe("layout files", () => {
  it("the bundled schema is the same as docs/v2 and /protocol", () => {
    const local = readFileSync(repoPath("web/src/office/office-layout.schema.json"), "utf8");
    expect(local).toBe(readFileSync(repoPath("docs/v2/office-layout.schema.json"), "utf8"));
    expect(local).toBe(readFileSync(repoPath("protocol/schemas/office/office-layout.schema.json"), "utf8"));
  });

  it("/office/default.json and the docs example pass schema and semantic checks", () => {
    for (const rel of ["office/default.json", "docs/v2/office.example.json"]) {
      const { layout, warnings } = validateLayout(readJson(rel));
      expect(layout.id).toBe("default");
      expect(warnings).toEqual([]);
    }
  });

  it("rejects misspelled fields (additionalProperties false)", () => {
    const doc = { ...defaultLayout(), colour: "#fff" };
    expect(schemaProblems(doc).join()).toMatch(/additional/);
    expect(() => validateLayout(doc)).toThrow(LayoutError);
  });

  it("rejects an unknown format with the upgrade message", () => {
    expect(() => validateLayout({ ...defaultLayout(), format: 2 })).toThrow(/布局格式需要更新的前端/);
  });
});

describe("semantic checks", () => {
  it("reports unknown references", () => {
    const l = defaultLayout();
    l.rooms[0].floor = "nope";
    l.states.rules[0].place = "anchor:missing";
    const r = validateSemantics(l);
    expect(r.errors).toContain('rooms[dev].floor: unknown "nope"');
    expect(r.errors.join()).toMatch(/states.rules\[0\].place: unknown "missing"/);
  });

  it("requires a catch-all last rule", () => {
    const l = defaultLayout();
    l.states.rules.pop();
    expect(validateSemantics(l).errors.join()).toMatch(/catch-all/);
  });

  it("detects a graph edge through a wall", () => {
    const l = defaultLayout();
    l.nav.edges.push(["hallTop", "coordIn"]);
    const errors = validateSemantics(l).errors.join("\n");
    expect(errors).toMatch(/(hallTop – coordIn|coordIn – hallTop) crosses the e wall of dev/);
  });

  it("wall crossing inside a door is allowed", () => {
    const room = defaultLayout().rooms.find((r) => r.id === "dev")!;
    expect(wallHits(room, { a: [700, 46], b: [740, 46], what: "x" })).toEqual([]);
    expect(wallHits(room, { a: [700, 100], b: [740, 100], what: "x" })).toEqual(["e"]);
  });

  it("checks pixel row widths", () => {
    const l = defaultLayout();
    l.characters.person.parts.head[0] = "..hh";
    expect(validateSemantics(l).errors.join()).toMatch(/parts.head\[0\] is 4 wide/);
  });

  it("detects a disconnected anchor", () => {
    const l = defaultLayout();
    l.nav.edges = l.nav.edges.filter(([a, b]) => a !== "benchDoor" && b !== "benchDoor");
    expect(validateSemantics(l).errors.join()).toMatch(/anchors.bench \(node benchIn\) is not connected/);
  });
});

describe("extends", () => {
  it("deep-merges objects, merges rooms by id, replaces other arrays", () => {
    const base = defaultLayout() as unknown as Record<string, unknown>;
    const child = {
      extends: "default",
      id: "dark",
      palette: { wood: "#000000" },
      rooms: [{ ...(base.rooms as OfficeLayout["rooms"])[0], label: "工坊" }, { id: "extra", label: "X", kind: "other", rect: [0, 0, 10, 10], floor: "plain" }],
      legend: [{ label: "x", color: "#fff" }],
    };
    const merged = mergeLayouts(base, child) as unknown as OfficeLayout;
    expect(merged.id).toBe("dark");
    expect(merged.palette!.wood).toBe("#000000");
    expect(merged.palette!.ink).toBe("#1F1E1C");
    expect(merged.rooms[0].label).toBe("工坊");
    expect(merged.rooms).toHaveLength(7);
    expect(merged.legend).toEqual([{ label: "x", color: "#fff" }]);
    expect("extends" in merged).toBe(false);
  });
});

describe("built-in fallback layout", () => {
  it("passes the schema and semantic checks", async () => {
    const { BUILTIN_LAYOUT } = await import("../builtin");
    expect(schemaProblems(BUILTIN_LAYOUT)).toEqual([]);
    expect(validateSemantics(BUILTIN_LAYOUT).errors).toEqual([]);
  });
});
