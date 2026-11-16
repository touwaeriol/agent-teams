// 项目设置 dialog: sections, the instructions limit, worktree choice, model and effort choices, usage numbers.

export const SETTINGS_SECTIONS = [
  { id: "general", label: "通用" },
  { id: "memory", label: "记忆" },
  { id: "environment", label: "环境" },
  { id: "usage", label: "使用情况" },
  { id: "advanced", label: "高级" },
] as const;

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number]["id"];

export function sectionOf(value: string | undefined): SettingsSection {
  return SETTINGS_SECTIONS.find((s) => s.id === value)?.id ?? "general";
}

/** 项目指令 (the project's `context`) counts to 16,000 characters, like the API. */
export const INSTRUCTIONS_MAX = 16000;

/** 环境 → 工作树: the two work modes the daemon implements (git worktree, or a full clone each). */
export const WORKTREE_CHOICES = [
  { value: "worktree" as const, label: "开启，使用工作树", desc: "每个节点克隆一份仓库，讨论串和每个成员各开一个 worktree：省磁盘、开工快，并行互不干扰。" },
  { value: "clone" as const, label: "关闭，直接在检出目录工作", desc: "不用 worktree：每个员工一份完整克隆，直接在自己的检出目录里工作。最稳，但占磁盘。" },
];

export type WorkMode = (typeof WORKTREE_CHOICES)[number]["value"];

export function worktreeChoice(mode: string) {
  return WORKTREE_CHOICES.find((c) => c.value === mode) ?? WORKTREE_CHOICES[0];
}

export const DEFAULT_MODEL = "claude-opus-5-5";

const MODEL_NAMES: Record<string, string> = { [DEFAULT_MODEL]: "Opus 5.5" };

export function modelLabel(id: string): string {
  if (!id) return "跟随岗位设置";
  return MODEL_NAMES[id] ?? id;
}

/** Model choices: follow the role, the platform default, and whatever is saved now. */
export function modelOptions(current: string): { value: string; label: string }[] {
  const ids = ["", DEFAULT_MODEL];
  if (current && !ids.includes(current)) ids.push(current);
  return ids.map((value) => ({ value, label: modelLabel(value) }));
}

export const EFFORTS = [
  { value: "", label: "跟随岗位" },
  { value: "low", label: "低" },
  { value: "medium", label: "中等" },
  { value: "high", label: "高" },
  { value: "xhigh", label: "超高" },
  { value: "max", label: "最高" },
] as const;

export function effortLabel(value: string): string {
  return EFFORTS.find((e) => e.value === value)?.label ?? value;
}

/** 950 / 12.3K / 4.5M tokens. */
export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${Number((n / 1000).toFixed(1))}K`;
  return `${Number((n / 1_000_000).toFixed(1))}M`;
}

export function formatUSD(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return `$${n < 10 ? n.toFixed(2) : n.toFixed(1)}`;
}

/** The days of the window, oldest first, each with its cost (0 when nothing ran). */
export function fillDays(byDay: { day: string; cost_usd: number }[], days: number, today = new Date()): { day: string; cost: number }[] {
  const cost = new Map(byDay.map((d) => [d.day, d.cost_usd]));
  const out: { day: string; cost: number }[] = [];
  for (let i = days - 1; i >= 0; i -= 1) {
    const d = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - i));
    const key = d.toISOString().slice(0, 10);
    out.push({ day: key, cost: cost.get(key) ?? 0 });
  }
  return out;
}
