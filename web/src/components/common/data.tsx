import type { ReactNode } from "react";
import { Badge, Dot } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export type Tone = "ok" | "info" | "warn" | "bad" | "neutral" | "primary";

/** A status shown as a colored pill; ``spinning`` for work in progress. */
export function StatusBadge({ tone, label, dot, spinning, className }: { tone: Tone; label: ReactNode; dot?: boolean; spinning?: boolean; className?: string }) {
  return (
    <Badge tone={tone} className={className}>
      {spinning && <span className="inline-block size-2.5 animate-spin rounded-full border-2 border-current border-r-transparent" aria-hidden />}
      {dot && <Dot tone={tone} />}
      {label}
    </Badge>
  );
}

/** Small monospace chip: an employee, node or ref, with an optional status dot. */
export function Chip({ tone, children, title, className }: { tone?: Tone; children: ReactNode; title?: string; className?: string }) {
  return (
    <span title={title} className={cn("inline-flex h-8 items-center gap-1.5 rounded-md bg-raised px-2.5 font-mono text-xs", className)}>
      {tone && <Dot tone={tone} />}
      {children}
    </span>
  );
}

/** One number or short fact with its label. */
export function StatCard({ label, value }: { label: ReactNode; value: ReactNode }) {
  return (
    <div className="flex flex-col gap-1 rounded-xl border bg-panel px-4 py-3.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="truncate text-xl font-semibold">{value}</span>
    </div>
  );
}

export function StatGrid({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-3">{children}</div>;
}

/** Label / value rows. */
export function KeyValueList({ items }: { items: { label: ReactNode; value: ReactNode; mono?: boolean }[] }) {
  return (
    <dl className="flex flex-col gap-1.5 text-[13px]">
      {items.map((item, i) => (
        <div key={i} className="flex justify-between gap-3 border-b border-border/60 pb-1.5">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className={cn("text-right", item.mono !== false && "font-mono text-xs")}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

export interface Column<T> {
  header: ReactNode;
  cell: (row: T) => ReactNode;
  className?: string;
}

/** A horizontally scrollable table with a header row. */
export function DataTable<T>({ columns, rows, rowKey, empty, minWidth = 640, className }: { columns: Column<T>[]; rows: T[]; rowKey: (row: T) => string; empty?: ReactNode; minWidth?: number; className?: string }) {
  return (
    <div className={cn("overflow-x-auto rounded-lg border", className)}>
      <table className="w-full border-collapse text-[13px]" style={{ minWidth }}>
        <thead className="bg-well text-left text-muted-foreground">
          <tr>
            {columns.map((c, i) => (
              <th key={i} className={cn("px-3 py-2.5 font-medium", c.className)}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={rowKey(row)} className="border-t">
              {columns.map((c, i) => (
                <td key={i} className={cn("px-3 py-2.5 align-middle", c.className)}>
                  {c.cell(row)}
                </td>
              ))}
            </tr>
          ))}
          {rows.length === 0 && empty && (
            <tr>
              <td colSpan={columns.length} className="px-3 py-6 text-center text-muted-foreground">
                {empty}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/** A scrollable list where one item can be picked. */
export function PickList<T>({ items, itemKey, selected, onPick, render, empty, loading, error, className }: {
  items: T[];
  itemKey: (item: T) => string;
  selected?: string;
  onPick: (item: T) => void;
  render: (item: T, picked: boolean) => ReactNode;
  empty?: ReactNode;
  loading?: boolean;
  error?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("scroll-thin max-h-56 overflow-y-auto rounded-lg border bg-well", className)}>
      {loading && <div className="p-3 text-xs text-muted-foreground">加载中……</div>}
      {error && <div className="p-3 text-xs text-destructive">{error}</div>}
      {!loading && !error && items.length === 0 && empty && <div className="p-3 text-xs text-muted-foreground">{empty}</div>}
      {items.map((item) => {
        const key = itemKey(item);
        const picked = key === selected;
        return (
          <button
            type="button"
            key={key}
            onClick={() => onPick(item)}
            className={cn(
              "flex w-full items-center gap-2 border-b border-border/60 px-3 py-2 text-left last:border-0 hover:bg-raised",
              picked && "bg-accent text-accent-foreground hover:bg-accent",
            )}
          >
            {render(item, picked)}
          </button>
        );
      })}
    </div>
  );
}
