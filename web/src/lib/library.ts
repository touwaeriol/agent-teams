// The 资料库 panel's table logic: names, owners, sizes, types and sorting of
// deliverables, and which files the user may add.
import type { DeliverableView } from "@/api/views";
import { isHosted } from "./artifacts";
import { deliverableKind } from "./present";
import { safeHref } from "./safeurl";

export type SortKey = "name" | "owner" | "modified" | "size" | "type";
export type SortDir = "asc" | "desc";

/** publish_file's limits and file types (server artifacts.MaxBytes, ContentType). */
export const LIBRARY_MAX_BYTES = 5 << 20;
export const LIBRARY_EXTENSIONS = ["html", "htm", "md", "markdown", "svg", "png", "jpg", "jpeg", "gif", "webp", "pdf", "json", "csv", "txt"];
export const LIBRARY_ACCEPT = LIBRARY_EXTENSIONS.map((e) => `.${e}`).join(",");

export function extensionOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

/** Why a file cannot be added, or null when it can. */
export function uploadProblem(file: { name: string; size: number }): string | null {
  if (!LIBRARY_EXTENSIONS.includes(extensionOf(file.name))) return `不支持 ${file.name}：只能添加 html、md、svg、png、jpg、gif、webp、pdf、json、csv、txt`;
  if (file.size === 0) return `${file.name} 是空文件`;
  if (file.size > LIBRARY_MAX_BYTES) return `${file.name} 超过 5 MB`;
  return null;
}

/** 9.4 kB / 1.2 MB (decimal units, like the reference); "—" when unknown. */
export function formatBytes(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "—";
  if (n < 1000) return `${n} B`;
  const units = ["kB", "MB", "GB", "TB"];
  let v = n / 1000;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i += 1;
  }
  return `${v >= 100 ? Math.round(v) : Number(v.toFixed(1))} ${units[i]}`;
}

export function sizeOf(d: Pick<DeliverableView, "detail">): number | undefined {
  const size = (d.detail as { size?: unknown }).size;
  return typeof size === "number" ? size : undefined;
}

/** PNG / HTML for hosted files, otherwise the kind's name (PR, 文档…). */
export function typeLabel(d: Pick<DeliverableView, "kind" | "detail">): string {
  const entry = d.detail.hosted ? d.detail.entry : undefined;
  const ext = entry ? extensionOf(entry) : "";
  if (ext) return ext === "markdown" ? "MD" : ext.toUpperCase();
  return d.kind === "pr" ? "PR" : deliverableKind(d.kind).label;
}

/** Who added it: you, the platform, or the employee's name. */
export function ownerLabel(publishedBy: string): string {
  if (publishedBy === "user") return "你";
  if (!publishedBy || publishedBy === "platform") return "平台";
  return publishedBy;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** 2026年11月14日 4:38 in the viewer's time zone. */
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${d.getHours()}:${pad(d.getMinutes())}`;
}

const SORT_VALUE: Record<SortKey, (d: DeliverableView) => string | number> = {
  name: (d) => d.title.toLowerCase(),
  owner: (d) => ownerLabel(d.published_by),
  modified: (d) => new Date(d.updated_at).getTime(),
  size: (d) => sizeOf(d) ?? -1,
  type: (d) => typeLabel(d),
};

export function sortLibrary(items: DeliverableView[], key: SortKey, dir: SortDir): DeliverableView[] {
  const value = SORT_VALUE[key];
  const sign = dir === "asc" ? 1 : -1;
  return [...items].sort((a, b) => {
    const x = value(a);
    const y = value(b);
    if (x === y) return a.id < b.id ? -1 : 1;
    if (typeof x === "number" && typeof y === "number") return (x - y) * sign;
    return String(x).localeCompare(String(y), "zh-CN") * sign;
  });
}

/** Next sort after clicking a column: same column flips, a new one starts ascending (dates newest first). */
export function nextSort(cur: { key: SortKey; dir: SortDir }, key: SortKey): { key: SortKey; dir: SortDir } {
  if (cur.key === key) return { key, dir: cur.dir === "asc" ? "desc" : "asc" };
  return { key, dir: key === "modified" || key === "size" ? "desc" : "asc" };
}

/** The file's bytes as base64 (the upload body). */
export async function fileBase64(file: Blob): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

/** Where an item opens: designs and hosted files in the viewer, everything else at its URL. */
export function deliverableHref(pid: string, d: Pick<DeliverableView, "id" | "kind" | "url"> & Partial<Pick<DeliverableView, "detail">>): { to?: string; href?: string } {
  if (d.kind === "design" || (d.detail && isHosted({ detail: d.detail }))) return { to: `/projects/${pid}/deliverables/${d.id}` };
  return { href: safeHref(d.url) };
}
