// One thread, chat only (like Claude Code's thread panel): a one-line header
// "讨论串 > ● #12 标题" with ✓ (archive), ⋯ (任务 / 交付物 / 成员 / 全屏 / 暂停 / 归档 / 删除),
// expand and close; then the conversation between you and the lead — question cards
// (answered ones collapsed to your choice), the lead's checklist, plans, published files
// and the acceptance card sit where they happened — and the message box.
// 任务 / 交付物 / 成员 open in a dialog over the chat (ThreadViews.tsx).
import { ChevronRight, Maximize2, Minimize2, X } from "lucide-react";
import type { ReactNode } from "react";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useQuestions } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { IconButton, IconLink } from "@/components/common";
import { threadTalk } from "@/lib/chat";
import { isWorking, threadActive } from "@/lib/live";
import { chatQuestions } from "@/lib/ask";
import { leadOf, threadLook } from "@/lib/present";
import { cn } from "@/lib/utils";
import { useAttachments } from "./attachments";
import { Composer, Dot, Spin } from "./bits";
import { ReviewCard, WaitingCard } from "./cards";
import { Chat } from "./Chat";
import { ResolveButton, THREAD_VIEWS, ThreadMenu, type ThreadViewId } from "./ThreadMenu";
import { ThreadViewDialog } from "./ThreadViews";

export type { ThreadViewId } from "./ThreadMenu";

/** The view a URL segment names (/threads/:no/:tab), or none (just the chat). */
export function detailViewOf(value: string | undefined): ThreadViewId | null {
  return THREAD_VIEWS.find(([id]) => id === value)?.[0] ?? null;
}

interface DetailProps {
  pid: string;
  thread: ThreadView;
  /** The view open over the chat. */
  view: ThreadViewId | null;
  onView: (view: ThreadViewId | null) => void;
  /** 讨论串 in the breadcrumb: back to the list. */
  onBack: () => void;
  onClose: () => void;
  /** Expand (panel → full page) or collapse (full page → panel); omitted where it would do nothing. */
  expand?: { to: string; label: string; collapse?: boolean };
  /** Where the full page is, for the ⋯ menu (panel only). */
  fullHref?: string;
  /** `page`: the full-page view, chat centered at reading width. */
  layout?: "panel" | "page";
}

export function ThreadDetail({ pid, thread, view, onView, onBack, onClose, expand, fullHref, layout = "panel" }: DetailProps) {
  const page = layout === "page";
  return (
    <>
      <DetailHeader thread={thread} onBack={onBack} onClose={onClose} onView={onView} expand={expand} fullHref={fullHref} />
      <div className="scroll-thin flex-1 overflow-y-auto">
        <div className={cn("flex flex-col px-5 pb-6 pt-5", page && "mx-auto w-full max-w-[760px] sm:px-6 sm:pt-8")}>
          <ThreadChat pid={pid} thread={thread} compact={!page} />
        </div>
      </div>
      <LeadComposer pid={pid} thread={thread} className={cn(page ? "mx-auto w-full max-w-[760px] px-3 pb-3 sm:px-6 sm:pb-5" : "px-4 pb-4")} />
      <ThreadViewDialog pid={pid} thread={thread} view={view} onView={onView} />
    </>
  );
}

interface HeaderProps {
  thread: ThreadView;
  onBack: () => void;
  onClose: () => void;
  onView: (view: ThreadViewId) => void;
  expand?: DetailProps["expand"];
  fullHref?: string;
}

function DetailHeader({ thread, onBack, onClose, onView, expand, fullHref }: HeaderProps) {
  const look = threadLook(thread);
  return (
    <header className="flex h-14 shrink-0 items-center gap-0.5 border-b border-[#EFEDE8] px-2.5">
      <nav aria-label="位置" className="flex min-w-0 flex-1 items-center gap-0.5 text-[13px]">
        <button type="button" onClick={onBack} className="h-8 shrink-0 rounded-lg px-2 text-muted-foreground hover:bg-raised hover:text-foreground">
          讨论串
        </button>
        <ChevronRight className="size-3.5 shrink-0 text-[#B7B3AA]" strokeWidth={2} />
        <span className="flex min-w-0 items-center gap-1.5 px-1" title={`#${thread.no} ${thread.title} · ${look.label}`}>
          {look.spin ? <Spin color={look.tone} size={11} label={look.label} /> : <Dot color={look.tone} size={7} className="mx-0.5" />}
          {!look.spin && <span className="sr-only">{look.label}</span>}
          <span aria-current="page" className="truncate font-medium">
            #{thread.no} {thread.title}
          </span>
        </span>
      </nav>
      <ResolveButton thread={thread} />
      <ThreadMenu thread={thread} fullHref={fullHref} onView={onView} onDeleted={onBack} />
      {expand && (
        <IconLink label={expand.label} to={expand.to}>
          {expand.collapse ? <Minimize2 strokeWidth={1.8} /> : <Maximize2 strokeWidth={1.8} />}
        </IconLink>
      )}
      <IconButton label="关闭" onClick={onClose}>
        <X strokeWidth={1.8} />
      </IconButton>
    </header>
  );
}

/** You and the lead: the lead's messages as Markdown, yours as bubbles, open cards where they were asked. */
function ThreadChat({ pid, thread, compact }: { pid: string; thread: ThreadView; compact: boolean }) {
  const questions = useQuestions(thread.id);
  const lead = leadOf(thread)?.name;
  const inserts = chatQuestions(questions.data).map((q) => ({
    key: q.id,
    at: q.created_at,
    node: <WaitingCard cid={thread.id} pid={pid} question={q} variant="soft" leadName={lead} />,
  }));
  const after = (
    <>
      {thread.status === "review" && <ReviewCard thread={thread} variant="soft" />}
      <Typing thread={thread} />
    </>
  );
  const empty = <p className="py-10 text-center text-[13px] leading-6 text-muted-foreground">还没有对话。组长汇报会出现在这里，你也可以直接给组长发消息。</p>;
  return <Chat cid={thread.id} pid={pid} filter={threadTalk} inserts={inserts} after={after} empty={empty} compact={compact} speakerColor="#2F6FDB" />;
}

/** One quiet line at the bottom while the thread is going. */
function Typing({ thread }: { thread: ThreadView }) {
  const line = typingLine(thread);
  if (!line) return null;
  return (
    <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
      {line.spin ? <Spin color="#8A877F" size={11} /> : <Dot color="#B7B3AA" size={6} className="mx-0.5" />}
      {line.text}
    </p>
  );
}

export function typingLine(thread: Pick<ThreadView, "status" | "members">): { text: string; spin: boolean } | null {
  if (!threadActive(thread)) return null;
  if (thread.status === "paused") return { text: "讨论串已暂停", spin: false };
  if (thread.status === "queued") return { text: "排队中，等节点空出来", spin: true };
  const lead = leadOf(thread);
  if (lead && isWorking(lead)) return { text: "组长正在回复…", spin: true };
  if (thread.status === "running") return { text: "讨论串进行中", spin: true };
  return null;
}

function LeadComposer({ pid, thread, className }: { pid: string; thread: ThreadView; className?: string }) {
  const attachments = useAttachments(pid);
  const lead = leadOf(thread);
  const send = useAction((v: { text: string; images: string[] }) => conversations.send(thread.id, v.text, false, v.images), {
    invalidate: [keys.messages(thread.id)],
  });
  let body: ReactNode;
  if (["done", "failed", "archived"].includes(thread.status))
    body = <p className="rounded-xl bg-background px-3 py-2.5 text-center text-xs text-muted-foreground">讨论串已{thread.status === "archived" ? "归档" : "结束"}，不能再给组长发消息</p>;
  else
    body = (
      <Composer
        label="发给组长"
        placeholder={`回复组长 ${lead?.name ?? ""}，可粘贴或拖入图片`}
        busy={send.isPending}
        attachments={attachments}
        onSend={(text, images) => send.mutateAsync({ text, images })}
        id={`lead-${thread.id}`}
        draftKey={`draft:${thread.id}`}
      />
    );
  return <div className={cn("shrink-0", className)}>{body}</div>;
}
