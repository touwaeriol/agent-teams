import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { QuestionView } from "@/api/views";

/** Answers an approval (allow / allow_always / deny + reason) and refreshes the waiting lists. */
export function useAnswer(cid: () => string, q: () => QuestionView, pid: () => string) {
  return useAction((v: { answer: string; reason?: string }) => conversations.answer(cid(), q().id, v.answer, v.reason), {
    invalidate: () => [keys.questions(cid()), keys.waiting(pid()), keys.threads(pid())],
  });
}
