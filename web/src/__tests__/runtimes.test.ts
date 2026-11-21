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
    expect(emptyRoleModel("codex")).toBe("留空 = Codex 默认");
    expect(emptyRoleModel("claude")).toBe("留空 = claude-opus-5-5");
    expect(roleModelNames([{ tier: "member", runtime: "codex" }], "codex")).toEqual(["Codex 默认"]);
  });

  it("lists every runtime on the nodes page", () => {
    expect(RUNTIMES.map(([id]) => id)).toEqual(RUNTIME_SPECS.map((r) => r.id));
    expect(runtimeLine("codex", undefined).login).toBe("安装后登录或设置 CODEX_API_KEY");
  });
});
