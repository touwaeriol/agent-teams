// The conversation column: messages (newest at the bottom, older pages on
// demand), chips for threads the speaker opened, cards inserted where they
// were asked, and auto-scroll. Used by the coordinator chat and the thread panel.
import { ChevronRight } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";
import { flattenMessages, useMessages } from "@/api/queries";
import type { MessageView, ThreadView } from "@/api/views";
import { Loading } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { chatBlocks, interleave, type ChatBlock } from "@/lib/chat";
import { featured, isWorking, liveLine } from "@/lib/live";
import { markTone } from "@/lib/present";
import { clock, cn } from "@/lib/utils";
import { MessageImages } from "./attachments";
import { Dot, LiveDot } from "./bits";

export interface ChatProps {
  cid: string | undefined;
  threads?: ThreadView[];
  onOpenThread?: (no: number) => void;
  speakerColor?: string;
  empty?: ReactNode;
  after?: ReactNode;
  compact?: boolean;
  /** Messages to show (default: all). */
  filter?: (m: MessageView) => boolean;
  /** Cards drawn between the messages at the time they happened. */
  inserts?: { key: string; at: string; node: ReactNode }[];
}

export function Chat({ cid, threads = [], onOpenThread, speakerColor = "#C8622A", empty, after, compact, filter, inserts = [] }: ChatProps) {
  const query = useMessages(cid);
  const all = flattenMessages(query.data);
  const messages = filter ? all.filter(filter) : all;
  const end = useRef<HTMLDivElement>(null);
  const count = messages.length + inserts.length;
  useEffect(() => {
    end.current?.scrollIntoView({ block: "end" });
  }, [count]);
  if (query.isLoading) return <Loading className="py-16" />;
  const blocks = chatBlocks(messages);
  const items = interleave(blocks, inserts);
  return (
    <div className={cn("flex flex-col", compact ? "gap-[18px]" : "gap-7")}>
      {query.hasNextPage && (
        <button type="button" onClick={() => query.fetchNextPage()} className="self-center text-xs text-muted-foreground hover:text-foreground">
          {query.isFetchingNextPage ? "加载中…" : "更早的消息"}
        </button>
      )}
      {items.length === 0 && empty}
      {items.map((it) =>
        it.kind === "block" ? (
          <Block key={it.block.message.id} block={it.block} threads={threads} onOpenThread={onOpenThread} speakerColor={speakerColor} compact={compact} />
        ) : (
          <div key={it.key}>{it.node}</div>
        ),
      )}
      {after}
      <div ref={end} />
    </div>
  );
}

function Block({ block, threads, onOpenThread, speakerColor, compact }: { block: ChatBlock; threads: ThreadView[]; onOpenThread?: (no: number) => void; speakerColor: string; compact?: boolean }) {
  const m = block.message;
  if (block.kind === "user")
    return (
      <div className="flex max-w-[85%] flex-col gap-2 self-end">
        <MessageImages items={m.meta?.attachments} />
        {m.content && <div className={cn("self-end whitespace-pre-wrap rounded-2xl bg-raised px-4 py-2.5", compact ? "leading-6" : "leading-[26px]")}>{m.content}</div>}
      </div>
    );
  if (block.kind === "line")
    return <p className={cn("text-center text-xs", m.role === "error" ? "text-destructive" : "text-muted-foreground")}>{m.content}</p>;
  return (
    <article className="flex flex-col gap-2.5">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Dot color={speakerColor} size={8} />
        {m.author || "leader"} · {clock(m.created_at)}
        {m.kind === "interrupt" && <span className="text-[#B4530B]">· 插话</span>}
      </span>
      <Markdown className={cn(compact ? "text-sm leading-[26px]" : "text-[15px] leading-7")}>{m.content}</Markdown>
      {block.opens.map((no) => (
        <ThreadChip key={no} thread={threads.find((t) => t.no === no)} no={no} onOpen={onOpenThread} />
      ))}
    </article>
  );
}

/** A thread the speaker opened, with its live status underneath while it runs. */
export function ThreadChip({ thread, no, onOpen }: { thread?: ThreadView; no: number; onOpen?: (no: number) => void }) {
  const tone = thread ? markTone(thread) : "#8A877F";
  const line = thread ? liveLine(thread) : "";
  const asks = (thread?.counts.waiting ?? 0) > 0;
  const working = Boolean(thread && featured(thread) && isWorking(featured(thread)!));
  return (
    <button
      type="button"
      onClick={() => onOpen?.(no)}
      className="flex min-h-8 max-w-full flex-col items-start gap-0.5 self-start rounded-lg border bg-panel px-3 py-1.5 text-left text-[13px] hover:border-input"
    >
      <span className="flex items-center gap-2">
        <Dot color={tone} />
        讨论串 #{no} {thread?.title ?? ""}
        <ChevronRight className="size-3 text-[#8A877F]" strokeWidth={2} />
      </span>
      {line && (
        <span className={cn("flex min-w-0 max-w-full items-center gap-1.5 pl-[15px] text-xs", asks ? "text-[#B4530B]" : "text-muted-foreground")}>
          {working && !asks && <LiveDot color="#2F6FDB" on size={6} />}
          <span className="truncate">{line}</span>
        </span>
      )}
    </button>
  );
}
