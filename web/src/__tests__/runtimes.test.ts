import { describe, expect, it } from "vitest";
import { promptOnly } from "@/lib/commands";
import { emptyRoleModel, roleModelNames } from "@/lib/models";
import { RUNTIME_SPECS, runtimeLabel, runtimeSpec } from "@/lib/runtimes";
import { RUNTIMES, runtimeLine } from "@/pages/settings/nodes/nodes";

describe("runtime registry", () => {
  it("falls back to the default runtime", () => {
    expect(runtimeSpec("").id).toBe("claude");
    expect(runtimeSpec("gemini").id).toBe("claude");
    expect(runtimeLabel("codex")).toBe("Codex");
  });

  it("drives capabilities instead of id checks", () => {
    expect(promptOnly({ tier: "lead", runtime: "claude" })).toBe(false);
    expect(promptOnly({ tier: "lead", runtime: "codex" })).toBe(true);
    expect(emptyRoleModel("codex")).toBe("留空 = 默认");
    expect(emptyRoleModel("claude")).toBe("留空 = claude-opus-5-5");
    expect(roleModelNames([{ tier: "member", runtime: "codex" }], "codex")).toEqual(["Codex 默认"]);
  });

  it("lists every runtime on the nodes page", () => {
    expect(RUNTIMES.map(([id]) => id)).toEqual(RUNTIME_SPECS.map((r) => r.id));
    expect(runtimeLine("codex", undefined).login).toBe("安装后登录或设置 CODEX_API_KEY");
  });
});

import { scopeRoles } from "@/lib/models";
import { modelOf, modelPatch, roleRuntimes, runtimesPatch } from "@/lib/runtimes";
import { permissionHint } from "@/lib/roles";

describe("roles with several runtimes", () => {
  const role = { runtime: "claude" as const, runtimes: ["claude" as const, "codex" as const], model: "claude-fable-5-2", models: { codex: "gpt-x" } };

  it("keeps each runtime's model when the list changes", () => {
    expect(roleRuntimes({ runtime: "codex" })).toEqual(["codex"]);
    expect(modelOf(role, "codex")).toBe("gpt-x");
    expect(runtimesPatch(role, ["codex", "claude"])).toEqual({ runtime: "codex", runtimes: ["codex", "claude"], model: "gpt-x", models: { claude: "claude-fable-5-2" } });
    expect(runtimesPatch(role, ["claude"])).toEqual({ runtime: "claude", runtimes: ["claude"], model: "claude-fable-5-2", models: {} });
    expect(runtimesPatch(role, [])).toBeNull();
    expect(modelPatch(role, "codex", "gpt-y")).toEqual({ models: { codex: "gpt-y" } });
    expect(modelPatch(role, "claude", "m")).toEqual({ model: "m" });
  });

  it("counts the role for every runtime it has", () => {
    const r = { tier: "member", ...role };
    expect(scopeRoles([r], "codex")).toHaveLength(1);
    expect(roleModelNames([r], "codex")).toEqual(["gpt-x"]);
    expect(promptOnly({ tier: "lead", ...role })).toBe(true);
    expect(permissionHint({ permission_mode: "bypass", ...role })).toContain("Codex：");
  });
});
