// Deliverable viewer: a design or any file published with publish_file, shown full size.
// Hosted files come from short-lived signed URLs: HTML runs in a sandboxed iframe (no
// allow-same-origin unless files have their own origin), Markdown is rendered safely,
// images inline. A version switcher picks older versions; comments are stored and sent to
// the employee who published it (or the thread's lead); "通过" tells the lead to go ahead.
import { ArrowLeft, ExternalLink } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { conversations, deliverables as deliverableActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverableComments, useDeliverableFiles, useDeliverables, useThreads } from "@/api/queries";
import type { DeliverableFiles, DeliverableView, ThreadView } from "@/api/views";
import { Composer } from "@/components/app/bits";
import { EmptyState, Loading } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { fileUrl, formatBytes, frameSandbox, isHosted, markdownUrl, renderLabel } from "@/lib/artifacts";
import { deliverableKind } from "@/lib/present";
import { clock, timeAgo } from "@/lib/utils";
import { embeddableUrl, safeHref } from "@/lib/safeurl";

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
  const hosted = isHosted(item);
  const [version, setVersion] = useState(0);
  useEffect(() => setVersion(0), [item.id, item.detail.version]);
  const files = useDeliverableFiles(item.id, version, hosted);
  const author = item.detail.author ?? item.published_by;
  const shown = files.data?.version ?? item.detail.version;
  const label = deliverableKind(item.kind).label;
  const approve = useAction(() => conversations.send(thread!.id, `${label}「${item.title}」${shown ? `第 ${shown} 版` : ""}通过，按这个做。`), {
    invalidate: [keys.messages(thread?.id ?? "")],
    success: "已告诉组长按这个做",
  });
  const sub = [thread ? `#${thread.no} ${thread.title}` : null, author ? `${author} 做的` : null, timeAgo(item.updated_at)].filter(Boolean).join(" · ");
  const external = hosted ? files.data?.entry_url : safeHref(item.url);
  return (
    <div className="flex min-h-full flex-col bg-[#ECEAE5] dark:bg-background">
      <header className="flex min-h-[52px] flex-wrap items-center gap-x-4 gap-y-2.5 border-b bg-panel px-4 py-2">
        <Link to={`/projects/${pid}/deliverables`} aria-label="返回交付物" className="flex size-9 items-center justify-center rounded-lg hover:bg-raised">
          <ArrowLeft className="size-[18px]" strokeWidth={1.8} />
        </Link>
        <span className="flex min-w-0 flex-[1_1_260px] flex-col">
          <span className="truncate font-semibold">
            {item.title} · {label}
          </span>
          <span className="truncate text-xs text-muted-foreground">{sub}</span>
        </span>
        <span className="flex flex-wrap items-center gap-1.5">
          {hosted && files.data && <VersionPicker files={files.data} onPick={setVersion} />}
          <Button asChild variant="outline" size="sm" disabled={!external}>
            <a href={external} target="_blank" rel="noreferrer noopener">
              新窗口打开 <ExternalLink />
            </a>
          </Button>
          <Button size="sm" disabled={!thread || approve.isPending} onClick={() => approve.mutate()}>
            通过，按这个做
          </Button>
        </span>
      </header>
      <div className="flex flex-1 flex-wrap">
        <section aria-label="内容" className="flex min-w-0 flex-[999_1_640px] p-4 sm:px-12 sm:py-10">
          {hosted ? <HostedContent title={item.title} files={files.data} loading={files.isLoading} failed={files.isError} /> : <LinkFrame item={item} />}
        </section>
        <Comments item={item} thread={thread} author={author} version={shown} />
      </div>
    </div>
  );
}

function VersionPicker({ files, onPick }: { files: DeliverableFiles; onPick: (v: number) => void }) {
  const latest = files.versions[0]?.version ?? files.version;
  return (
    <Select value={String(files.version)} onValueChange={(v) => onPick(Number(v) === latest ? 0 : Number(v))}>
      <SelectTrigger className="h-8 w-auto gap-2 text-[13px]" aria-label="版本">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {files.versions.map((v) => (
          <SelectItem key={v.version} value={String(v.version)}>
            第 {v.version} 版{v.version === latest ? "（最新）" : ""} · {v.published_by} · {timeAgo(v.created_at)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

const frameClass = "min-h-[560px] w-full flex-1 rounded-md bg-white shadow-[0_1px_3px_rgba(0,0,0,0.12)]";

function LinkFrame({ item }: { item: DeliverableView }) {
  const src = embeddableUrl(item.url, window.location.origin);
  if (!src) return <EmptyState title="这个链接不能嵌入预览" description="用右上角“新窗口打开”查看。" />;
  return <iframe title={item.title} src={src} sandbox="allow-scripts allow-same-origin allow-popups" className={frameClass} />;
}

function HostedContent({ title, files, loading, failed }: { title: string; files?: DeliverableFiles; loading: boolean; failed: boolean }) {
  if (loading) return <Loading className="flex-1" />;
  if (failed || !files) return <EmptyState title="文件加载失败" description="刷新页面重试。" />;
  switch (files.render) {
    case "html":
      return <iframe key={files.entry_url} title={title} src={files.entry_url} sandbox={frameSandbox(files.isolated)} referrerPolicy="no-referrer" className={frameClass} />;
    case "markdown":
      return <MarkdownFile files={files} />;
    case "image":
      return (
        <div className="flex flex-1 items-start justify-center overflow-auto">
          <img src={files.entry_url} alt={title} referrerPolicy="no-referrer" className="max-w-full rounded-md bg-white shadow-[0_1px_3px_rgba(0,0,0,0.12)]" />
        </div>
      );
    default:
      return <FileList files={files} />;
  }
}

function useText(url: string): { text?: string; error: boolean } {
  const [state, setState] = useState<{ url: string; text?: string; error: boolean }>({ url, error: false });
  useEffect(() => {
    let live = true;
    fetch(url, { credentials: "omit", referrerPolicy: "no-referrer" })
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      .then((text) => live && setState({ url, text, error: false }))
      .catch(() => live && setState({ url, error: true }));
    return () => {
      live = false;
    };
  }, [url]);
  return state.url === url ? state : { error: false };
}

function MarkdownFile({ files }: { files: DeliverableFiles }) {
  const { text, error } = useText(files.entry_url);
  if (error) return <EmptyState title="文档加载失败" description="链接可能已过期，刷新页面重试。" />;
  if (text === undefined) return <Loading className="flex-1" />;
  return (
    <article className="mx-auto w-full max-w-[820px] rounded-md bg-panel px-6 py-8 shadow-[0_1px_3px_rgba(0,0,0,0.12)] sm:px-10">
      <Markdown urlTransform={markdownUrl(files.base_url)}>{text}</Markdown>
    </article>
  );
}

function FileList({ files }: { files: DeliverableFiles }) {
  return (
    <div className="flex w-full flex-col gap-2 rounded-md bg-panel p-5">
      <span className="text-sm font-medium">{renderLabel(files.render)} · {files.files.length} 个文件</span>
      {files.files.map((f) => (
        <a key={f.path} href={fileUrl(files.base_url, f.path)} target="_blank" rel="noreferrer noopener" className="flex justify-between gap-3 text-[13px] hover:underline">
          <span className="truncate font-mono">{f.path}</span>
          <span className="shrink-0 text-muted-foreground">{formatBytes(f.size)}</span>
        </a>
      ))}
    </div>
  );
}

function Comments({ item, thread, author, version }: { item: DeliverableView; thread?: ThreadView; author: string; version?: number }) {
  const comments = useDeliverableComments(item.id);
  const add = useAction((text: string) => deliverableActions.comment(item.id, text, version), {
    invalidate: [keys.deliverableComments(item.id)],
  });
  const items = comments.data?.items ?? [];
  return (
    <aside aria-label="批注" className="flex min-w-0 max-w-full flex-[1_1_300px] flex-col gap-3.5 border-l bg-panel px-[18px] py-4 sm:max-w-[360px]">
      <span className="flex items-center justify-between">
        <span className="font-semibold">批注 · {items.length}</span>
        <span className="text-xs text-muted-foreground">批注会转给 {author || "发布的员工"}</span>
      </span>
      {items.length === 0 && <p className="text-[13px] leading-6 text-muted-foreground">看完写下意见，会直接发给 {author || "发布的员工"}，回复在讨论串里。</p>}
      {items.map((c, i) => (
        <div key={c.id} className="flex flex-col gap-1.5 rounded-[10px] border bg-background px-3 py-2.5">
          <span className="flex items-center gap-2 text-xs text-muted-foreground">
            <span className="flex size-[18px] items-center justify-center rounded-full bg-[#C8622A] text-[10px] font-bold text-white">{i + 1}</span>
            你 · {clock(c.created_at)}
            {c.version ? ` · 第 ${c.version} 版` : ""}
            {c.sent_to ? ` · 已发给 ${c.sent_to}` : " · 未转发"}
          </span>
          <span className="whitespace-pre-wrap text-[13px] leading-[21px]">{c.text}</span>
        </div>
      ))}
      <div className="mt-auto flex flex-col gap-1.5">
        <Composer variant="inline" label="写批注" placeholder="写批注，发给做这个的员工" busy={add.isPending} onSend={(t) => add.mutateAsync(t)} id="note" />
        {!thread && <p className="text-xs text-muted-foreground">这个交付物不属于任何讨论串，批注只会保存，不会转发。</p>}
      </div>
    </aside>
  );
}
