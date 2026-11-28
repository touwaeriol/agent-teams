import { describe, expect, it } from "vitest";
import { reactive } from "vue";
import type { RoleView } from "@/api/views";
import { addEdge, draftOf, freeName, layout, looseSteps, newStepId, presets, removeStep, roleChoices, roleLabel, unlabeledBranches } from "./flow";

const role = (tier: string, key: string, title: string) => ({ tier, role: key, title }) as unknown as RoleView;
const team = [role("coordinator", "coordinator", "统筹"), role("lead", "lead", "组长"), role("member", "dev", "开发"), role("member", "qa", "测试")];

describe("workflow page logic", () => {
  it("offers member roles then the lead, and names unknown roles", () => {
    const choices = roleChoices(team);
    expect(choices.map((c) => c.value)).toEqual(["dev", "qa", "lead"]);
    expect(roleLabel("qa", choices)).toBe("测试");
    expect(roleLabel("", choices)).toBe("我");
    expect(roleLabel("ghost", choices)).toContain("已不在团队");
  });

  it("lays parallel branches out in one column and the join after them", () => {
    const steps = ["a", "b", "c", "d"].map((id) => ({ id, name: id, role: "dev" }));
    const edges = [
      { from: "a", to: "b", kind: "next" as const },
      { from: "a", to: "c", kind: "next" as const },
      { from: "b", to: "d", kind: "next" as const },
      { from: "c", to: "d", kind: "next" as const },
      { from: "d", to: "a", kind: "reject" as const },
    ];
    const pos = Object.fromEntries(layout(steps, edges).map((s) => [s.id, [s.x, s.y]]));
    expect(pos.a).toEqual([0, 0]);
    expect(pos.b?.[0]).toBe(pos.c?.[0]);
    expect(pos.b?.[1]).not.toBe(pos.c?.[1]);
    expect(pos.d?.[0]).toBeGreaterThan(pos.b?.[0] ?? 0);
  });

  it("keeps edges unique and off their own step, and removing a step drops its edges", () => {
    let edges = addEdge([], { from: "a", to: "b", kind: "next" });
    edges = addEdge(edges, { from: "a", to: "b", kind: "next" });
    edges = addEdge(edges, { from: "a", to: "a", kind: "next" });
    edges = addEdge(edges, { from: "b", to: "a", kind: "reject" });
    expect(edges).toHaveLength(2);
    const d = removeStep({ name: "x", steps: [{ id: "a", name: "A", role: "" }, { id: "b", name: "B", role: "" }], edges }, "a");
    expect(d.steps?.map((s) => s.id)).toEqual(["b"]);
    expect(d.edges).toEqual([]);
  });

  it("builds presets on the user's roles and finds free ids and names", () => {
    const [dev, parallel] = presets(team);
    expect(dev?.draft.steps?.map((s) => s.role)).toEqual(["dev", "qa", "dev", "lead"]);
    expect(parallel?.draft.edges?.filter((e) => e.from === "ok")).toHaveLength(2);
    expect(newStepId([{ id: "s1", name: "", role: "" }, { id: "s3", name: "", role: "" }])).toBe("s2");
    const flows = [{ name: "新流程" }, { name: "新流程 2" }] as never[];
    expect(freeName("新流程", flows)).toBe("新流程 3");
    expect(looseSteps({ name: "x", steps: [{ id: "a", name: "A", role: "" }, { id: "b", name: "B", role: "" }], edges: [] })).toEqual(["A", "B"]);
  });

  it("lays branches out like next edges and counts branches without a condition", () => {
    const steps = ["q", "a", "b"].map((id) => ({ id, name: id, role: "dev" }));
    const edges = [
      { from: "q", to: "a", kind: "branch" as const, label: "是" },
      { from: "q", to: "b", kind: "branch" as const },
    ];
    const pos = Object.fromEntries(layout(steps, edges).map((s) => [s.id, s.x]));
    expect(pos.a).toBe(pos.b);
    expect(pos.a).toBeGreaterThan(pos.q ?? 0);
    expect(unlabeledBranches({ name: "x", steps, edges })).toBe(1);
  });

  it("copies a saved flow out of Vue's reactive cache (the page opens it on load)", () => {
    const saved = reactive({ id: "wf1", name: "f", description: "", is_default: true, created_at: "", updated_at: "", steps: [{ id: "a", name: "A", role: "dev" }], edges: [] });
    const d = draftOf(saved);
    d.steps![0]!.name = "B";
    expect(saved.steps[0]!.name).toBe("A");
  });
});
