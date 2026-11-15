// Deliverables board: everything the team published, by kind, plus the
// “新做一个” tiles that hand a templated request to the coordinator.
import { MoreHorizontal, Search } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { deliverables as deliverableActions, projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverables, useProject } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import { Field, FormDialog, Loading } from "@/components/common";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { NavButton } from "@/components/shell/Shell";
import { deliverableKind } from "@/lib/present";
import { cn, timeAgo } from "@/lib/utils";
import { useProjectId } from "./Main";

const TABS: [string, string][] = [
  ["", "全部"],
  ["design", "设计稿"],
  ["doc", "文档"],
  ["slides", "幻灯片"],
  ["pr", "PR"],
  ["data", "数据"],
];

const MAKE = ["design", "doc", "slides", "dashboard", "board"];

/** Where a card goes: designs open in the viewer, everything else at its URL. */
export function deliverableHref(pid: string, d: Pick<DeliverableView, "id" | "kind" | "url">): { to?: string; href?: string } {
  if (d.kind === "design") return { to: `/projects/${pid}/deliverables/${d.id}` };
  return { href: d.url };
}

export function DeliverablesPage() {
  const pid = useProjectId();
  const project = useProject(pid);
  const [kind, setKind] = useState("");
  const [q, setQ] = useState("");
  const [making, setMaking] = useState<string | null>(null);
  const list = useDeliverables(pid, kind, q);
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-9">
      <div className="mx-auto flex max-w-[1000px] flex-col gap-6">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1.5">
            <Link to={`/projects/${pid}/overview`} className="text-xs text-muted-foreground hover:text-foreground">
              {project.data?.name ?? "项目"}
            </Link>
            <h1 className="text-[26px] font-semibold">交付物</h1>
          </div>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div role="tablist" aria-label="类型" className="flex flex-wrap gap-1">
            {TABS.map(([id, label]) => (
              <button
                key={id || "all"}
                type="button"
                role="tab"
                aria-selected={kind === id}
                onClick={() => setKind(id)}
                className={cn("h-8 rounded-lg px-3 text-[13px]", kind === id ? "bg-[#ECEAE4] font-semibold" : "hover:bg-raised")}
              >
                {label}
              </button>
            ))}
          </div>
          <label className="flex h-[34px] items-center gap-2 rounded-lg border border-input bg-white px-2.5 text-muted-foreground">
            <Search className="size-3.5" strokeWidth={1.8} />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索交付物" className="w-40 bg-transparent text-[13px] text-foreground outline-none" />
          </label>
        </div>
        <section className="flex flex-col gap-2.5">
          <span className="text-xs text-muted-foreground">让团队做一个新的（交给项目统筹，它会开讨论串去做）</span>
          <div className="flex flex-wrap gap-3">
            {MAKE.map((k) => {
              const look = deliverableKind(k);
              return (
                <button key={k} type="button" onClick={() => setMaking(k)} className="flex w-28 flex-col gap-2 text-left">
                  <span className="flex h-[76px] items-center justify-center rounded-xl text-[22px] font-bold" style={{ background: look.bg, color: look.fg }}>
                    {look.glyph}
                  </span>
                  <span className="text-[13px]">{look.label}</span>
                </button>
              );
            })}
          </div>
        </section>
        {list.isLoading && <Loading />}
        {list.data && list.data.items.length === 0 && <p className="rounded-xl border border-dashed p-8 text-center text-[13px] text-muted-foreground">{q || kind ? "没有匹配的交付物。" : "还没有交付物。员工发布设计稿、文档、PR 后会出现在这里。"}</p>}
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {(list.data?.items ?? []).map((d) => (
            <DeliverableCard key={d.id} pid={pid} item={d} />
          ))}
        </div>
      </div>
      <MakeDialog pid={pid} kind={making} onClose={() => setMaking(null)} />
    </main>
  );
}

function DeliverableCard({ pid, item }: { pid: string; item: DeliverableView }) {
  const look = deliverableKind(item.kind);
  const target = deliverableHref(pid, item);
  const body = (
    <>
      <span className="flex h-[150px] items-end justify-center px-[22px] pt-[18px]" style={{ background: look.bg }}>
        <span className="flex size-full flex-col gap-1.5 rounded-t-md bg-white p-3 shadow-[0_1px_4px_rgba(0,0,0,0.08)]">
          <span className="truncate text-[11px] font-semibold text-[#4A4843]">{item.detail.summary || item.title}</span>
          <span className="h-1.5 w-[85%] rounded-sm bg-[#EFEDE8]" />
          <span className="h-1.5 w-[70%] rounded-sm bg-[#EFEDE8]" />
          <span className="h-6 w-full rounded" style={{ background: look.block }} />
          <span className="h-1.5 w-[60%] rounded-sm bg-[#EFEDE8]" />
        </span>
      </span>
      <span className="flex flex-col gap-1 border-t border-[#EFEDE8] px-3.5 py-3">
        <span className="truncate pr-6 text-sm font-medium">{item.title}</span>
        <span className="flex justify-between gap-2 text-xs text-muted-foreground">
          <span>
            {look.label}
            {item.thread_no ? ` · #${item.thread_no}` : ""}
          </span>
          <span>{timeAgo(item.updated_at)}</span>
        </span>
      </span>
    </>
  );
  const cls = "flex flex-col overflow-hidden rounded-xl border bg-panel hover:border-input";
  return (
    <div className="relative">
      {target.to ? (
        <Link to={target.to} className={cls}>
          {body}
        </Link>
      ) : (
        <a href={target.href} target="_blank" rel="noreferrer" className={cls}>
          {body}
        </a>
      )}
      <CardMenu pid={pid} item={item} />
    </div>
  );
}

function CardMenu({ pid, item }: { pid: string; item: DeliverableView }) {
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState(item.title);
  const invalidate = [keys.allDeliverables(pid)];
  const rename = useAction(() => deliverableActions.rename(item.id, title.trim()), { invalidate, onSuccess: () => setRenaming(false) });
  const remove = useAction(() => deliverableActions.remove(item.id), { invalidate, success: "已删除" });
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label={`${item.title} 的菜单`} className="absolute bottom-3 right-2 flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-raised">
            <MoreHorizontal className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => setRenaming(true)}>重命名</DropdownMenuItem>
          <DropdownMenuItem className="text-destructive" onSelect={() => remove.mutate()}>
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <FormDialog open={renaming} onOpenChange={setRenaming} title="重命名交付物" submitLabel="保存" busy={rename.isPending} disabled={!title.trim()} onSubmit={() => rename.mutate()}>
        <Field label="标题">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
        </Field>
      </FormDialog>
    </>
  );
}

function MakeDialog({ pid, kind, onClose }: { pid: string; kind: string | null; onClose: () => void }) {
  const navigate = useNavigate();
  const [brief, setBrief] = useState("");
  const label = kind ? deliverableKind(kind).label : "";
  const request = useAction(() => projectActions.requestDeliverable(pid, kind!, brief.trim()), {
    success: "已交给项目统筹",
    onSuccess: () => {
      setBrief("");
      onClose();
      navigate(`/projects/${pid}`);
    },
  });
  return (
    <FormDialog
      open={kind !== null}
      onOpenChange={(o) => !o && onClose()}
      title={`做一个${label}`}
      description="会作为一条消息发给项目统筹，它决定怎么开讨论串去做。"
      submitLabel="交给统筹"
      busy={request.isPending}
      disabled={!brief.trim()}
      onSubmit={() => request.mutate()}
    >
      <Field label="要做什么">
        <Textarea rows={4} autoFocus value={brief} onChange={(e) => setBrief(e.target.value)} placeholder={`例如：给投资人的 v2 进展介绍`} />
      </Field>
    </FormDialog>
  );
}
