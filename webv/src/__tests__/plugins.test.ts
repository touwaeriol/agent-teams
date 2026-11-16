import { describe, expect, it } from "vitest";
import type { PluginView } from "@/api/views";
import { contentsText, formatRef, missingRefs, parseRef, pinPlugin, refFor, resolvedVersion, runtimeNote, splitLibrary, togglePlugin } from "@/lib/plugins";
import { fromPreset, newRole, roleProblems } from "@/lib/roles";

const contents = { skills: ["greet"], commands: ["shout"], agents: ["echoer"], hooks: true, mcp_servers: ["calc"] };
function plugin(name: string, patch: Partial<PluginView> = {}): PluginView {
  return {
    id: `pl_${name}`, name, builtin: false, enabled: true, latest: 2,
    versions: [
      { version: 2, manifest_version: "0.2.0", description: "", size: 10, sha256: "h", contents, created_at: "2026-11-15T00:00:00Z" },
      { version: 1, manifest_version: "0.1.0", description: "", size: 10, sha256: "h", contents, created_at: "2026-11-14T00:00:00Z" },
    ],
    ...patch,
  };
}

describe("plugin refs", () => {
  it("reads and writes name and name@version", () => {
    expect(parseRef("tinyplug")).toEqual({ name: "tinyplug", version: 0 });
    expect(parseRef("tinyplug@3")).toEqual({ name: "tinyplug", version: 3 });
    expect(parseRef("tinyplug@x").version).toBe(0);
    expect(formatRef({ name: "tinyplug", version: 3 })).toBe("tinyplug@3");
    expect(formatRef({ name: "tinyplug", version: 0 })).toBe("tinyplug");
  });
  it("ticks at the latest version, pins in place and unticks pinned entries", () => {
    let refs = togglePlugin(["a@1"], "tinyplug");
    expect(refs).toEqual(["a@1", "tinyplug"]);
    refs = pinPlugin(refs, "tinyplug", 2);
    expect(refs).toEqual(["a@1", "tinyplug@2"]);
    expect(refFor(refs, "tinyplug")).toEqual({ name: "tinyplug", version: 2 });
    expect(pinPlugin(refs, "tinyplug", 0)).toEqual(["a@1", "tinyplug"]);
    expect(togglePlugin(refs, "tinyplug")).toEqual(["a@1"]);
    expect(togglePlugin(null, "x")).toEqual(["x"]);
  });
  it("flags pinned versions that were pruned and plugins that are gone", () => {
    const p = plugin("tinyplug");
    expect(resolvedVersion(p, { name: "tinyplug", version: 0 })).toBe(2);
    expect(resolvedVersion(p, { name: "tinyplug", version: 1 })).toBe(1);
    expect(resolvedVersion(p, { name: "tinyplug", version: 7 })).toBeUndefined();
    expect(missingRefs(["tinyplug@1", "ghost"], [p])).toEqual([{ name: "ghost", version: 0 }]);
  });
});

describe("plugin library", () => {
  it("summarises what a version provides", () => {
    expect(contentsText(contents)).toBe("技能 1 · 命令 1 · 子代理 1 · hooks · MCP 1");
    expect(contentsText({ skills: [], commands: null, agents: [], hooks: false, mcp_servers: ["a", "b"] })).toBe("MCP 2");
    expect(contentsText(undefined)).toBe("");
  });
  it("lists my plugins before the built-ins", () => {
    const { mine, builtin } = splitLibrary([plugin("zeta", { builtin: true }), plugin("beta"), plugin("alpha")]);
    expect(mine.map((p) => p.name)).toEqual(["alpha", "beta"]);
    expect(builtin.map((p) => p.name)).toEqual(["zeta"]);
  });
  it("tells Codex roles what they get from plugins", () => {
    expect(runtimeNote({ runtime: "codex" })).toContain("子代理和 hooks 只在 Claude Code 下生效");
    expect(runtimeNote({ runtime: "claude" })).toBe("");
  });
  it("keeps plugins off the coordinator and copies them from presets", () => {
    const coord = { ...newRole([], "#000"), tier: "coordinator" as const, role: "coordinator", plugins: ["tinyplug"] };
    expect(roleProblems([coord]).join()).toContain("不能加载插件");
    expect(newRole([], "#000").plugins).toEqual([]);
    const preset = { ...newRole([], "#000"), role: "designer", plugins: ["brand@2"] };
    const added = fromPreset([], preset);
    expect(added.plugins).toEqual(["brand@2"]);
    expect(added.plugins).not.toBe(preset.plugins);
  });
});
