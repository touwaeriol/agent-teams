// 资料库: the project's deliverables and published files as a table (or tiles),
// filtered by type, searchable, with "+ 添加" to upload a file the team can read.
// Used as the right-hand panel of the main board and on the full Deliverables page.
import { ChevronDown, ChevronUp, FileText, Grid2x2, List, Maximize2, MoreHorizontal, Plus, Search, X } from "lucide-react";
import { useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { deliverables as deliverableActions, projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverables } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import { Field, FormDialog, IconButton, IconLink, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { deliverableHref, fileBase64, formatBytes, formatDateTime, LIBRARY_ACCEPT, nextSort, ownerLabel, sizeOf, sortLibrary, typeLabel, uploadProblem, type SortKey } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { cn } from "@/lib/utils";
import { Initial } from "./bits";

export const LIBRARY_TYPES: [string, string][] = [
  ["", "全部类型"],
  ["design", "设计稿"],
  ["doc", "文档"],
  ["slides", "幻灯片"],
  ["pr", "PR"],
  ["data", "数据"],
];

type View = "list" | "grid";

function storedView(): View {
  try {
    return localStorage.getItem("library.view") === "grid" ? "grid" : "list";
  } catch {
    return "list";
  }
}

function saveView(v: View) {
  try {
    localStorage.setItem("library.view", v);
  } catch {
    /* private mode: the choice lasts for this page only */
  }
}

/** Uploads picked files one after another; refused ones become a toast. */
function useUpload(pid: string) {
  const upload = useAction((file: File) => fileBase64(file).then((data) => projectActions.upload(pid, { name: file.name, data })), {
    invalidate: [keys.allDeliverables(pid)],
    success: (d) => `已添加 ${d.title}`,
  });
  return async (files: FileList | null) => {
    for (const file of Array.from(files ?? [])) {
      const problem = uploadProblem(file);
      if (problem) toast.error(problem);
      else await upload.mutateAsync(file).catch(() => undefined);
    }
  };
}

export function LibraryBrowser({ pid, variant, onClose, extra }: { pid: string; variant: "panel" | "page"; onClose?: () => void; extra?: ReactNode }) {
  const [kind, setKind] = useState("");
  const [q, setQ] = useState("");
  const [searching, setSearching] = useState(variant === "page");
  const [view, setView] = useState<View>(storedView);
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "modified", dir: "desc" });
  const list = useDeliverables(pid, kind, q);
  const items = sortLibrary(list.data?.items ?? [], sort.key, sort.dir);
  const pick = (v: View) => {
    setView(v);
    saveView(v);
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {variant === "panel" && (
        <div className="flex h-14 shrink-0 items-center gap-1 border-b px-4">
          <span className="flex-1 font-semibold">资料库</span>
          <IconButton label="搜索" active={searching} onClick={() => setSearching(!searching)}>
            <Search strokeWidth={1.8} />
          </IconButton>
          <IconLink label="全屏打开" to={`/projects/${pid}/deliverables`}>
            <Maximize2 strokeWidth={1.8} />
          </IconLink>
          <IconButton label="关闭资料库" onClick={onClose}>
            <X strokeWidth={1.8} />
          </IconButton>
        </div>
      )}
      <div className={cn("flex shrink-0 flex-wrap items-center gap-2", variant === "panel" ? "px-4 pt-3" : "")}>
        <TypeFilter value={kind} onChange={setKind} />
        {searching && (
          <label className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border border-input bg-panel px-2.5 text-muted-foreground sm:max-w-60">
            <Search className="size-3.5 shrink-0" strokeWidth={1.8} />
            <input autoFocus={variant === "panel"} value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索资料库" className="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" />
          </label>
        )}
        <span className="ml-auto flex items-center gap-2">
          {extra}
          <ViewToggle value={view} onChange={pick} />
          <AddButton pid={pid} />
        </span>
      </div>
      <div className={cn("scroll-thin min-h-0 flex-1 overflow-y-auto", variant === "panel" ? "px-4 pb-3 pt-2" : "pt-3")}>
        {list.isLoading && <Loading />}
        {list.data && items.length === 0 && <Empty filtered={Boolean(q || kind)} />}
        {items.length > 0 && view === "list" && <LibraryTable pid={pid} items={items} sort={sort} onSort={(k) => setSort(nextSort(sort, k))} compact={variant === "panel"} />}
        {items.length > 0 && view === "grid" && <LibraryGrid pid={pid} items={items} />}
      </div>
      <div className={cn("flex shrink-0 items-center border-t py-2.5 text-xs text-muted-foreground", variant === "panel" ? "px-4" : "")}>
        已使用 {formatBytes(list.data?.storage_bytes ?? 0)}
        <span className="ml-auto">{items.length} 项</span>
      </div>
    </div>
  );
}

function Empty({ filtered }: { filtered: boolean }) {
  return (
    <p className="rounded-xl border border-dashed p-8 text-center text-[13px] text-muted-foreground">
      {filtered ? "没有匹配的内容。" : "资料库还是空的。员工发布的设计稿、文档、PR 会出现在这里，你也可以点“添加”上传文件。"}
    </p>
  );
}

function TypeFilter({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const label = LIBRARY_TYPES.find(([id]) => id === value)?.[1] ?? "全部类型";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" className="flex h-8 items-center gap-1 rounded-lg px-2 text-[13px] font-medium hover:bg-raised data-[state=open]:bg-raised">
          {value ? label : "类型"}
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {LIBRARY_TYPES.map(([id, name]) => (
          <DropdownMenuItem key={id || "all"} onSelect={() => onChange(id)} className={cn(id === value && "font-semibold")}>
            {name}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ViewToggle({ value, onChange }: { value: View; onChange: (v: View) => void }) {
  const cls = (on: boolean) => cn("flex size-7 items-center justify-center rounded-md", on ? "bg-panel shadow-sm" : "text-muted-foreground hover:text-foreground");
  return (
    <span role="group" aria-label="显示方式" className="flex items-center gap-0.5 rounded-lg bg-raised p-0.5">
      <button type="button" aria-label="网格" aria-pressed={value === "grid"} onClick={() => onChange("grid")} className={cls(value === "grid")}>
        <Grid2x2 className="size-4" strokeWidth={1.8} />
      </button>
      <button type="button" aria-label="列表" aria-pressed={value === "list"} onClick={() => onChange("list")} className={cls(value === "list")}>
        <List className="size-4" strokeWidth={1.8} />
      </button>
    </span>
  );
}

function AddButton({ pid }: { pid: string }) {
  const input = useRef<HTMLInputElement>(null);
  const upload = useUpload(pid);
  return (
    <>
      <Button size="sm" onClick={() => input.current?.click()}>
        <Plus /> 添加
      </Button>
      <input
        ref={input}
        type="file"
        multiple
        accept={LIBRARY_ACCEPT}
        className="hidden"
        aria-label="选择要添加的文件"
        onChange={(e) => {
          void upload(e.target.files);
          e.target.value = "";
        }}
      />
    </>
  );
}

function ItemLink({ pid, item, className, children }: { pid: string; item: DeliverableView; className?: string; children: ReactNode }) {
  const target = deliverableHref(pid, item);
  if (target.to)
    return (
      <Link to={target.to} className={className}>
        {children}
      </Link>
    );
  return (
    <a href={target.href} target="_blank" rel="noreferrer" className={className}>
      {children}
    </a>
  );
}

function KindGlyph({ kind, size = 22 }: { kind: string; size?: number }) {
  const look = deliverableKind(kind);
  return (
    <span aria-hidden className="flex shrink-0 items-center justify-center rounded-md text-[11px] font-bold" style={{ width: size, height: size, background: look.bg, color: look.fg }}>
      {kind === "doc" ? <FileText className="size-3.5" /> : look.glyph}
    </span>
  );
}

function Owner({ by }: { by: string }) {
  const name = ownerLabel(by);
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <Initial name={name} bg={by === "user" ? "#F4E3D7" : "#ECEAE4"} fg={by === "user" ? "#A24E1E" : "#4A4843"} size={18} round />
      <span className="truncate">{name}</span>
    </span>
  );
}

const COLUMNS: { key: SortKey; label: string; className: string }[] = [
  { key: "name", label: "名称", className: "min-w-0 flex-1" },
  { key: "owner", label: "所有者", className: "w-28 max-md:hidden" },
  { key: "modified", label: "修改日期", className: "w-40 max-lg:hidden" },
  { key: "size", label: "大小", className: "w-20 max-sm:hidden" },
  { key: "type", label: "类型", className: "w-16" },
];

function LibraryTable({ pid, items, sort, onSort, compact }: { pid: string; items: DeliverableView[]; sort: { key: SortKey; dir: "asc" | "desc" }; onSort: (k: SortKey) => void; compact: boolean }) {
  const cols = compact ? COLUMNS.map((c) => (c.key === "modified" ? { ...c, className: "w-36 max-2xl:hidden" } : c.key === "owner" ? { ...c, className: "w-24 max-xl:hidden" } : c)) : COLUMNS;
  return (
    <div role="table" aria-label="资料库" className="text-[13px]">
      <div role="row" className="sticky top-0 z-[1] flex h-9 items-center gap-3 rounded-lg bg-raised/70 px-3 text-xs text-muted-foreground backdrop-blur">
        {cols.map((c) => (
          <button key={c.key} type="button" role="columnheader" aria-sort={sort.key === c.key ? (sort.dir === "asc" ? "ascending" : "descending") : "none"} onClick={() => onSort(c.key)} className={cn("flex items-center gap-1 text-left font-medium hover:text-foreground", c.className)}>
            {c.label}
            {sort.key === c.key && (sort.dir === "asc" ? <ChevronUp className="size-3" /> : <ChevronDown className="size-3" />)}
          </button>
        ))}
        <span className="w-7" />
      </div>
      {items.map((d) => (
        <div key={d.id} role="row" className="group flex h-10 items-center gap-3 rounded-lg px-3 hover:bg-raised/60">
          <ItemLink pid={pid} item={d} className={cn("flex items-center gap-2.5", cols[0].className)}>
            <KindGlyph kind={d.kind} />
            <span className="truncate">{d.title}</span>
            {d.thread_no && <span className="shrink-0 text-xs text-muted-foreground">#{d.thread_no}</span>}
          </ItemLink>
          <span className={cn("text-muted-foreground", cols[1].className)}>
            <Owner by={d.published_by} />
          </span>
          <span className={cn("tabular-nums text-muted-foreground", cols[2].className)}>{formatDateTime(d.updated_at)}</span>
          <span className={cn("tabular-nums text-muted-foreground", cols[3].className)}>{formatBytes(sizeOf(d))}</span>
          <span className={cn("text-muted-foreground", cols[4].className)}>{typeLabel(d)}</span>
          <ItemMenu item={d} pid={pid} />
        </div>
      ))}
    </div>
  );
}

function LibraryGrid({ pid, items }: { pid: string; items: DeliverableView[] }) {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
      {items.map((d) => {
        const look = deliverableKind(d.kind);
        return (
          <div key={d.id} className="group relative">
            <ItemLink pid={pid} item={d} className="flex flex-col overflow-hidden rounded-xl border bg-panel hover:border-input">
              <span className="flex h-24 items-center justify-center text-[22px] font-bold" style={{ background: look.bg, color: look.fg }}>
                {typeLabel(d)}
              </span>
              <span className="flex flex-col gap-0.5 px-3 py-2">
                <span className="truncate pr-5 text-[13px] font-medium">{d.title}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {ownerLabel(d.published_by)} · {formatBytes(sizeOf(d))}
                </span>
              </span>
            </ItemLink>
            <span className="absolute bottom-2 right-1">
              <ItemMenu item={d} pid={pid} />
            </span>
          </div>
        );
      })}
    </div>
  );
}

/** 重命名 / 删除 for one row or tile. */
export function ItemMenu({ pid, item }: { pid: string; item: DeliverableView }) {
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState(item.title);
  const invalidate = [keys.allDeliverables(pid)];
  const rename = useAction(() => deliverableActions.rename(item.id, title.trim()), { invalidate, onSuccess: () => setRenaming(false) });
  const remove = useAction(() => deliverableActions.remove(item.id), { invalidate, success: "已删除" });
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label={`${item.title} 的菜单`} className="flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground opacity-0 hover:bg-background focus:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100 max-md:opacity-100">
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
      <FormDialog open={renaming} onOpenChange={setRenaming} title="重命名" submitLabel="保存" busy={rename.isPending} disabled={!title.trim()} onSubmit={() => rename.mutate()}>
        <Field label="名称">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
        </Field>
      </FormDialog>
    </>
  );
}
