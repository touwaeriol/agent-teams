// Session controls (#53): the permission mode, model and thinking effort of a session's employee,
// as the server lists them (GET /api/projects/{pid}/session-controls, GET /api/session-options).
// The server already filters the menus by CLI, model, role and node; this file only numbers,
// groups, labels and colours them.
import type { components } from "@/api/schema";

export type ModeOption = components["schemas"]["ModeOption"];
export type ModelChoice = components["schemas"]["ModelChoice"];
export type SessionControls = components["schemas"]["SessionControls"];
export type SessionOptions = components["schemas"]["SessionOptions"];
export type ModeId = ModeOption["id"];

/** Look of each mode: the chip's state text and tone, and its icon name (lucide). */
export const MODE_LOOK: Record<ModeId, { state: string; short: string; tone: "none" | "info" | "review" | "warn"; icon: "sparkles" | "hand" | "pen" | "list" | "shield" }> = {
  auto: { state: "自动审批", short: "自动", tone: "info", icon: "sparkles" },
  default: { state: "修改前询问", short: "手动", tone: "none", icon: "hand" },
  acceptEdits: { state: "自动接受编辑", short: "接受编辑", tone: "info", icon: "pen" },
  plan: { state: "计划模式", short: "计划", tone: "review", icon: "list" },
  bypassPermissions: { state: "跳过权限确认", short: "跳过确认", tone: "warn", icon: "shield" },
};

/** Chip colours by tone (text, border, background), from the design's palette. */
export const TONE_CLASS: Record<string, string> = {
  none: "border-[#DCD9D2] text-muted-foreground",
  info: "border-[#C9D8F3] bg-[#F1F5FD] text-[#2F6FDB]",
  review: "border-[#D9CCF2] bg-[#F6F2FC] text-[#7A4FD1]",
  warn: "border-[#EBC9AE] bg-[#FBF3EC] text-[#C2570C]",
  pending: "border-[#C9D8F3] bg-[#F7F9FE] text-[#2F6FDB]",
};

const EFFORT_LABELS: Record<string, string> = { none: "关闭", minimal: "最低", low: "低", medium: "中", high: "高", xhigh: "超高", max: "最高" };

/** 低 / 中 / 高 …; an unknown level shows as is. */
export function effortLabel(e: string): string {
  return EFFORT_LABELS[e] ?? e;
}

export function isMode(id: string): id is ModeId {
  return id in MODE_LOOK;
}

/** One numbered menu row; `danger` rows (跳过权限确认) go under a divider. */
export interface ModeRow extends ModeOption {
  key: number;
  danger: boolean;
}

/**
 * The menu as shown: the server's order with bypass last (under a divider), numbered 1..n in that
 * order — the number is the shortcut while the menu is open.
 */
export function modeRows(modes: ModeOption[] | null | undefined): ModeRow[] {
  const list = modes ?? [];
  const ordered = [...list.filter((m) => m.id !== "bypassPermissions"), ...list.filter((m) => m.id === "bypassPermissions")];
  return ordered.map((m, i) => ({ ...m, key: i + 1, danger: m.id === "bypassPermissions" }));
}

/** The row a digit key picks ("1".."9"), or undefined. */
export function rowForKey(rows: ModeRow[], key: string): ModeRow | undefined {
  if (!/^[1-9]$/.test(key)) return undefined;
  return rows.find((r) => r.key === Number(key));
}

/** When a switch takes effect, for the menu's footer (by the modes' applies). */
export function appliesNote(runtime: string, modes: ModeOption[] | null | undefined): string {
  const now = (modes ?? []).some((m) => m.applies === "now") || (runtime === "claude" && !(modes ?? []).length);
  if (now) return "立即生效：从下一次工具调用起按新模式。";
  if (runtime === "codex") return "下一轮生效。Codex 没有“接受编辑”和“计划”模式。";
  if (runtime === "antigravity") return "下一轮生效。Antigravity 只有“手动”和“跳过权限确认”两档。";
  return "下一轮生效。";
}

/** The model row for an id (undefined when the runtime did not list it). */
export function modelOf(models: ModelChoice[] | null | undefined, id: string): ModelChoice | undefined {
  return (models ?? []).find((m) => m.id === id);
}

/** Short display name: the runtime's name, else the id without its claude- prefix. */
export function modelLabel(models: ModelChoice[] | null | undefined, id: string, runtime = "claude"): string {
  const m = modelOf(models, id);
  if (m?.name) return m.name;
  if (!id) return runtime === "codex" ? "Codex 默认" : runtime === "antigravity" ? "Antigravity 默认" : "默认模型";
  return id;
}

/** "Opus 5.5 · 高" (no effort part for a runtime without one or while it runs on the default). */
export function modelChipText(models: ModelChoice[] | null | undefined, model: string, effort: string, runtime: string): string {
  const name = modelLabel(models, model, runtime);
  if (runtime === "antigravity") return name;
  return effort ? `${name} · ${effortLabel(effort)}` : name;
}

/** The effort to keep when the model changes: the current one if the new model takes it, else its default (or none). */
export function effortFor(model: ModelChoice | undefined, current: string): string {
  if (!model) return current;
  const list = model.efforts ?? [];
  if (list.includes(current)) return current;
  return model.default_effort && list.includes(model.default_effort) ? model.default_effort : "";
}

/** Claude's current model lacks auto mode (the menu hides 自动 then): for the footer note. */
export function noAutoNote(runtime: string, models: ModelChoice[] | null | undefined, model: string): string {
  const m = modelOf(models, model);
  if (runtime !== "claude" || !m || m.supports_auto) return "";
  return `${modelLabel(models, model)} 不支持自动模式，所以菜单里没有“自动”。`;
}
