// Plugin library helpers (pure): role entries are "name" (follows the latest version) or
// "name@3" (pinned), mirroring server/internal/plugins.ParseRef.
import { $t } from "@/i18n";
import type { PluginContents, PluginView, RoleView } from "@/api/views";
import { roleRuntimes, runtimeLabel } from "@/lib/runtimes";

export interface PluginRef {
  name: string;
  /** 0 = latest */
  version: number;
}

export function parseRef(s: string): PluginRef {
  const [name = "", ver] = s.split("@", 2);
  const n = ver === undefined ? 0 : Number(ver);
  return { name, version: Number.isInteger(n) && n > 0 ? n : 0 };
}

export function formatRef(r: PluginRef): string {
  return r.version > 0 ? `${r.name}@${r.version}` : r.name;
}

/** The role's entry for a plugin name, if ticked. */
export function refFor(refs: string[] | null | undefined, name: string): PluginRef | undefined {
  return (refs ?? []).map(parseRef).find((r) => r.name === name);
}

/** Ticks (latest version) or unticks a plugin. */
export function togglePlugin(refs: string[] | null | undefined, name: string): string[] {
  const list = refs ?? [];
  return refFor(list, name) ? list.filter((s) => parseRef(s).name !== name) : [...list, name];
}

/** Pins a ticked plugin to a version (0 = follow the latest), keeping its position. */
export function pinPlugin(refs: string[] | null | undefined, name: string, version: number): string[] {
  return (refs ?? []).map((s) => (parseRef(s).name === name ? formatRef({ name, version }) : s));
}

/** Short Chinese summary of what a version provides: "技能 2 · 命令 1 · 子代理 1 · hooks · MCP 1". */
export function contentsText(c: PluginContents | undefined): string {
  if (!c) return "";
  const parts = [
    count($t("技能"), c.skills),
    count($t("命令"), c.commands),
    count($t("子代理"), c.agents),
    c.hooks ? "hooks" : "",
    count("MCP", c.mcp_servers),
  ];
  return parts.filter(Boolean).join(" · ");
}

function count(label: string, list: string[] | null | undefined): string {
  return list && list.length > 0 ? `${label} ${list.length}` : "";
}

/** The version a role entry resolves to, or undefined when it no longer exists. */
export function resolvedVersion(p: PluginView, ref: PluginRef): number | undefined {
  const want = ref.version || p.latest;
  return (p.versions ?? []).some((v) => v.version === want) ? want : undefined;
}

/** Ticked entries whose plugin is gone from the library (deleted, or a disabled built-in). */
export function missingRefs(refs: string[] | null | undefined, library: PluginView[]): PluginRef[] {
  return (refs ?? []).map(parseRef).filter((r) => !library.some((p) => p.name === r.name));
}

/** What a role on a runtime without Claude Code's plugin system (Codex, Antigravity) gets from its plugins: only skills, commands and MCP servers. */
export function runtimeNote(role: Pick<RoleView, "runtime"> & Partial<Pick<RoleView, "runtimes">>): string {
  const others = roleRuntimes(role).filter((rt) => rt !== "claude");
  if (!others.length) return "";
  return $t("{others} 员工只用插件里的技能、命令（作为说明文字）和 MCP 服务器；子代理和 hooks 只在 Claude Code 下生效。", { others: others.map(runtimeLabel).join(" / ") });
}

/** My plugins first, then the built-ins, each by name. */
export function splitLibrary(list: PluginView[]): { mine: PluginView[]; builtin: PluginView[] } {
  const byName = (a: PluginView, b: PluginView) => a.name.localeCompare(b.name);
  return { mine: list.filter((p) => !p.builtin).sort(byName), builtin: list.filter((p) => p.builtin).sort(byName) };
}
