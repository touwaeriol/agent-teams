// 定时任务 form state: the draft being edited and the request body it becomes.
import type { ScheduleView } from "@/api/views";
import { browserTimeZone, CRON_PRESETS, looksLikeCron } from "@/lib/schedules";

export type Draft = {
  id?: string;
  name: string;
  kind: "cron" | "once";
  cron: string;
  /** once: when it fires (local time picked in the form) */
  runAt: Date | null;
  /** null = the project coordinator; a number = that thread's lead (fixed after creation) */
  threadNo: number | null;
  message: string;
};

const inAnHour = () => {
  const d = new Date(Date.now() + 3_600_000);
  d.setMinutes(0, 0, 0);
  return d;
};

export const emptyDraft = (): Draft => ({ name: "", kind: "cron", cron: CRON_PRESETS[0].cron, runAt: inAnHour(), threadNo: null, message: "" });

export const draftOf = (s: Pick<ScheduleView, "id" | "name" | "kind" | "cron" | "run_at" | "thread_no" | "message">): Draft => ({
  id: s.id,
  name: s.name,
  kind: s.kind,
  cron: s.kind === "cron" ? s.cron : CRON_PRESETS[0].cron,
  runAt: s.run_at ? new Date(s.run_at) : inAnHour(),
  threadNo: s.thread_no ?? null,
  message: s.message,
});

export function scheduleBody(d: Draft, timezone = browserTimeZone()) {
  const base = { name: d.name.trim(), kind: d.kind, message: d.message.trim(), timezone };
  return d.kind === "once" ? { ...base, run_at: d.runAt?.toISOString() } : { ...base, cron: d.cron.trim() };
}

/** Saving needs a name, a message and a cron-shaped expression or a time in the future. */
export function draftValid(d: Draft, now = Date.now()): boolean {
  const b = scheduleBody(d, "UTC");
  if (!b.name || !b.message) return false;
  return d.kind === "once" ? Boolean(d.runAt && d.runAt.getTime() > now) : looksLikeCron(d.cron);
}
