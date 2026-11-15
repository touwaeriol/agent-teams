// DesignViewer board: a design deliverable shown full size, with notes that go
// straight to the employee who made it (POST …/members/{name}/message) and an
// approve button that tells the thread's lead to go ahead.
import { ArrowLeft, ExternalLink } from "lucide-react";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { conversations, threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverables, useThreads } from "@/api/queries";
import type { DeliverableView, ThreadView } from "@/api/views";
import { Composer } from "@/components/app/bits";
import { EmptyState, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { clock, timeAgo } from "@/lib/utils";

interface Note {
  text: string;
  at: string;
}

export function DesignViewerPage() {
  const { pid = "", id = "" } = useParams();
  const list = useDeliverables(pid, "", "");
  const threads = useThreads(pid);
  const item = list.data?.items.find((d) => d.id === id);
  if (list.isLoading) return <Loading className="h-full" />;
  if (!item) return <EmptyState title="找不到这个交付物" action={<Button asChild><Link to={`/projects/${pid}/deliverables`}>回到交付物</Link></Button>} />;
  const thread = threads.data?.find((t) => t.no === item.thread_no);
  return <Viewer pid={pid} item={item} thread={thread} />;
}

function Viewer({ pid, item, thread }: { pid: string; item: DeliverableView; thread?: ThreadView }) {
  const [notes, setNotes] = useState<Note[]>([]);
  const author = item.detail.author ?? item.published_by;
  const approve = useAction(() => conversations.send(thread!.id, `设计稿「${item.title}」通过，按这个做。`), {
    invalidate: [keys.messages(thread?.id ?? "")],
    success: "已告诉组长按这个做",
  });
  const note = useAction((text: string) => threadActions.messageMember(thread!.id, author, `对设计稿「${item.title}」的批注：${text}`), {
    onSuccess: (_d, text) => setNotes((n) => [...n, { text, at: new Date().toISOString() }]),
  });
  const sub = [thread ? `#${thread.no} ${thread.title}` : null, author ? `${author} 做的` : null, item.detail.version ? `版本 ${item.detail.version}` : null, timeAgo(item.updated_at)].filter(Boolean).join(" · ");
  return (
    <div className="flex min-h-full flex-col bg-[#ECEAE5]">
      <header className="flex min-h-[52px] flex-wrap items-center gap-x-4 gap-y-2.5 border-b bg-panel px-4 py-2">
        <Link to={`/projects/${pid}/deliverables`} aria-label="返回交付物" className="flex size-9 items-center justify-center rounded-lg hover:bg-raised">
          <ArrowLeft className="size-[18px]" strokeWidth={1.8} />
        </Link>
        <span className="flex min-w-0 flex-[1_1_260px] flex-col">
          <span className="truncate font-semibold">{item.title} · 设计稿</span>
          <span className="truncate text-xs text-muted-foreground">{sub}</span>
        </span>
        <span className="flex flex-wrap items-center gap-1.5">
          <Button asChild variant="outline" size="sm">
            <a href={item.url} target="_blank" rel="noreferrer">
              打开原稿 <ExternalLink />
            </a>
          </Button>
          <Button size="sm" disabled={!thread || approve.isPending} onClick={() => approve.mutate()}>
            通过，按这个做
          </Button>
        </span>
      </header>
      <div className="flex flex-1 flex-wrap">
        <section aria-label="画布" className="flex min-w-0 flex-[999_1_640px] p-6 sm:px-12 sm:py-10">
          <iframe title={item.title} src={item.url} sandbox="allow-scripts allow-same-origin allow-popups" className="min-h-[560px] w-full flex-1 rounded-md bg-white shadow-[0_1px_3px_rgba(0,0,0,0.12)]" />
        </section>
        <aside aria-label="批注" className="flex min-w-0 max-w-[360px] flex-[1_1_300px] flex-col gap-3.5 border-l bg-panel px-[18px] py-4">
          <span className="flex items-center justify-between">
            <span className="font-semibold">批注 · {notes.length}</span>
            <span className="text-xs text-muted-foreground">批注会转给做设计的成员</span>
          </span>
          {notes.length === 0 && <p className="text-[13px] leading-6 text-muted-foreground">看完写下意见，会直接发给 {author || "做设计的成员"}，回复在讨论串里。</p>}
          {notes.map((n, i) => (
            <div key={i} className="flex flex-col gap-1.5 rounded-[10px] border border-[#EFEDE8] bg-background px-3 py-2.5">
              <span className="flex items-center gap-2 text-xs text-muted-foreground">
                <span className="flex size-[18px] items-center justify-center rounded-full bg-[#C8622A] text-[10px] font-bold text-white">{i + 1}</span>
                你 · {clock(n.at)}
              </span>
              <span className="text-[13px] leading-[21px]">{n.text}</span>
            </div>
          ))}
          <div className="mt-auto">
            {thread ? (
              <Composer variant="inline" label="写批注" placeholder="写批注，发给做设计的成员" busy={note.isPending} onSend={(t) => note.mutateAsync(t)} id="note" />
            ) : (
              <p className="text-xs text-muted-foreground">这个设计稿不属于任何讨论串，无法转发批注。</p>
            )}
          </div>
        </aside>
      </div>
    </div>
  );
}
