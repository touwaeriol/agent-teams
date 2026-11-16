// Turns a message list into what the chat column draws: user bubbles, speaker
// articles (with the threads they opened as chips), and system lines.
import type { MessageView } from "@/api/views";

export type ChatBlock =
  | { kind: "user"; message: MessageView }
  | { kind: "card"; message: MessageView }
  | { kind: "speaker"; message: MessageView; opens: number[] }
  | { kind: "line"; message: MessageView };

function opensOf(m: MessageView): number[] {
  return m.meta?.opens_thread ? [m.meta.opens_thread] : [];
}

/** Message kinds drawn as cards in the chat (tool calls the user should see; the rest stay hidden). */
export const CARD_KINDS = new Set(["todos", "plan", "artifact", "resumed"]);

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

/** What a thread's chat shows: the user and the lead talking, errors and the chat cards; task briefs and platform notes stay out. */
export function threadTalk(m: MessageView): boolean {
  return m.role === "user" || m.role === "leader" || m.role === "error" || isCard(m);
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
