// Question cards (ask_user, Claude's AskUserQuestion): pure helpers behind AskCard.
// A plain question (text + options) is drawn as one structured question without
// descriptions, so both kinds share the same card.
import type { AskAnswer, AskChoice, AskItem, QuestionView } from "@/api/views";

/** The card's questions: detail.questions, or the plain question with its options. */
export function askItems(q: Pick<QuestionView, "text" | "options" | "detail">): AskItem[] {
  if (q.detail?.questions?.length) return q.detail.questions;
  return [{ question: q.text, header: "", options: (q.options ?? []).map((label) => ({ label })) }];
}

/** A, B, C… for the option badges. */
export function letter(i: number): string {
  return String.fromCharCode(65 + (i % 26));
}

export function emptyChoices(items: AskItem[]): AskChoice[] {
  return items.map(() => ({ selected: [], other: "" }));
}

/** Clicking an option: single select replaces (and clears 其他), multi select toggles. */
export function toggle(choice: AskChoice, label: string, multi: boolean): AskChoice {
  if (!multi) return { selected: choice.selected[0] === label ? [] : [label], other: "" };
  const on = choice.selected.includes(label);
  return { ...choice, selected: on ? choice.selected.filter((x) => x !== label) : [...choice.selected, label] };
}

/** Typing into 其他: single select drops the picked option. */
export function typeOther(choice: AskChoice, text: string, multi: boolean): AskChoice {
  return { selected: multi || !text ? choice.selected : [], other: text };
}

/** Every question has an option or a typed answer. */
export function complete(choices: AskChoice[]): boolean {
  return choices.length > 0 && choices.every((c) => c.selected.length > 0 || Boolean(c.other?.trim()));
}

/** One question with single select answers with one click (like Claude Code's card). */
export function oneClick(items: AskItem[]): boolean {
  return items.length === 1 && !items[0].multi_select;
}

/** The body the answer endpoint takes. Plain questions send text; structured ones choices. */
export function answerBody(q: Pick<QuestionView, "detail">, choices: AskChoice[]): { answer: string; answers?: AskChoice[] } {
  const clean = choices.map((c) => ({ selected: c.selected, ...(c.other?.trim() ? { other: c.other.trim() } : {}) }));
  if (q.detail?.questions?.length) return { answer: "", answers: clean };
  const c = clean[0];
  return { answer: c.other ?? c.selected[0] ?? "" };
}

/** The card was closed by a message the user typed in the chat (the message itself is the answer). */
export function answeredInChat(q: Pick<QuestionView, "detail">): boolean {
  return q.detail?.answered_in_chat === true;
}

/** What the user chose, per question, for the collapsed card (nothing when answered in the chat). */
export function answeredRows(q: Pick<QuestionView, "text" | "options" | "detail" | "answer">): AskAnswer[] {
  const items = askItems(q);
  if (answeredInChat(q)) return items.map((i) => ({ question: i.question, header: i.header, selected: [] }));
  if (q.detail?.answers?.length) return q.detail.answers;
  const text = q.answer ?? "";
  const picked = items[0].options.some((o) => o.label === text);
  return [{ question: items[0].question, header: items[0].header, selected: picked ? [text] : [], ...(picked || !text ? {} : { other: text }) }];
}

/** Cards a chat shows where they were asked: the user's questions (open and answered) and open approvals. */
export function chatQuestions(list: QuestionView[] | undefined): QuestionView[] {
  return (list ?? []).filter((q) => q.audience === "user" && (q.kind === "question" ? q.status !== "cancelled" : q.status === "open"));
}
