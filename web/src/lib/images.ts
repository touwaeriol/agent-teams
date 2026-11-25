// Files attached to a message (#58): any type, at most 200 MB each, at most 20 per message (the
// node protocol's limit). The console checks the same again. They are uploaded straight to storage
// in parts (lib/upload.ts); without object storage the old base64 upload takes files up to 5 MB.
// Images show as thumbnails, everything else as a file card; employees get every file on disk.

export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
export const PDF_TYPE = "application/pdf";
/** Largest attachment (core.MaxAttachmentBytes). */
export const ATTACH_MAX_BYTES = 200 << 20;
/** Attachments per message (protocol session.send attachments maxItems). */
export const ATTACH_MAX_COUNT = 20;
/** What the old base64 upload (no object storage) still takes. */
export const LEGACY_MAX_BYTES = 5 << 20;

/** The file picker offers everything. */
export const ATTACH_ACCEPT = "";

/** Text file extensions shown as "文本" (the console inlines small text files into the message). */
export const TEXT_EXTS = (
  "md markdown txt text log json jsonl ndjson csv tsv yaml yml toml ini cfg conf xml html htm css scss less js mjs cjs ts tsx jsx vue svelte " +
  "go py rb rs java kt kts scala swift c h cc cpp hpp cs php sh bash zsh fish ps1 sql graphql gql proto lua r dart ex exs erl hs ml clj tf hcl " +
  "gradle properties rst adoc tex diff patch"
).split(" ");
const TEXT_NAMES = ["makefile", "dockerfile", "readme", "license", "gemfile", "procfile"];

export type AttachKind = "image" | "pdf" | "text" | "file";

type FileLike = { type: string; size: number; name: string };

const extOf = (name: string) => (name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "");

/** How a file is shown (every file is accepted). */
export function attachKind(file: Pick<FileLike, "type" | "name">): AttachKind {
  if (IMAGE_TYPES.includes(file.type)) return "image";
  const name = file.name.toLowerCase();
  if (file.type === PDF_TYPE || name.endsWith(".pdf")) return "pdf";
  if (file.type.startsWith("text/") || TEXT_EXTS.includes(extOf(name)) || TEXT_NAMES.includes(name)) return "text";
  return "file";
}

export function attachProblem(file: FileLike): string | null {
  if (file.size === 0) return `${file.name || "这个文件"} 是空文件（0 字节），不能作为附件`;
  if (file.size > ATTACH_MAX_BYTES) return `${file.name || "这个文件"} 超过 200 MB`;
  return null;
}

/** Splits picked files into the ones to upload (within the per-message cap) and the reasons for the rest. */
export function pickImages<T extends FileLike>(files: T[], already: number): { ok: T[]; problems: string[] } {
  const ok: T[] = [];
  const problems: string[] = [];
  for (const f of files) {
    const problem = attachProblem(f);
    if (problem) problems.push(problem);
    else if (already + ok.length >= ATTACH_MAX_COUNT) problems.push(`一条消息最多 ${ATTACH_MAX_COUNT} 个附件`);
    else ok.push(f);
  }
  return { ok, problems: [...new Set(problems)] };
}

/** Files among pasted items (pasted text stays text). */
export function imageFiles(list: FileList | File[] | null | undefined): File[] {
  return Array.from(list ?? []);
}

/**
 * The `attachments` field of a request body: only present with files, so a request without
 * attachments is exactly what it was before (servers without the field reject unknown ones).
 */
export const attachmentsField = (ids: string[] | null | undefined): { attachments?: string[] } => (ids?.length ? { attachments: ids } : {});

export const attachmentUrl = (id: string) => `/api/attachments/${encodeURIComponent(id)}`;
/** Always a download (never shown inline). */
export const attachmentDownloadUrl = (id: string) => `${attachmentUrl(id)}?download=1`;

export const isImageType = (contentType: string) => IMAGE_TYPES.includes(contentType.split(";")[0]!.trim().toLowerCase()) || contentType === "image/svg+xml";

/** A short label of a file's type for its card: PDF, 文本, ZIP, … */
export function typeLabel(contentType: string, name: string): string {
  const t = contentType.split(";")[0]!.trim().toLowerCase();
  if (t === PDF_TYPE) return "PDF";
  if (t.startsWith("image/")) return "图片";
  if (t.startsWith("text/") || TEXT_EXTS.includes(extOf(name))) return "文本";
  const ext = extOf(name);
  return ext && ext.length <= 6 ? ext.toUpperCase() : "文件";
}
