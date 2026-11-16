// Cards that wait for the user come in three looks: `soft` (thread panel), `plain` (Thread page
// aside), `mobile` (44 px buttons in a grid).
import type { QuestionView } from "@/api/views";

export type CardVariant = "soft" | "plain" | "mobile";

export const FRAME: Record<CardVariant, string> = {
  soft: "border-[#F0C9A8] bg-[#FFF9F4] px-3.5 py-3",
  plain: "border-[#F0C9A8] bg-panel px-4 py-3.5",
  mobile: "border-[#F0C9A8] bg-[#FFF9F4] p-3",
};

export const CODE: Record<CardVariant, string> = {
  soft: "bg-white border border-[#F3E2D3]",
  plain: "bg-[#FFF9F4] border border-[#F3E2D3]",
  mobile: "bg-white",
};

export function askerTitle(q: QuestionView, leadName?: string): string {
  if (q.kind === "approval") return `${q.asker} 请求权限`;
  return q.asker === leadName || q.asker.startsWith("lead") ? "组长提问" : `${q.asker} 提问`;
}

/** Who asks, when it is not the person the chat is with (the lead in a thread, the coordinator). */
export function askerName(q: QuestionView, leadName?: string): string | undefined {
  if (q.asker === leadName || q.asker.startsWith("lead") || q.asker === "coordinator" || !q.thread_no) return undefined;
  return q.asker;
}
