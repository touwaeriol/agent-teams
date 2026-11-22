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

const DESCRIPTORS: Record<string, string> = { "@yearly": "每年", "@annually": "每年", "@monthly": "每月", "@weekly": "每周", "@daily": "每天 0:00", "@midnight": "每天 0:00", "@hourly": "每小时" };
const EVERY = /^@every \d+[hm](\d+m)?$/;

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
  if (EVERY.test(cron)) return `每 ${cron.slice(7).replace("h", " 小时 ").replace("m", " 分钟").trim()}`;
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
  if (!s.run_at) return "一次";
  const d = new Date(s.run_at);
  return `一次 · ${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function scheduleTarget(s: Pick<ScheduleLike, "target" | "thread_no" | "thread_title" | "employee" | "task_id">): string {
  if (s.target === "member") return `${s.employee ?? "员工"}（${s.thread_no ? `#${s.thread_no} ` : ""}任务 ${s.task_id ?? ""}）`;
  if (s.target !== "lead") return "统筹";
  return s.thread_no ? `#${s.thread_no} ${s.thread_title ?? ""} 的组长`.replace(/\s+的/, " 的") : "讨论串组长";
}

const CREATORS = { user: "你", coordinator: "统筹", lead: "组长", member: "员工" } as const;
export const scheduleCreator = (s: Pick<ScheduleLike, "created_by">) => CREATORS[s.created_by] ?? s.created_by;

const STATUSES: Record<string, string> = { active: "进行中", done: "已完成", cancelled: "已取消", missed: "已错过" };
export const scheduleStatus = (status: string) => STATUSES[status] ?? status;

const OUTCOMES: Record<string, string> = { delivered: "已发出", skipped: "跳过", failed: "失败", missed: "错过" };
export const runOutcome = (outcome: string) => OUTCOMES[outcome] ?? outcome;
