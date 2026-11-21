import { describe, expect, it } from "vitest";
import { RuntimeRegistry, type RuntimeAdapter } from "../src/sessions/registry.js";
import type { Driver } from "../src/sessions/types.js";

function fake(id: "claude" | "codex", nativeSkills: boolean): RuntimeAdapter & { probed: number } {
  return {
    id, nativeSkills, passEnv: id === "codex" ? ["OPENAI_API_KEY"] : [], probed: 0,
    info: () => (id === "claude" ? { sdk: "1", auth: "subscription" } : { cli: "2", auth: "none", sandbox_ok: true }),
    async probe() { this.probed++; },
    fixedEnv: () => ({ RUNTIME: id }),
    createDriver: () => ({ id } as unknown as Driver),
  };
}

describe("RuntimeRegistry", () => {
  it("looks runtimes up by id and reports them in hello", async () => {
    const claude = fake("claude", true);
    const codex = fake("codex", false);
    const reg = new RuntimeRegistry([claude, codex]);
    expect(reg.get("codex")).toBe(codex);
    expect(reg.has("claude")).toBe(true);
    expect(reg.has("gemini")).toBe(false);
    await reg.probeAll();
    expect([claude.probed, codex.probed]).toEqual([1, 1]);
    expect(reg.hello()).toEqual({ claude: { sdk: "1", auth: "subscription" }, codex: { cli: "2", auth: "none", sandbox_ok: true } });
  });

  it("refuses a runtime the node does not run", () => {
    const reg = new RuntimeRegistry([fake("claude", true)]);
    expect(() => reg.get("codex")).toThrow(/not supported/);
  });
});
