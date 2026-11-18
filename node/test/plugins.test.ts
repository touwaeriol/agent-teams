import { existsSync, mkdtempSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { buildClaudeOptions } from "../src/sessions/claude/options.js";
import { codexMcpServers, materializePlugins, sdkMcpServers, withRoot } from "../src/sessions/plugins.js";
import type { PluginBundle } from "../src/protocol.js";

const employee = { name: "dev-1", tier: "member" as const, project_id: "p1", thread_no: 3 };

function tinyplug(version = 2): PluginBundle {
  return {
    name: "tinyplug", version,
    files: [
      { path: ".claude-plugin/plugin.json", content: '{"name":"tinyplug"}' },
      { path: "skills/greet/SKILL.md", content: "PINEAPPLE-42" },
      { path: "mcp/server.sh", content: Buffer.from("#!/bin/sh\necho hi\n").toString("base64"), encoding: "base64", executable: true },
    ],
    skills: ["tinyplug:greet", "tinyplug:shout"], agents: ["tinyplug:echoer"],
    mcp_servers: {
      calc: { type: "stdio", command: "${CLAUDE_PLUGIN_ROOT}/mcp/server.sh", args: ["--root", "${CLAUDE_PLUGIN_ROOT}"], env: { DATA: "${CLAUDE_PLUGIN_ROOT}/data" } },
      docs: { type: "sse", url: "https://example.com/sse", headers: { "X-Root": "${CLAUDE_PLUGIN_ROOT}" } },
    },
  };
}

describe("plugins", () => {
  it("writes each plugin at its exact version into a per-employee directory outside the worktree", () => {
    const root = mkdtempSync(join(tmpdir(), "at-plugins-"));
    const [p] = materializePlugins(join(root, "agent-plugins"), { employee, plugins: [tinyplug()] });
    expect(p!.dir).toBe(join(root, "agent-plugins", "p1", "3", "dev-1", "tinyplug@2"));
    expect(readFileSync(join(p!.dir, "skills", "greet", "SKILL.md"), "utf8")).toBe("PINEAPPLE-42");
    expect(readFileSync(join(p!.dir, "mcp", "server.sh"), "utf8")).toBe("#!/bin/sh\necho hi\n");
    expect(statSync(join(p!.dir, "mcp", "server.sh")).mode & 0o111).not.toBe(0);
    expect(statSync(join(p!.dir, "skills", "greet", "SKILL.md")).mode & 0o111).toBe(0);
    expect(p!.skills).toEqual(["tinyplug:greet", "tinyplug:shout"]);
    expect(p!.mcpServers.plugin_tinyplug_calc!.command).toBe(join(p!.dir, "mcp", "server.sh"));
    // A new version replaces the old directory; no plugins clears it.
    materializePlugins(join(root, "agent-plugins"), { employee, plugins: [tinyplug(3)] });
    expect(existsSync(p!.dir)).toBe(false);
    expect(existsSync(join(root, "agent-plugins", "p1", "3", "dev-1", "tinyplug@3"))).toBe(true);
    expect(materializePlugins(join(root, "agent-plugins"), { employee, plugins: [] })).toEqual([]);
    expect(existsSync(join(root, "agent-plugins", "p1", "3", "dev-1"))).toBe(false);
  });

  it("keeps a project member's plugins in <project>/project/<name> whatever thread it works in", () => {
    const root = mkdtempSync(join(tmpdir(), "at-plugins-"));
    const shared = { ...employee, scope: "project" as const };
    const [a] = materializePlugins(root, { employee: shared, plugins: [tinyplug()] });
    expect(a!.dir).toBe(join(root, "p1", "project", "dev-1", "tinyplug@2"));
    const [b] = materializePlugins(root, { employee: { ...shared, thread_no: 7 }, plugins: [tinyplug()] });
    expect(b!.dir).toBe(a!.dir);
    expect(existsSync(join(root, "p1", "7"))).toBe(false);
    // A thread-scoped employee (a lead) stays under its thread number.
    const [lead] = materializePlugins(root, { employee: { ...employee, name: "lead-3", tier: "lead", scope: "thread" }, plugins: [tinyplug()] });
    expect(lead!.dir).toBe(join(root, "p1", "3", "lead-3", "tinyplug@2"));
  });

  it("never gives the coordinator plugins", () => {
    const root = mkdtempSync(join(tmpdir(), "at-plugins-"));
    expect(materializePlugins(root, { employee: { ...employee, tier: "coordinator", thread_no: null }, plugins: [tinyplug()] })).toEqual([]);
  });

  it("refuses names and paths that escape the plugin directory", () => {
    const root = mkdtempSync(join(tmpdir(), "at-plugins-"));
    const bad = (patch: Partial<PluginBundle>) => () => materializePlugins(root, { employee, plugins: [{ ...tinyplug(), ...patch }] });
    expect(bad({ name: "../evil" })).toThrow();
    expect(bad({ version: 0 })).toThrow();
    expect(bad({ files: [{ path: "../../x", content: "x" }] })).toThrow();
    expect(bad({ files: [{ path: "a/./b", content: "x" }] })).toThrow();
    expect(bad({ files: [{ path: "/etc/x", content: "x" }] })).toThrow();
  });

  it("replaces ${CLAUDE_PLUGIN_ROOT} in every MCP field", () => {
    const s = withRoot(tinyplug().mcp_servers!.calc!, "/d");
    expect(s).toEqual({ type: "stdio", command: "/d/mcp/server.sh", args: ["--root", "/d"], env: { DATA: "/d/data" } });
    expect(withRoot(tinyplug().mcp_servers!.docs!, "/d").headers).toEqual({ "X-Root": "/d" });
  });

  it("passes plugins next to the skills plugin, lists their skills and owns their MCP servers", () => {
    const root = mkdtempSync(join(tmpdir(), "at-plugins-"));
    const plugins = materializePlugins(root, { employee, plugins: [tinyplug()] });
    const base = { root: "/r", cwd: "/r/p", env: {}, sessionId: "u1", resume: false, server: { type: "sdk", name: "platform" } as never, canUseTool: (async () => ({ behavior: "deny", message: "" })) as never, hooks: {}, secretPaths: [] };
    const claude = { model: "claude-opus-5-5", tools: ["Read", "Skill", "Agent"], allowed_tools: ["mcp__platform__*", "Skill"], permission_mode: "default" as const, setting_sources: [] as [] };
    const o = buildClaudeOptions({ ...base, claude, skills: { pluginDir: "/r/agent-skills/x", names: ["agent-teams-skills:design"] }, plugins });
    expect(o.plugins).toEqual([{ type: "local", path: "/r/agent-skills/x", skipMcpDiscovery: true }, { type: "local", path: plugins[0]!.dir, skipMcpDiscovery: true }]);
    expect(o.skills).toEqual(["agent-teams-skills:design", "tinyplug:greet", "tinyplug:shout"]);
    expect(o.strictMcpConfig).toBe(true);
    expect(Object.keys(o.mcpServers!).sort()).toEqual(["platform", "plugin_tinyplug_calc", "plugin_tinyplug_docs"]);
    expect(o.mcpServers!.plugin_tinyplug_calc).toMatchObject({ type: "stdio", command: join(plugins[0]!.dir, "mcp", "server.sh") });
    // Plugin MCP tools are not pre-approved: they reach canUseTool.
    expect(o.allowedTools).toEqual(["mcp__platform__*", "Skill"]);
    // Plugins alone still pin the skill list, so user-level skills stay hidden.
    const only = buildClaudeOptions({ ...base, claude, plugins: [{ ...plugins[0]!, skills: [] }] });
    expect(only.skills).toEqual([]);
  });

  it("maps MCP servers for the SDK and for Codex (no SSE there)", () => {
    const root = mkdtempSync(join(tmpdir(), "at-plugins-"));
    const plugins = materializePlugins(root, { employee, plugins: [tinyplug()] });
    expect(sdkMcpServers(plugins).plugin_tinyplug_docs).toEqual({ type: "sse", url: "https://example.com/sse", headers: { "X-Root": plugins[0]!.dir } });
    const codex = codexMcpServers(plugins);
    expect(Object.keys(codex)).toEqual(["plugin_tinyplug_calc"]);
    expect(codex.plugin_tinyplug_calc).toMatchObject({ command: join(plugins[0]!.dir, "mcp", "server.sh"), args: ["--root", plugins[0]!.dir] });
  });
});
