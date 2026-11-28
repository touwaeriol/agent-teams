// 定时任务: cron presets and a short Chinese description of an expression.
import { $t } from "@/i18n";

export const CRON_PRESETS = [
  { id: "daily9", label: $t("每天 9:00"), cron: "0 9 * * *" },
  { id: "weekday9", label: $t("工作日 9:00"), cron: "0 9 * * 1-5" },
  { id: "monday9", label: $t("每周一 9:00"), cron: "0 9 * * 1" },
  { id: "hourly", label: $t("每小时"), cron: "0 * * * *" },
] as const;

export const CUSTOM = "custom";

const WEEKDAYS = [$t("日"), $t("一"), $t("二"), $t("三"), $t("四"), $t("五"), $t("六"), $t("日")];
const FIELD = /^[0-9*,/-]+$/;
const NUM = /^\d+$/;

export function normalizeCron(expr: string): string {
  return expr.trim().split(/\s+/).join(" ");
}

export function presetOf(expr: string): string {
  const cron = normalizeCron(expr);
  return CRON_PRESETS.find((p) => p.cron === cron)?.id ?? CUSTOM;
}

const DESCRIPTORS: Record<string, string> = { "@yearly": $t("每年"), "@annually": $t("每年"), "@monthly": $t("每月"), "@weekly": $t("每周"), "@daily": $t("每天 0:00"), "@midnight": $t("每天 0:00"), "@hourly": $t("每小时") };
const EVERY = /^@every \d+[hm](\d+m)?$/;

/** "2h30m" → "每 2 小时 30 分钟". */
function everyText(spec: string): string {
  const h = Number(/(\d+)h/.exec(spec)?.[1] ?? 0);
  const m = Number(/(\d+)m/.exec(spec)?.[1] ?? 0);
  if (h && m) return $t("每 {h} 小时 {m} 分钟", { h, m });
  return h ? $t("每 {h} 小时", { h }) : $t("每 {m} 分钟", { m });
}

/** A quick shape check; the server parses it for real. */
export function looksLikeCron(expr: string): boolean {
  const cron = normalizeCron(expr);
  if (cron.startsWith("@")) return cron in DESCRIPTORS || EVERY.test(cron);
  const parts = cron.split(" ");
  return parts.length === 5 && parts.every((p) => FIELD.test(p));
}

const hhmm = (h: string, m: string) => `${Number(h)}:${m.padStart(2, "0")}`;

export function describeCron(expr: string): string {
  const cron = normalizeCron(expr);
  const preset = CRON_PRESETS.find((p) => p.cron === cron);
  if (preset) return preset.label;
  if (cron in DESCRIPTORS) return DESCRIPTORS[cron];
  if (EVERY.test(cron)) return everyText(cron.slice(7));
  const [m, h, dom, mon, dow] = cron.split(" ");
  if (!NUM.test(m ?? "") || dom !== "*" || mon !== "*") return cron;
  if (h === "*" && dow === "*") return $t("每小时第 {m} 分", { m: Number(m) });
  if (!NUM.test(h ?? "")) return cron;
  if (dow === "*") return $t("每天 {hhmm}", { hhmm: hhmm(h, m) });
  if (NUM.test(dow) && Number(dow) <= 7) return $t("每周{n} {hhmm}", { n: WEEKDAYS[Number(dow)], hhmm: hhmm(h, m) });
  return cron;
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

type ScheduleLike = {
  kind: "cron" | "once";
  cron: string;
  run_at?: string | null;
  target: "coordinator" | "lead" | "member";
  thread_no?: number | null;
  thread_title?: string | null;
  employee?: string | null;
  task_id?: string | null;
  created_by: "user" | "coordinator" | "lead" | "member";
};

const pad = (n: number) => String(n).padStart(2, "0");

/** 周期的说明（describeCron），一次性的写“一次 · 11-23 15:00”（本地时间）。 */
export function describeSchedule(s: Pick<ScheduleLike, "kind" | "cron" | "run_at">): string {
  if (s.kind === "cron") return describeCron(s.cron);
  if (!s.run_at) return $t("一次");
  const d = new Date(s.run_at);
  return $t("一次 · {month}-{day} {hour}:{minute}", { month: pad(d.getMonth() + 1), day: pad(d.getDate()), hour: pad(d.getHours()), minute: pad(d.getMinutes()) });
}

export function scheduleTarget(s: Pick<ScheduleLike, "target" | "thread_no" | "thread_title" | "employee" | "task_id">): string {
  if (s.target === "member") return $t("{n}（{n2}任务 {n3}）", { n: s.employee ?? $t("员工"), n2: s.thread_no ? `#${s.thread_no} ` : "", n3: s.task_id ?? "" });
  if (s.target !== "lead") return $t("统筹");
  return s.thread_no ? $t("#{thread_no} {n} 的组长", { thread_no: s.thread_no, n: s.thread_title ?? "" }).replace(/\s+的/, " 的") : $t("讨论串组长");
}

const CREATORS = { user: $t("你"), coordinator: $t("统筹"), lead: $t("组长"), member: $t("员工") } as const;
export const scheduleCreator = (s: Pick<ScheduleLike, "created_by">) => CREATORS[s.created_by] ?? s.created_by;

const STATUSES: Record<string, string> = { active: $t("进行中"), done: $t("已完成"), cancelled: $t("已取消"), missed: $t("已错过") };
export const scheduleStatus = (status: string) => STATUSES[status] ?? status;

const OUTCOMES: Record<string, string> = { delivered: $t("已发出"), skipped: $t("跳过"), failed: $t("失败"), missed: $t("错过") };
export const runOutcome = (outcome: string) => OUTCOMES[outcome] ?? outcome;
