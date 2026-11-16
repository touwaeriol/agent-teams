// 定时任务: cron presets and a short Chinese description of an expression.

export const CRON_PRESETS = [
  { id: "daily9", label: "每天 9:00", cron: "0 9 * * *" },
  { id: "weekday9", label: "工作日 9:00", cron: "0 9 * * 1-5" },
  { id: "monday9", label: "每周一 9:00", cron: "0 9 * * 1" },
  { id: "hourly", label: "每小时", cron: "0 * * * *" },
] as const;

export const CUSTOM = "custom";

const WEEKDAYS = ["日", "一", "二", "三", "四", "五", "六", "日"];
const FIELD = /^[0-9*,/-]+$/;
const NUM = /^\d+$/;

export function normalizeCron(expr: string): string {
  return expr.trim().split(/\s+/).join(" ");
}

export function presetOf(expr: string): string {
  const cron = normalizeCron(expr);
  return CRON_PRESETS.find((p) => p.cron === cron)?.id ?? CUSTOM;
}

/** A quick shape check; the server parses it for real. */
export function looksLikeCron(expr: string): boolean {
  const parts = normalizeCron(expr).split(" ");
  return parts.length === 5 && parts.every((p) => FIELD.test(p));
}

const hhmm = (h: string, m: string) => `${Number(h)}:${m.padStart(2, "0")}`;

export function describeCron(expr: string): string {
  const cron = normalizeCron(expr);
  const preset = CRON_PRESETS.find((p) => p.cron === cron);
  if (preset) return preset.label;
  const [m, h, dom, mon, dow] = cron.split(" ");
  if (!NUM.test(m ?? "") || dom !== "*" || mon !== "*") return cron;
  if (h === "*" && dow === "*") return `每小时第 ${Number(m)} 分`;
  if (!NUM.test(h ?? "")) return cron;
  if (dow === "*") return `每天 ${hhmm(h, m)}`;
  if (NUM.test(dow) && Number(dow) <= 7) return `每周${WEEKDAYS[Number(dow)]} ${hhmm(h, m)}`;
  return cron;
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
