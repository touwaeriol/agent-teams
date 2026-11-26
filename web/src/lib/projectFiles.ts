// Project files: the project's folder in the user's shared folder (JuiceFS, projects/<pid>/ of the
// volume; .project-files/ in every employee's working directory). Path rules, breadcrumbs, the
// upload plan and the multipart upload with progress live here; the page is pages/ProjectFiles.vue.
import { ApiError, csrfToken, CSRF_HEADER, toApiError } from "@/api/client";
import type { FileEntry, FilesUploaded, FsView } from "@/api/views";
import { formatBytes } from "./bytes";
import { sendXhr, UploadCancelled } from "./upload";

/** The body limit of one upload request (server orchapi.maxFilesUpload, 1 GiB) minus room for the multipart framing. */
export const BATCH_BYTES = 1000 << 20;
/** Most bytes of a path inside the volume (server userfs.MaxPathBytes). */
export const MAX_PATH_BYTES = 1024;

export const FILES_NOTICE = "项目文件对本项目所有会话和成员可见，不要放密钥或密码。";

/** The folder at the project's root where the platform keeps deliverable versions (the server refuses to delete it). */
export const MANAGED_FOLDER = ".deliverables";

/** Whether an entry is managed by the platform (no delete button). */
export const isManaged = (e: Pick<FileEntry, "dir" | "path">) => e.dir && e.path === MANAGED_FOLDER;

/** The text of the link to the shared folder setting, by the volume's state. */
export function folderLinkText(view: Pick<FsView, "volume"> | null | undefined): string {
  const state = view?.volume?.state;
  if (state === "error") return "开通失败，去重试";
  if (state === "provisioning") return "正在开通，去查看";
  if (state === "deleting") return "正在删除，去查看";
  return state ? "去查看" : "去开通";
}

const utf8Length = (s: string) => new TextEncoder().encode(s).length;

// C0 controls and DEL (server userfs.CleanPath).
// eslint-disable-next-line no-control-regex
const BAD_CHARS = /[\u0000-\u001f\u007f]/;

/**
 * Why a path inside the project's folder would be refused, or null when it is fine. The same rule
 * as the server's userfs.CleanPath (which checks again): '/'-separated, at most 1024 bytes of
 * UTF-8, no empty, '.' or '..' segment, no backslash or control characters.
 */
export function pathProblem(p: string): string | null {
  const t = p.replace(/^\/+|\/+$/g, "");
  if (t === "") return "路径是空的";
  if (utf8Length(t) > MAX_PATH_BYTES) return `路径超过 ${MAX_PATH_BYTES} 字节`;
  if (t.includes("\\")) return "不能有反斜杠";
  if (BAD_CHARS.test(t)) return "不能有控制字符或换行";
  for (const s of t.split("/")) {
    if (s === "") return "不能有空的目录名（比如 a//b）";
    if (s === "." || s === "..") return "不能有 . 或 .. 目录";
  }
  return null;
}

/** dir + "/" + name ("" is the project's folder). */
export function joinPath(dir: string, name: string): string {
  const d = dir.replace(/\/+$/, "");
  return d ? `${d}/${name}` : name;
}

/** The folder above dir ("" for a top-level folder). */
export function parentDir(dir: string): string {
  const i = dir.lastIndexOf("/");
  return i < 0 ? "" : dir.slice(0, i);
}

/** The links of the breadcrumb: the project's folder, then every folder down to dir. */
export function breadcrumbs(dir: string): { name: string; path: string }[] {
  const out = [{ name: "项目文件", path: "" }];
  let path = "";
  for (const seg of dir.split("/").filter(Boolean)) {
    path = joinPath(path, seg);
    out.push({ name: seg, path });
  }
  return out;
}

/** Entries whose name contains the query (case-insensitive); the server already sorts folders first. */
export function filterEntries(entries: FileEntry[], q: string): FileEntry[] {
  const needle = q.trim().toLowerCase();
  return needle ? entries.filter((e) => e.name.toLowerCase().includes(needle)) : entries;
}

export interface PickedFile {
  file: File;
  /** Path relative to the folder being uploaded into (may contain sub-folders). */
  path: string;
}

/** A file with the path it has inside a picked or dropped folder ("" = picked on its own). */
export interface SourceFile {
  file: File;
  rel: string;
}

/** Names nobody wants in the project's files when they come along inside a folder. */
const JUNK = new Set([".DS_Store", ".git", "Thumbs.db", "desktop.ini", "__MACOSX"]);

/**
 * The files to upload with their paths: files that came inside a folder keep their sub-folders;
 * those under a junk name (.DS_Store, .git/…) are skipped and counted.
 */
export function collectFiles(list: ArrayLike<File | SourceFile>): { files: PickedFile[]; skipped: number } {
  const files: PickedFile[] = [];
  let skipped = 0;
  for (const item of Array.from(list)) {
    const src = item instanceof File ? { file: item, rel: item.webkitRelativePath } : item;
    if (src.rel && src.rel.split("/").some((seg) => JUNK.has(seg))) {
      skipped += 1;
      continue;
    }
    files.push({ file: src.file, path: src.rel || src.file.name });
  }
  return { files, skipped };
}

// The parts of the File and Directory Entries API a drop needs (DataTransferItem.webkitGetAsEntry).
interface DropEntry {
  isFile: boolean;
  isDirectory: boolean;
  name: string;
  fullPath: string;
  file?: (ok: (f: File) => void, fail: (e: unknown) => void) => void;
  createReader?: () => { readEntries: (ok: (entries: DropEntry[]) => void, fail: (e: unknown) => void) => void };
}

async function entryFiles(entry: DropEntry, out: SourceFile[], inFolder: boolean): Promise<void> {
  if (entry.isFile && entry.file) {
    const file = await new Promise<File>((ok, fail) => entry.file!(ok, fail));
    out.push({ file, rel: inFolder ? entry.fullPath.replace(/^\/+/, "") : "" });
    return;
  }
  if (!entry.isDirectory || !entry.createReader) return;
  const reader = entry.createReader();
  // readEntries returns at most ~100 entries per call: read until it returns none.
  for (;;) {
    const batch = await new Promise<DropEntry[]>((ok, fail) => reader.readEntries(ok, fail));
    if (batch.length === 0) break;
    for (const child of batch) await entryFiles(child, out, true);
  }
}

/** Whether the first byte of a dropped item can be read (a folder in a plain file list cannot). */
function readable(f: Blob): Promise<boolean> {
  return new Promise((resolve) => {
    try {
      const reader = new FileReader();
      reader.onload = () => resolve(true);
      reader.onerror = () => resolve(false);
      reader.readAsArrayBuffer(f.slice(0, 1));
    } catch {
      resolve(false);
    }
  });
}

/**
 * The files of a drop: dropped folders are walked with webkitGetAsEntry and keep their structure.
 * Browsers without it give the plain file list; a folder then shows up as an empty "file" that
 * cannot be read, which is reported as `folders` (the page asks to use 上传文件夹 instead).
 */
export async function droppedFiles(dt: Pick<DataTransfer, "items" | "files"> | null | undefined): Promise<{ files: SourceFile[]; folders: number }> {
  if (!dt) return { files: [], folders: 0 };
  const items = Array.from(dt.items ?? []).filter((i) => i.kind === "file");
  const entries = items.map((i) => (i as DataTransferItem & { webkitGetAsEntry?: () => DropEntry | null }).webkitGetAsEntry?.() ?? null);
  if (entries.length > 0 && entries.every((e) => e !== null)) {
    const out: SourceFile[] = [];
    for (const e of entries as DropEntry[]) await entryFiles(e, out, e.isDirectory);
    return { files: out, folders: 0 };
  }
  const files: SourceFile[] = [];
  let folders = 0;
  for (const f of Array.from(dt.files ?? [])) {
    // A folder in a plain file list has no type and cannot be read; an empty file without an
    // extension (an empty Makefile) has no type either but reads fine, so size is not looked at.
    const isFolder = f.type === "" && !(await readable(f));
    if (isFolder) folders += 1;
    else files.push({ file: f, rel: "" });
  }
  return { files, folders };
}

export interface UploadPlan {
  files: PickedFile[];
  /** Entries of the current folder the upload replaces (a file, or a folder it writes into, once). */
  overwrites: string[];
  /** Why the upload cannot start (bad paths, duplicates, a file over one request). */
  problems: string[];
}

/** The existing entry of the current folder an upload path replaces or writes into, if any. */
function replaced(path: string, files: Set<string>, dirs: Set<string>): string | null {
  if (files.has(path)) return path;
  const top = path.split("/")[0]!;
  return top !== path && dirs.has(top) ? `${top}/` : null;
}

/**
 * Checks an upload into dir (the current folder): every path (with dir in front, for the length),
 * duplicates within the upload, files too big for one request, and which existing files or
 * folders it would replace or write into (the page asks once first).
 */
export function planUpload(picked: PickedFile[], entries: FileEntry[] | null | undefined, dir = ""): UploadPlan {
  const names = (dir: boolean) => new Set((entries ?? []).filter((e) => e.dir === dir).map((e) => e.name));
  const files = names(false);
  const dirs = names(true);
  const problems: string[] = [];
  const overwrites = new Set<string>();
  const seen = new Set<string>();
  for (const { file, path } of picked) {
    const bad = pathProblem(path) ?? pathProblem(joinPath(dir, path));
    if (bad) problems.push(`${path || file.name}：${bad}`);
    else if (seen.has(path)) problems.push(`${path}：这次上传里有两个同名文件`);
    seen.add(path);
    if (file.size > BATCH_BYTES) problems.push(`${path}：${formatBytes(file.size)}，网页一次最多上传 ${formatBytes(BATCH_BYTES)}，更大的文件请让员工直接放进 .project-files/`);
    const hit = replaced(path, files, dirs);
    if (hit) overwrites.add(hit);
  }
  return { files: picked, overwrites: [...overwrites], problems };
}

/** The download link of a file (always served as an attachment). */
export function downloadUrl(pid: string, path: string): string {
  return `/api/projects/${encodeURIComponent(pid)}/files/download?path=${encodeURIComponent(path)}`;
}

/** Splits the files into batches of at most maxBytes (a file over it gets a batch of its own), in order. */
export function uploadBatches(files: PickedFile[], maxBytes = BATCH_BYTES): PickedFile[][] {
  const out: PickedFile[][] = [];
  let cur: PickedFile[] = [];
  let size = 0;
  for (const f of files) {
    if (cur.length > 0 && size + f.file.size > maxBytes) {
      out.push(cur);
      cur = [];
      size = 0;
    }
    cur.push(f);
    size += f.file.size;
  }
  if (cur.length > 0) out.push(cur);
  return out;
}

/** A failed batch: the files of earlier batches are already stored. */
export class UploadBatchError extends Error {
  constructor(
    public cause: unknown,
    public done: FileEntry[],
    public failed: PickedFile[],
  ) {
    super(cause instanceof Error ? cause.message : String(cause));
  }
}

/** A readable message for a failed upload (409 folder_unavailable / 422 path come with the server's detail). */
export function uploadErrorText(e: unknown): string {
  if (e instanceof UploadCancelled) return e.message;
  if (e instanceof UploadBatchError) {
    const done = e.done.length;
    const reason = uploadErrorText(e.cause);
    return done > 0 ? `已上传 ${done} 个文件，其余 ${e.failed.length} 个没有上传：${reason}` : reason;
  }
  if (e instanceof ApiError && e.status === 413) return `这次上传的请求太大，被服务器拒绝（${e.message}）。请分几次上传`;
  return e instanceof Error ? e.message : String(e);
}

export type SendBatch = (pid: string, dir: string, files: PickedFile[], onProgress?: (fraction: number) => void, signal?: AbortSignal) => Promise<FilesUploaded>;

/**
 * Uploads the files into dir in batches of at most BATCH_BYTES, one request after another.
 * onProgress gets 0..1 over all bytes. When a batch fails (or signal aborts: the cause is then
 * UploadCancelled) the promise rejects with UploadBatchError naming what was stored and what was not.
 */
export async function uploadProjectFiles(
  pid: string,
  dir: string,
  files: PickedFile[],
  onProgress?: (fraction: number) => void,
  signal?: AbortSignal,
  send: SendBatch = sendBatch,
): Promise<FileEntry[]> {
  const all = files.reduce((n, f) => n + f.file.size, 0) || 1;
  const done: FileEntry[] = [];
  let sent = 0;
  const batches = uploadBatches(files);
  for (const [i, batch] of batches.entries()) {
    const bytes = batch.reduce((n, f) => n + f.file.size, 0);
    try {
      if (signal?.aborted) throw new UploadCancelled();
      const res = await send(pid, dir, batch, (fraction) => onProgress?.(Math.min(1, (sent + fraction * bytes) / all)), signal);
      done.push(...(res.files ?? []));
    } catch (e) {
      throw new UploadBatchError(e, done, batches.slice(i).flat());
    }
    sent += bytes;
    onProgress?.(Math.min(1, sent / all));
  }
  return done;
}

/**
 * One request (multipart: dir, one `path` field and one `file` part per file, in order) with
 * XMLHttpRequest, which reports upload progress (fetch does not). onProgress gets 0..1.
 */
async function sendBatch(pid: string, dir: string, files: PickedFile[], onProgress?: (fraction: number) => void, signal?: AbortSignal): Promise<FilesUploaded> {
  const form = new FormData();
  form.append("dir", dir);
  for (const f of files) form.append("path", f.path);
  for (const f of files) form.append("file", f.file, f.file.name);
  const token = csrfToken();
  const xhr = await sendXhr({
    method: "POST",
    url: `/api/projects/${encodeURIComponent(pid)}/files`,
    body: form,
    signal,
    withCredentials: true,
    headers: token ? { [CSRF_HEADER]: token } : {},
    onProgress: (loaded, total) => total > 0 && onProgress?.(loaded / total),
    networkError: () => new ApiError(0, "网络错误，上传没有完成"),
  });
  const body = parseBody(xhr.responseText);
  if (xhr.status >= 200 && xhr.status < 300) return body as FilesUploaded;
  throw toApiError(xhr.status, body, xhr.statusText);
}

/** JSON when it is, else the text. */
function parseBody(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}
