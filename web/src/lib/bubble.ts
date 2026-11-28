import { $t, serverText } from "@/i18n";

/**
 * An employee's status bubble in the page language. Bubbles the platform makes (status/machine.go
 * defaultBubble) are Chinese message keys, and "领取 <task>" for a claim; what an employee wrote
 * with set_status is shown as written (it is only looked up as a key, so text that is not one stays).
 */
export function statusBubble(bubble: string | null | undefined): string {
  const b = bubble ?? "";
  if (!b) return "";
  const claim = /^领取 (\S+)$/.exec(b);
  if (claim) return $t("领取 {task}", { task: claim[1] });
  return serverText({ key: b }, b);
}
