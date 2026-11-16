// Files attached to a message: which are accepted and why one is refused. The console checks the
// same rules again (core.NewAttachment): PNG, JPEG, GIF or WebP images and PDFs up to 5 MB
// (Claude's own limit), UTF-8 text files (by extension) up to 1 MB, at most 8 per message.
// Images and PDFs reach the employee as files; a text file's content goes into the message.

export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
export const IMAGE_ACCEPT = IMAGE_TYPES.join(",");
export const IMAGE_MAX_BYTES = 5 << 20;
export const IMAGE_MAX_COUNT = 8;
export const PDF_TYPE = "application/pdf";
export const TEXT_MAX_BYTES = 1 << 20;

/** Text file extensions the console accepts (core.textExts). */
export const TEXT_EXTS = (
  "md markdown txt text log json jsonl ndjson csv tsv yaml yml toml ini cfg conf xml html htm css scss less js mjs cjs ts tsx jsx vue svelte " +
  "go py rb rs java kt kts scala swift c h cc cpp hpp cs php sh bash zsh fish ps1 sql graphql gql proto lua r dart ex exs erl hs ml clj tf hcl " +
  "gradle properties rst adoc tex diff patch"
).split(" ");
const TEXT_NAMES = ["makefile", "dockerfile", "readme", "license", "gemfile", "procfile"];

/** What the file picker offers: images, PDFs and the text extensions. */
export const ATTACH_ACCEPT = [...IMAGE_TYPES, PDF_TYPE, ...TEXT_EXTS.map((e) => `.${e}`)].join(",");

export type AttachKind = "image" | "pdf" | "text";

type FileLike = { type: string; size: number; name: string };

/** How a file would be attached (null: refused). */
export function attachKind(file: Pick<FileLike, "type" | "name">): AttachKind | null {
  if (IMAGE_TYPES.includes(file.type)) return "image";
  const name = file.name.toLowerCase();
  if (file.type === PDF_TYPE || name.endsWith(".pdf")) return "pdf";
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1) : "";
  if (TEXT_EXTS.includes(ext) || TEXT_NAMES.includes(name)) return "text";
  return null;
}

export function imageProblem(file: FileLike): string | null {
  const kind = attachKind(file);
  if (!kind) return `${file.name || "这个文件"} 不是图片、PDF 或文本文件`;
  if (kind === "text" && file.size > TEXT_MAX_BYTES) return `${file.name} 超过 1 MB`;
  if (file.size > IMAGE_MAX_BYTES) return `${file.name || "图片"} 超过 5 MB`;
  return null;
}

/** Splits picked files into the ones to upload (within the per-message cap) and the reasons for the rest. */
export function pickImages<T extends FileLike>(files: T[], already: number): { ok: T[]; problems: string[] } {
  const ok: T[] = [];
  const problems: string[] = [];
  for (const f of files) {
    const problem = imageProblem(f);
    if (problem) problems.push(problem);
    else if (already + ok.length >= IMAGE_MAX_COUNT) problems.push(`一条消息最多 ${IMAGE_MAX_COUNT} 个附件`);
    else ok.push(f);
  }
  return { ok, problems: [...new Set(problems)] };
}

/** Attachable files among pasted items (pasted text stays text). */
export function imageFiles(list: FileList | File[] | null | undefined): File[] {
  return Array.from(list ?? []).filter((f) => attachKind(f) !== null);
}

export const attachmentUrl = (id: string) => `/api/attachments/${encodeURIComponent(id)}`;

export const isImageType = (contentType: string) => contentType.startsWith("image/");
