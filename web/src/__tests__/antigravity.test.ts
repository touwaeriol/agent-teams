import { describe, expect, it } from "vitest";
import { promptOnly } from "@/lib/commands";
import { COMMON_MODELS, emptyRoleModel, modelName, modelSuggestions, validModel } from "@/lib/models";
import { runtimeNote } from "@/lib/plugins";
import { RUNTIME_LABEL } from "@/lib/present";
import { newRole, readOnlyCaveat, roleProblems } from "@/lib/roles";
import { runtimesFor } from "@/lib/runtimes";
import { runtimeLine } from "@/pages/settings/nodes/nodes";
import type { RoleView } from "@/api/views";

const CAVEAT = "Antigravity 的只读靠禁用写类工具和拒绝写权限实现，不是沙箱级保证";

describe("antigravity runtime", () => {
  it("is offered to members only", () => {
    expect(runtimesFor("member").map((r) => r.id)).toContain("antigravity");
    expect(runtimesFor("lead").map((r) => r.id)).toEqual(["claude"]);
    expect(runtimesFor("coordinator").map((r) => r.id)).toEqual(["claude"]);
  });

  it("refuses a lead or coordinator on it", () => {
    const base = newRole([], "#000");
    const coord = { ...base, tier: "coordinator" as const, role: "leader", title: "" };
    const lead = { ...base, tier: "lead" as const, role: "lead", title: "", runtime: "antigravity" as const, runtimes: ["antigravity" as const] };
    expect(roleProblems([coord, lead])).toContain("组长 不能用 Antigravity");
    const dev = { ...base, runtime: "antigravity" as const, runtimes: ["antigravity" as const] };
    expect(roleProblems([coord, { ...lead, runtime: "claude" as const, runtimes: ["claude" as const] }, dev])).toEqual([]);
  });

  it("warns that a read-only Antigravity member has no sandbox", () => {
    const r: RoleView = { ...newRole([], "#000"), gate: "review", runtime: "claude", runtimes: ["claude", "antigravity"] };
    expect(readOnlyCaveat(r)).toBe(CAVEAT);
    expect(readOnlyCaveat({ ...r, read_only: false })).toBe("");
    expect(readOnlyCaveat({ ...r, runtimes: ["claude"] })).toBe("");
    expect(readOnlyCaveat({ ...r, gate: "" })).toBe("");
    expect(readOnlyCaveat({ ...r, gate: "", read_only: true, runtime: "antigravity", runtimes: ["antigravity"] })).toBe(CAVEAT);
  });

  it("suggests agy's models and leaves empty to agy", () => {
    expect(COMMON_MODELS.antigravity.length).toBeGreaterThan(0);
    expect(COMMON_MODELS.antigravity.every((m) => validModel(m.id) && m.id.startsWith("gemini-"))).toBe(true);
    expect(modelSuggestions([], "antigravity")[0]).toBe("gemini-4-neon-high");
    expect(modelSuggestions([{ tier: "member", runtime: "antigravity", model: "my-model" }], "antigravity")[0]).toBe("my-model");
    expect(modelSuggestions([], "claude")).not.toContain("gemini-4-neon-high");
    expect(modelName("gemini-4-neon-high")).toBe("Gemini 4 Neon (high)");
    expect(emptyRoleModel("antigravity")).toBe("留空 = Antigravity 默认");
  });

  it("shows its node state", () => {
    expect(runtimeLine("antigravity", { cli: "", auth: "none", installed: false })).toEqual({ ver: "未安装", login: "安装 agy 后运行 agy acp --login 登录", ok: false });
    expect(runtimeLine("antigravity", { cli: "1.5.14", auth: "oauth", installed: true }).ok).toBe(true);
    expect(runtimeLine("antigravity", { cli: "1.5.14", auth: "oauth" })).toEqual({ ver: "1.5.14", login: "已登录", ok: true });
    expect(runtimeLine("antigravity", { cli: "1.5.14", auth: "none" })).toEqual({ ver: "1.5.14", login: "未登录", ok: false });
    expect(runtimeLine("antigravity", { cli: "1.5.14", auth: "api_key" }).login).toBe("已配置 API Key");
    expect(runtimeLine("antigravity", { cli: "1.5.14", auth: "adc" }).login).toBe("已配置 ADC 凭证");
  });

  it("is named everywhere a runtime is shown", () => {
    expect(RUNTIME_LABEL.antigravity).toBe("Antigravity");
    expect(promptOnly({ tier: "member", runtime: "antigravity" })).toBe(true);
    expect(runtimeNote({ runtime: "antigravity" })).toContain("Antigravity 员工只用插件里的技能");
    expect(runtimeNote({ runtime: "claude", runtimes: ["claude", "codex", "antigravity"] })).toContain("Codex / Antigravity 员工");
  });
});
