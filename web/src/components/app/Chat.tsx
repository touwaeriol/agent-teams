// The conversation column: messages (newest at the bottom, older pages on
// demand), chips for threads the speaker opened, chat cards (todos, plan,
// artifact, 会话已恢复), question cards inserted where they were asked, and
// scrolling that follows new messages and comes back to where you were. Used by the coordinator chat and the thread panel.
import { ChevronRight } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, type ReactNode, type RefObject } from "react";
import { flattenMessages, useMessages } from "@/api/queries";
import type { MessageView, ThreadView } from "@/api/views";
import { Loading } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { chatBlocks, interleave, type ChatBlock } from "@/lib/chat";
import { featured, isWorking, liveLine } from "@/lib/live";
import { readScroll, restoredTop, scrollMarkOf, scrollParent, writeScroll } from "@/lib/persist";
import { markTone } from "@/lib/present";
import { clock, cn } from "@/lib/utils";
import { MessageImages } from "./attachments";
import { Dot, LiveDot } from "./bits";
import { ArtifactCard, PlanCard, ResumedNote, TodoCard } from "./chatcards";

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
  /** Project id, for links from cards (deliverables). */
  pid?: string;
}

export function Chat({ cid, threads = [], onOpenThread, speakerColor = "#C8622A", empty, after, compact, filter, inserts = [], pid = "" }: ChatProps) {
  const query = useMessages(cid);
  const all = flattenMessages(query.data);
  const messages = filter ? all.filter(filter) : all;
  const end = useRef<HTMLDivElement>(null);
  const count = messages.length + inserts.length;
  useChatScroll(cid, end, count, !query.isLoading);
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
          <Block key={it.block.message.id} block={it.block} threads={threads} onOpenThread={onOpenThread} speakerColor={speakerColor} compact={compact} pid={pid} />
        ) : (
          <div key={it.key}>{it.node}</div>
        ),
      )}
      {after}
      <div ref={end} />
    </div>
  );
}

/**
 * Scroll: reopening a conversation goes back to where it was read (kept per conversation in
 * localStorage); new messages scroll into view only while the reader follows the bottom.
 */
function useChatScroll(cid: string | undefined, end: RefObject<HTMLDivElement>, count: number, ready: boolean) {
  const restored = useRef<string | null>(null);
  const following = useRef(true);
  useLayoutEffect(() => {
    const box = scrollParent(end.current);
    if (!cid || !ready || !box || restored.current === cid) return;
    restored.current = cid;
    const mark = readScroll(cid);
    following.current = !mark;
    box.scrollTop = restoredTop(mark, box);
  }, [cid, ready, count, end]);
  useEffect(() => {
    if (restored.current === cid && following.current) end.current?.scrollIntoView({ block: "end" });
  }, [count, cid, end]);
  useEffect(() => {
    const box = scrollParent(end.current);
    if (!cid || !box) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const onScroll = () => {
      const mark = scrollMarkOf(box);
      following.current = mark.fromBottom === 0;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => writeScroll(cid, mark), 200);
    };
    box.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      box.removeEventListener("scroll", onScroll);
      if (timer) clearTimeout(timer);
    };
  }, [cid, ready, end]);
}

function Block({ block, threads, onOpenThread, speakerColor, compact, pid }: { block: ChatBlock; threads: ThreadView[]; onOpenThread?: (no: number) => void; speakerColor: string; compact?: boolean; pid: string }) {
  const m = block.message;
  if (block.kind === "card") return <ChatCard message={m} pid={pid} />;
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

function ChatCard({ message, pid }: { message: MessageView; pid: string }) {
  switch (message.kind) {
    case "todos":
      return <TodoCard message={message} />;
    case "plan":
      return <PlanCard message={message} />;
    case "artifact":
      return <ArtifactCard message={message} pid={pid} />;
    case "resumed":
      return <ResumedNote message={message} />;
  }
  return null;
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
