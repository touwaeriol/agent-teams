// Turns a message list into what the chat column draws: user bubbles, speaker
// articles (with the threads they opened as chips), and system lines.
import type { MessageView } from "@/api/views";
import { serverText } from "@/i18n";

export type ChatBlock =
  | { kind: "user"; message: MessageView }
  | { kind: "card"; message: MessageView }
  | { kind: "speaker"; message: MessageView; opens: number[] }
  | { kind: "line"; message: MessageView };

function opensOf(m: MessageView): number[] {
  return m.meta?.opens_thread ? [m.meta.opens_thread] : [];
}

/** Message kinds drawn as cards in the chat (tool calls the user should see; the rest stay hidden). */
export const CARD_KINDS = new Set(["todos", "plan", "artifact", "resumed", "image", "relay"]);

export function isCard(m: Pick<MessageView, "kind">): boolean {
  return CARD_KINDS.has(m.kind);
}

/** System messages that only announce an opened thread attach to the speaker message before them. */
export function chatBlocks(messages: MessageView[]): ChatBlock[] {
  const blocks: ChatBlock[] = [];
  for (const m of messages) {
    const last = blocks[blocks.length - 1];
    if (m.role === "system" && m.meta?.opens_thread && last?.kind === "speaker") {
      last.opens.push(m.meta.opens_thread);
      continue;
    }
    if (isCard(m)) blocks.push({ kind: "card", message: m });
    else if (m.role === "user") blocks.push({ kind: "user", message: m });
    else if (m.role === "system" || m.role === "error") blocks.push({ kind: "line", message: m });
    else blocks.push({ kind: "speaker", message: m, opens: opensOf(m) });
  }
  return blocks;
}

/** What a thread's chat shows: the user and the lead talking, errors, the chat cards and the lines about wake-ups agents set; task briefs and other platform notes stay out. */
export function threadTalk(m: MessageView): boolean {
  return m.role === "user" || m.role === "leader" || m.role === "error" || isCard(m) || Boolean(m.meta?.schedule_note);
}

/** A lead reply up to this long that answers a platform check is a mere acknowledgement. */
export const TRIVIAL_REPLY = 200;

/** A platform → lead check (the stalled-thread note after a node daemon restart). */
export function platformCheck(m: Pick<MessageView, "role" | "meta">): boolean {
  return m.role === "system" && Boolean(m.meta?.stalled);
}

/**
 * The thread chat: threadTalk, minus the lead's short direct reply to a platform check (its
 * first message after the check, with nothing from you in between). The checks talk to the
 * lead, not to you; a substantial reply (a report, a summary) stays.
 */
export function threadChat(messages: MessageView[]): MessageView[] {
  const out: MessageView[] = [];
  let afterCheck = false;
  for (const m of messages) {
    if (platformCheck(m)) afterCheck = true;
    else if (m.role === "user") afterCheck = false;
    else if (m.role === "leader" && afterCheck) {
      afterCheck = false;
      if (m.kind === "chat" && m.content.trim().length <= TRIVIAL_REPLY) continue;
    }
    if (threadTalk(m)) out.push(m);
  }
  return out;
}

export type ChatItem<N = unknown> = { kind: "block"; block: ChatBlock } | { kind: "insert"; key: string; node: N };

/** Blocks and inserts in time order; an insert goes before the first message newer than it, ties stay after the message. */
export function interleave<N>(blocks: ChatBlock[], inserts: { key: string; at: string; node: N }[]): ChatItem<N>[] {
  const rest = inserts.slice().sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
  const out: ChatItem<N>[] = [];
  let i = 0;
  for (const b of blocks) {
    const t = Date.parse(b.message.created_at);
    while (i < rest.length && Date.parse(rest[i].at) < t) {
      out.push({ kind: "insert", key: rest[i].key, node: rest[i].node });
      i += 1;
    }
    out.push({ kind: "block", block: b });
  }
  for (; i < rest.length; i += 1) out.push({ kind: "insert", key: rest[i].key, node: rest[i].node });
  return out;
}

/** What a message says in the page language: a platform line's meta.text, else its content. */
export function messageText(m: Pick<MessageView, "content" | "meta">): string {
  return m.meta?.text ? serverText(m.meta.text, m.content) : m.content;
}
