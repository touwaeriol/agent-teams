import { describe, expect, it } from "vitest";
import type { RoleView } from "@/api/views";
import { countText, fromPreset, isReadOnly, newRole, roleProblems } from "@/lib/roles";

// The reviewer and helper presets (roles.Presets) come with three members each.
describe("reviewer and helper presets", () => {
  const base = (): RoleView[] => [
    { ...newRole([], "#111"), id: "r0", tier: "coordinator", role: "coordinator", sort: 0 },
    { ...newRole([], "#222"), id: "r1", tier: "lead", role: "lead", sort: 1 },
  ];
  // As GET /api/roles/presets returns them (mock/fixtures.mjs rolePresets).
  const presets: Partial<RoleView>[] = [
    { role: "reviewer", title: "审查员", count: 3, gate: "review", read_only: true },
    { role: "helper", title: "助手", count: 3, gate: "", read_only: false },
  ];
  const preset = (key: string): RoleView => ({ ...newRole([], "#333"), ...presets.find((r) => r.role === key)! });

  it("keeps count 3 when added and shows 项目编制 3 个", () => {
    const reviewer = fromPreset(base(), preset("reviewer"));
    expect(reviewer.count).toBe(3);
    expect(reviewer.gate).toBe("review");
    expect(isReadOnly(reviewer)).toBe(true);
    expect(countText(reviewer)).toBe("项目编制 3 个（可选）");
    const helper = fromPreset(base(), preset("helper"));
    expect(helper.count).toBe(3);
    expect(isReadOnly(helper)).toBe(false);
    expect(countText(helper)).toBe("项目编制 3 个");
    expect(roleProblems([...base(), reviewer, { ...helper, sort: 3 }])).toEqual([]);
  });

  it("a second reviewer preset gets its own key", () => {
    const list = [...base(), fromPreset(base(), preset("reviewer"))];
    expect(fromPreset(list, preset("reviewer")).role).toBe("reviewer-2");
  });
});
