// The 资料库 panel's table logic: names, owners, sizes, types and sorting of
// deliverables, and which files the user may add.
import type { DeliverableView } from "@/api/views";
import { isHosted } from "./artifacts";
import { deliverableKind } from "./present";
import { safeHref } from "./safeurl";

export type SortKey = "name" | "owner" | "modified" | "size" | "type";
export type SortDir = "asc" | "desc";

/**
 * publish_file's limits and the file types the user may add (server artifacts.MaxBytes). Images
 * are not deliverables: screenshots belong inside a report document (they become thread
 * attachments on the server), so the library takes no png/jpg/gif/webp/svg.
 */
export const LIBRARY_MAX_BYTES = 5 << 20;
export const LIBRARY_EXTENSIONS = ["html", "htm", "md", "markdown", "pdf", "json", "csv", "txt"];
export const LIBRARY_ACCEPT = LIBRARY_EXTENSIONS.map((e) => `.${e}`).join(",");
export const IMAGE_EXTENSIONS = ["png", "jpg", "jpeg", "gif", "webp", "svg"];
export const IMAGE_NOT_DELIVERABLE = "图片不是交付物，请把截图放进报告文档（html/md/pdf）里";

export function extensionOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

/**
 * A hosted image (render=image, an image content type, or a png/jpg/gif/webp/svg entry). Images
 * are no longer deliverables; older ones stay in the data but no list shows them.
 */
export function isImageDeliverable(d: Pick<DeliverableView, "detail">): boolean {
  if (!isHosted(d)) return false;
  if (d.detail.render === "image") return true;
  const type = d.detail.content_type;
  if (typeof type === "string" && type.toLowerCase().startsWith("image/")) return true;
  return IMAGE_EXTENSIONS.includes(extensionOf(String(d.detail.entry ?? "")));
}

/** Whether the 资料库 (and the thread's 交付物 view) lists a deliverable: everything but images. */
export function isLibraryDeliverable(d: Pick<DeliverableView, "detail">): boolean {
  return !isImageDeliverable(d);
}

/** Why a file cannot be added, or null when it can. */
export function uploadProblem(file: { name: string; size: number }): string | null {
  const ext = extensionOf(file.name);
  if (IMAGE_EXTENSIONS.includes(ext)) return `${file.name}：${IMAGE_NOT_DELIVERABLE}`;
  if (!LIBRARY_EXTENSIONS.includes(ext)) return `不支持 ${file.name}：只能添加 html、md、pdf、json、csv、txt`;
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

/** PNG / HTML for hosted files, otherwise the kind's name (设计稿, 文档…). */
export function typeLabel(d: Pick<DeliverableView, "kind" | "detail">): string {
  const entry = d.detail.hosted ? d.detail.entry : undefined;
  const ext = entry ? extensionOf(entry) : "";
  if (ext) return ext === "markdown" ? "MD" : ext.toUpperCase();
  return deliverableKind(d.kind).label;
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

export type FileIconKind = "image" | "code" | "text" | "table" | "design" | "link";

const ICON_BY_EXT: Record<string, FileIconKind> = {
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image", svg: "image",
  html: "code", htm: "code", json: "table", csv: "table",
  md: "text", markdown: "text", txt: "text", pdf: "text",
};
const ICON_BY_KIND: Record<string, FileIconKind> = { design: "design", doc: "text", data: "table", dashboard: "table", slides: "design", board: "design" };

/** Which outline icon a library row shows: by the hosted file's extension, else by kind. */
export function fileIcon(d: Pick<DeliverableView, "kind" | "detail">): FileIconKind {
  const entry = d.detail.hosted ? d.detail.entry : undefined;
  const byExt = entry ? ICON_BY_EXT[extensionOf(String(entry))] : undefined;
  return byExt ?? ICON_BY_KIND[d.kind] ?? "link";
}

/** Table columns that fit a library this wide (px); 名称 and 类型 always show. */
export function libraryColumns(width: number): Set<SortKey> {
  const cols = new Set<SortKey>(["name", "type"]);
  if (width >= 420) cols.add("size");
  if (width >= 540) cols.add("owner");
  if (width >= 660) cols.add("modified");
  return cols;
}
