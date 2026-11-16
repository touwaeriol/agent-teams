// The right panel's thread list: grouped rows with live lines, row menus and the archived section.
import { ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";
import type { QuestionView, ThreadView } from "@/api/views";
import { isWorking, liveLine, threadActive } from "@/lib/live";
import { groupThreads, markTone, threadLine, threadLook } from "@/lib/present";
import { LiveDot, Spin } from "./bits";
import { ThreadMenu } from "./ThreadMenu";

/** The round mark before a thread in lists: a spinner while in progress, outlined in the state colour otherwise, filled with ✓ when done. */
export function ThreadMark({ thread }: { thread: ThreadView }) {
  const tone = markTone(thread);
  const done = thread.status === "done";
  if (threadLook(thread).spin) return <Spin color={tone} size={16} className="mt-0.5" label="进行中" />;
  return (
    <span
      aria-hidden
      className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border-[1.5px] text-[10px] font-bold text-white"
      style={{ borderColor: tone, background: done ? tone : "#FFFFFF" }}
    >
      {done ? "✓" : ""}
    </span>
  );
}

/** The right panel's list: 等你处理 / 进行中 / 已完成, then a collapsed 已归档 section. Rows have a ⋯ menu. */
export function ThreadList({ threads, onOpen, waitingByThread, menus = false }: { threads: ThreadView[]; onOpen: (no: number) => void; waitingByThread?: Map<number, QuestionView>; menus?: boolean }) {
  const groups = groupThreads(threads);
  const archived = threads.filter((t) => t.status === "archived").sort((a, b) => b.no - a.no);
  if (groups.length === 0 && archived.length === 0) return <p className="text-[13px] leading-6 text-muted-foreground">还没有讨论串。和项目统筹说需求，它会开讨论串去做。</p>;
  return (
    <div className="flex flex-col gap-5">
      {groups.map((g) => (
        <section key={g.id} className="flex flex-col gap-0.5">
          <span className="pb-1.5 text-xs text-muted-foreground">{g.title}</span>
          {g.items.map((t) => (
            <ThreadRow key={`${g.id}-${t.id}`} thread={t} onOpen={onOpen} line={threadRowLine(t, waitingByThread?.get(t.no))} live={!waitingByThread?.get(t.no) && t.counts.waiting === 0} menu={menus} />
          ))}
        </section>
      ))}
      {archived.length > 0 && <ArchivedSection threads={archived} onOpen={onOpen} menus={menus} />}
    </div>
  );
}

/** A live thread's line prefers what is happening right now; others keep the summary line. */
function threadRowLine(t: ThreadView, open?: QuestionView): string {
  if (open) return threadLine(t, open);
  return liveLine(t) || threadLine(t, open);
}

function ThreadRow({ thread, onOpen, line, menu, live = false }: { thread: ThreadView; onOpen: (no: number) => void; line: string; menu: boolean; live?: boolean }) {
  const working = live && threadActive(thread) && thread.members.some(isWorking);
  return (
    <div className="group -mx-2 flex items-start gap-1 rounded-lg hover:bg-background">
      <button type="button" onClick={() => onOpen(thread.no)} className="flex min-w-0 flex-1 gap-2.5 p-2 text-left">
        <ThreadMark thread={thread} />
        <span className="flex min-w-0 flex-col gap-[3px]">
          <span className="text-sm font-medium">
            #{thread.no} {thread.title}
          </span>
          <span className="flex items-start gap-1.5 text-xs leading-[18px] text-muted-foreground">
            {working && <LiveDot color="#2F6FDB" on size={6} className="mt-[6px]" />}
            <span className="line-clamp-2">{line}</span>
          </span>
        </span>
      </button>
      {menu && <ThreadMenu thread={thread} className="mt-1.5 shrink-0 opacity-0 focus-visible:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100 max-lg:opacity-100" />}
    </div>
  );
}

function ArchivedSection({ threads, onOpen, menus }: { threads: ThreadView[]; onOpen: (no: number) => void; menus: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <section className="flex flex-col gap-0.5">
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex items-center gap-1 self-start pb-1.5 text-xs text-muted-foreground hover:text-foreground">
        {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        已归档 {threads.length}
      </button>
      {open && threads.map((t) => <ThreadRow key={t.id} thread={t} onOpen={onOpen} line={t.summary || t.brief} menu={menus} />)}
    </section>
  );
}
