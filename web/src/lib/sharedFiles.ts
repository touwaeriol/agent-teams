// Project shared files (api.md §3.5.1): files every session and user of the project can store and
// fetch, kept by the console, never in git. Path rules, limits, the upload plan (what will be
// overwritten, what does not fit) and the multipart upload with progress live here; the page is
// pages/SharedFiles.vue.
import { ApiError, csrfToken, CSRF_HEADER, toApiError } from "@/api/client";
import type { SharedFile, SharedFileList, SharedFileUpload } from "@/api/views";

/** The server's limits (sharedfiles.Max*), used until the list arrives with its own `limits`. */
export const SHARED_LIMITS = { file_bytes: 20 << 20, project_bytes: 500 << 20, project_files: 1000 };
export const SHARED_MAX_DEPTH = 8;
export const SHARED_MAX_PATH_BYTES = 255;

export const SHARED_NOTICE = "共享文件对本项目所有会话和成员可见，不要放密钥或密码（凭证以后走远程服务器配置）。";

/** 820 B / 1.5 KB / 20 MB (binary units, the same the limits are written in). */
export function formatSize(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v >= 100 || Number.isInteger(v) ? Math.round(v) : Number(v.toFixed(1))} ${units[i]}`;
}

const utf8Length = (s: string) => new TextEncoder().encode(s).length;

// C0, DEL, C1 controls and the Unicode line / paragraph separators (SharedPath).
// eslint-disable-next-line no-control-regex
const BAD_CHARS = /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/;

/**
 * Why a shared-files path would be refused, or null when it is fine. The same rule as the server's
 * sharedfiles.CleanPath (which checks again): relative, '/'-separated, at most 8 segments and 255
 * bytes of UTF-8; no empty, '.' or '..' segment, no segment starting with '.' or starting or ending
 * with a space; no backslash or control characters.
 */
export function sharedPathProblem(p: string): string | null {
  if (p === "") return "路径是空的";
  if (utf8Length(p) > SHARED_MAX_PATH_BYTES) return `路径超过 ${SHARED_MAX_PATH_BYTES} 字节`;
  if (p.startsWith("/")) return "不能是绝对路径（不要以 / 开头）";
  if (p.includes("\\")) return "不能有反斜杠";
  if (BAD_CHARS.test(p)) return "不能有控制字符或换行";
  const segs = p.split("/");
  if (segs.length > SHARED_MAX_DEPTH) return `目录不能超过 ${SHARED_MAX_DEPTH} 层`;
  for (const s of segs) {
    if (s === "") return "不能有空的目录名（比如 a//b 或以 / 结尾）";
    if (s === "." || s === "..") return "不能有 . 或 .. 目录";
    if (s.startsWith(".")) return `“${s}”以 . 开头（隐藏文件不能上传）`;
    if (s.startsWith(" ") || s.endsWith(" ")) return `“${s}”开头或结尾有空格`;
  }
  return null;
}

/** The target path of a file: the optional folder prefix (a trailing '/' is fine) + its name. */
export function joinSharedPath(dir: string, name: string): string {
  const d = dir.replace(/\/+$/, "");
  return d ? `${d}/${name}` : name;
}

/** “你” for the user's uploads, “dev-1 #31” for an employee's (the thread it uploaded from). */
export function uploaderLabel(f: Pick<SharedFile, "uploaded_by" | "uploaded_by_thread_no">): string {
  if (f.uploaded_by === "user") return "你";
  return f.uploaded_by_thread_no ? `${f.uploaded_by} #${f.uploaded_by_thread_no}` : f.uploaded_by;
}

/** Files whose path contains the query (case-insensitive), by path (byte order, like the server). */
export function filterShared(files: SharedFile[], q: string): SharedFile[] {
  const needle = q.trim().toLowerCase();
  const shown = needle ? files.filter((f) => f.path.toLowerCase().includes(needle)) : files.slice();
  return shown.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
}

export interface PickedFile {
  file: File;
  /** Target path in the shared files (folder prefix + name, may contain sub-folders). */
  path: string;
}

export interface UploadPlan {
  files: PickedFile[];
  /** Paths that already exist and will be replaced. */
  overwrites: string[];
  /** Why the upload cannot start (bad paths, too large, over the project's limits). */
  problems: string[];
  /** Project total after the upload. */
  totalAfter: number;
}

/** The files picked (or dropped) with their target paths: a folder pick keeps its sub-folders. */
export function pickedFiles(list: ArrayLike<File>, dir: string): PickedFile[] {
  return Array.from(list).map((file) => ({ file, path: joinSharedPath(dir.trim(), file.webkitRelativePath || file.name) }));
}

/**
 * Checks an upload against the list before sending it: every path, duplicates within the upload,
 * the per-file limit and the project's byte and file limits (an overwrite counts the difference),
 * and which existing files it would overwrite (the page asks once before replacing them).
 */
export function planUpload(picked: PickedFile[], list: Pick<SharedFileList, "files" | "total_bytes" | "file_count" | "limits"> | undefined): UploadPlan {
  const limits = list?.limits ?? SHARED_LIMITS;
  const existing = new Map((list?.files ?? []).map((f) => [f.path, f.size]));
  const problems: string[] = [];
  const overwrites: string[] = [];
  const seen = new Set<string>();
  let total = list?.total_bytes ?? 0;
  let count = list?.file_count ?? existing.size;
  for (const { file, path } of picked) {
    const bad = sharedPathProblem(path);
    if (bad) problems.push(`${path || file.name}：${bad}`);
    else if (seen.has(path)) problems.push(`${path}：这次上传里有两个同名文件`);
    seen.add(path);
    if (file.size > limits.file_bytes) problems.push(`${path}：${formatSize(file.size)}，超过单个文件 ${formatSize(limits.file_bytes)} 的上限`);
    const old = existing.get(path);
    if (old === undefined) {
      count += 1;
    } else {
      overwrites.push(path);
      total -= old;
    }
    total += file.size;
  }
  if (total > limits.project_bytes) {
    problems.push(`上传后项目共享文件合计 ${formatSize(total)}，超过 ${formatSize(limits.project_bytes)} 的上限，先删掉不用的文件`);
  }
  if (count > limits.project_files) problems.push(`上传后项目共享文件有 ${count} 个，超过 ${limits.project_files} 个的上限`);
  return { files: picked, overwrites, problems, totalAfter: total };
}

/** The download link of a file (always served as an attachment). */
export function downloadUrl(pid: string, path: string): string {
  return `/api/projects/${encodeURIComponent(pid)}/shared-files/download?path=${encodeURIComponent(path)}`;
}

/** A readable message for a failed upload (413 / 409 quota / 422 path come with the server's detail). */
export function uploadErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 413) return e.message.includes("MB") ? e.message : `文件太大：单个文件不能超过 ${formatSize(SHARED_LIMITS.file_bytes)}`;
    return e.message;
  }
  return e instanceof Error ? e.message : String(e);
}

/**
 * Uploads the files (multipart: one `file` part and one `path` field per file, in order) with
 * XMLHttpRequest, which reports upload progress (fetch does not). onProgress gets 0..1.
 */
export function uploadSharedFiles(pid: string, files: PickedFile[], onProgress?: (fraction: number) => void): Promise<SharedFileUpload> {
  const form = new FormData();
  for (const f of files) form.append("path", f.path);
  for (const f of files) form.append("file", f.file, f.file.name);
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/projects/${encodeURIComponent(pid)}/shared-files`);
    xhr.withCredentials = true;
    const token = csrfToken();
    if (token) xhr.setRequestHeader(CSRF_HEADER, token);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      let body: unknown = xhr.responseText;
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        // not JSON: keep the text
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as SharedFileUpload);
      else reject(toApiError(xhr.status, body, xhr.statusText));
    };
    xhr.onerror = () => reject(new ApiError(0, "网络错误，上传没有完成"));
    xhr.send(form);
  });
}
