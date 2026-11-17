// Thread reminders in the browser: ask for notification permission when the user first sets a
// reminder, and when one fires (the thread's reminded_at appears) show a system notification
// if allowed, else an in-app toast. Each firing is announced once per page.
import { onBeforeUnmount, watch } from "vue";
import type { ThreadView } from "@/api/views";
import { toast } from "./toast";

const hasNotifications = () => typeof window !== "undefined" && "Notification" in window;

/** Asks for notification permission the first time (does nothing once granted or denied). */
export async function askNotificationPermission(): Promise<void> {
  if (!hasNotifications() || Notification.permission !== "default") return;
  try {
    await Notification.requestPermission();
  } catch {
    // older browsers: callback form only; the toast fallback covers them
  }
}

/** Fired reminders not announced yet; ones that fired before `since` (page load) stay quiet. */
export function freshReminders(threads: ThreadView[], seen: Set<string>, since: number): ThreadView[] {
  return threads.filter((t) => {
    if (!t.reminded_at) return false;
    const key = `${t.id}@${t.reminded_at}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return Date.parse(t.reminded_at) >= since;
  });
}

const seen = new Set<string>();

function announce(t: ThreadView, open: (no: number) => void) {
  const title = `提醒：#${t.no} ${t.title}`;
  if (hasNotifications() && Notification.permission === "granted") {
    try {
      const n = new Notification(title, { body: "你设置的提醒到了，讨论串已回到“进行中”。", tag: `reminder-${t.id}` });
      n.onclick = () => {
        window.focus();
        open(t.no);
        n.close();
      };
      return;
    } catch {
      // some browsers only allow notifications from a service worker: fall back to the toast
    }
  }
  toast.info(`${title} — 提醒时间到了`);
}

/** Announces reminders that fire while this page is open. */
export function useReminderAlerts(threads: () => ThreadView[], open: (no: number) => void) {
  const since = Date.now() - 60_000;
  const stop = watch(threads, (list) => freshReminders(list, seen, since).forEach((t) => announce(t, open)), { immediate: true });
  onBeforeUnmount(stop);
}
