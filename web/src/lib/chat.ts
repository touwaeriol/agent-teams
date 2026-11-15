// Turns a message list into what the chat column draws: user bubbles, speaker
// articles (with the threads they opened as chips), and system lines.
import type { MessageView } from "@/api/views";

export type ChatBlock =
  | { kind: "user"; message: MessageView }
  | { kind: "speaker"; message: MessageView; opens: number[] }
  | { kind: "line"; message: MessageView };

function opensOf(m: MessageView): number[] {
  return m.meta?.opens_thread ? [m.meta.opens_thread] : [];
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
    if (m.role === "user") blocks.push({ kind: "user", message: m });
    else if (m.role === "system" || m.role === "error") blocks.push({ kind: "line", message: m });
    else blocks.push({ kind: "speaker", message: m, opens: opensOf(m) });
  }
  return blocks;
}
