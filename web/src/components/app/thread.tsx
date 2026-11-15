// Thread pieces shared by the Main page's right panel, the Thread page and
// the phone view: the grouped list, the compact checklist, cards, members.
import { ArrowLeft, Maximize2 } from "lucide-react";
import { Link } from "react-router-dom";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useQuestions } from "@/api/queries";
import type { MainTaskView, QuestionView, ThreadView } from "@/api/views";
import { Loading } from "@/components/common";
import { groupThreads, markTone, leadOf, mainCount, memberDoing, memberTone, openForUser, threadLine, threadLook } from "@/lib/present";
import { cn } from "@/lib/utils";
import { CheckMark, Composer, Dot, StatusPill } from "./bits";
import { ReviewCard, WaitingCard, type CardVariant } from "./cards";

/** The round mark before a thread in lists: outlined in the state colour, filled with ✓ when done. */
export function ThreadMark({ thread }: { thread: ThreadView }) {
  const tone = markTone(thread);
  const done = thread.status === "done";
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

export function ThreadList({ threads, onOpen, waitingByThread }: { threads: ThreadView[]; onOpen: (no: number) => void; waitingByThread?: Map<number, QuestionView> }) {
  const groups = groupThreads(threads);
  if (groups.length === 0) return <p className="text-[13px] leading-6 text-muted-foreground">还没有讨论串。和项目统筹说需求，它会开讨论串去做。</p>;
  return (
    <div className="flex flex-col gap-5">
      {groups.map((g) => (
        <section key={g.id} className="flex flex-col gap-0.5">
          <span className="pb-1.5 text-xs text-muted-foreground">{g.title}</span>
          {g.items.map((t) => (
            <button key={`${g.id}-${t.id}`} type="button" onClick={() => onOpen(t.no)} className="-mx-2 flex gap-2.5 rounded-lg p-2 text-left hover:bg-background">
              <ThreadMark thread={t} />
              <span className="flex min-w-0 flex-col gap-[3px]">
                <span className="text-sm font-medium">
                  #{t.no} {t.title}
                </span>
                <span className="line-clamp-2 text-xs leading-[18px] text-muted-foreground">{threadLine(t, waitingByThread?.get(t.no))}</span>
              </span>
            </button>
          ))}
        </section>
      ))}
    </div>
  );
}

/** Main tasks as a compact checklist (one row each). */
export function Checklist({ tasks, size = "sm" }: { tasks: MainTaskView[]; size?: "sm" | "md" }) {
  const sorted = tasks.slice().sort((a, b) => a.pos - b.pos);
  if (sorted.length === 0) return <p className="py-1 text-[13px] text-muted-foreground">组长还在列主要任务…</p>;
  return (
    <div className="flex flex-col">
      {sorted.map((m) => (
        <span key={m.id} className={cn("flex items-center gap-2.5", size === "sm" ? "min-h-7 text-[13px]" : "min-h-[34px] text-sm")}>
          <CheckMark status={m.status} size={size === "sm" ? 15 : 16} />
          <span className={cn("min-w-0 flex-1", m.status === "done" && "text-[#8A877F] line-through")}>{m.title}</span>
          {size === "md" && <span className="text-xs text-[#8A877F]">{mainCount(m)}</span>}
        </span>
      ))}
    </div>
  );
}

/** Open cards of a thread: questions/approvals for the user, then PR acceptance. */
export function ThreadCards({ thread, questions, variant }: { thread: ThreadView; questions: QuestionView[]; variant: CardVariant }) {
  const lead = leadOf(thread)?.name;
  return (
    <>
      {questions.map((q) => (
        <WaitingCard key={q.id} cid={thread.id} pid={thread.project_id} question={q} variant={variant} leadName={lead} />
      ))}
      {thread.status === "review" && <ReviewCard thread={thread} variant={variant} />}
    </>
  );
}

export function MemberLines({ thread }: { thread: ThreadView }) {
  const members = thread.members.filter((m) => m.tier !== "lead");
  return (
    <section className="flex flex-col gap-1.5">
      <span className="text-xs text-muted-foreground">成员</span>
      {members.length === 0 && <span className="text-[13px] text-muted-foreground">还没有成员</span>}
      {members.map((m) => (
        <span key={m.id} className="flex items-center gap-2 text-[13px]">
          <Dot color={memberTone(m)} />
          <span className="font-mono">{m.name}</span>
          <span className="min-w-0 flex-1 truncate text-muted-foreground">{memberDoing(m)}</span>
        </span>
      ))}
    </section>
  );
}

/** Right-panel detail of one thread (Main board, “← 所有讨论串”). */
export function ThreadPanel({ thread, onBack, fullHref }: { thread: ThreadView; onBack: () => void; fullHref: string }) {
  const questions = useQuestions(thread.id);
  const open = openForUser(questions.data);
  const look = threadLook(thread);
  const lead = leadOf(thread);
  const send = useAction((text: string) => conversations.send(thread.id, text), { invalidate: [keys.messages(thread.id)], success: "已发给组长" });
  return (
    <>
      <div className="flex flex-col gap-2 border-b border-[#EFEDE8] px-3.5 pb-3 pt-2.5">
        <span className="flex items-center justify-between">
          <button type="button" onClick={onBack} className="flex h-8 items-center gap-1 rounded-lg px-2 text-[13px] text-[#4A4843] hover:bg-background">
            <ArrowLeft className="size-3.5" strokeWidth={2} />
            所有讨论串
          </button>
          <Link to={fullHref} aria-label="全屏打开讨论串" className="flex size-8 items-center justify-center rounded-lg text-[#4A4843] hover:bg-background">
            <Maximize2 className="size-[15px]" strokeWidth={1.8} />
          </Link>
        </span>
        <span className="flex items-center justify-between gap-2.5 px-1.5">
          <span className="text-base font-semibold">
            #{thread.no} {thread.title}
          </span>
          <StatusPill tone={look.tone} label={look.label} />
        </span>
        <span className="px-1.5 font-mono text-xs text-muted-foreground">
          {thread.branch || "分支待创建"} · 组长 {lead?.name ?? "—"}
        </span>
      </div>
      <div className="scroll-thin flex flex-1 flex-col gap-4 overflow-y-auto px-5 py-3.5">
        <div className="rounded-xl border border-[#E2DFD8] px-3 py-2">
          <Checklist tasks={thread.main_tasks} />
        </div>
        {questions.isLoading ? <Loading className="py-2" /> : <ThreadCards thread={thread} questions={open} variant="soft" />}
        <MemberLines thread={thread} />
        <section className="flex flex-col gap-1.5">
          <span className="text-xs text-muted-foreground">组长最近说</span>
          <p className="text-[13px] leading-[22px]">{thread.lead_last?.text || "还没有汇报。"}</p>
        </section>
      </div>
      <Composer variant="inline" label="和组长说话" placeholder="直接和组长说话" className="mx-4 mb-4" busy={send.isPending} onSend={(t) => send.mutateAsync(t)} id={`lead-${thread.id}`} />
    </>
  );
}
