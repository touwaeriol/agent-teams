// 定时任务 form state: the draft being edited and the request body it becomes.
import type { ScheduleView } from "@/api/views";
import { browserTimeZone, CRON_PRESETS, looksLikeCron } from "@/lib/schedules";

export type Draft = { id?: string; name: string; cron: string; message: string };

export const emptyDraft = (): Draft => ({ name: "", cron: CRON_PRESETS[0].cron, message: "" });

export const draftOf = (s: Pick<ScheduleView, "id" | "name" | "cron" | "message">): Draft => ({ id: s.id, name: s.name, cron: s.cron, message: s.message });

export function scheduleBody(d: Draft, timezone = browserTimeZone()) {
  return { name: d.name.trim(), cron: d.cron.trim(), message: d.message.trim(), timezone };
}

/** Saving needs a name, a message and something shaped like a cron expression. */
export function draftValid(d: Draft): boolean {
  const b = scheduleBody(d, "UTC");
  return Boolean(b.name && b.message && looksLikeCron(b.cron));
}
