// Attachments of session.send / first_message (attachments_v2, #58): any type, up to 200 MB each.
// The daemon asks the console for a short-lived download URL at delivery time (GET
// /nodes/attachments/{id}/url, node token), streams the file into <cwd>/.attachments/<id>-<name>
// (git-ignored by its own .gitignore) and lists every attachment's path in the text. Images and
// PDFs under the runtime's per-file limits also go in as content blocks. The URL never reaches the AI.
import { createHash, randomBytes } from "node:crypto";
import { constants, createReadStream } from "node:fs";
import { lstat, open, readFile, readdir, realpath, rename, rm, statfs, utimes } from "node:fs/promises";
import { join } from "node:path";
import { Readable, Transform } from "node:stream";
import { pipeline } from "node:stream/promises";
import { pathToFileURL } from "node:url";
import type { AttachmentRef } from "../protocol.js";
import { ensureDir, writeNew } from "../toolhost/download.js";
import { consoleBase, type LoadedImage } from "./images.js";

/** Folder (relative to the session's working directory) attachments are downloaded into. */
export const ATTACH_DIR = ".attachments";
export const MAX_ATTACHMENT_BYTES = 200 * 1024 * 1024;
const GITIGNORE = "*\n";
const ID = /^img_[0-9A-Za-z]{1,60}$/;
const MAX_NAME_BYTES = 120;

export interface AttachmentOptions {
  /** Budget of one file's download (request and body). */
  timeoutMs: number;
  /** Free space that must remain on the disk after the download. */
  minFreeBytes: number;
  /** How long a local copy is kept; 0 keeps them forever. */
  retentionMs: number;
  /** What the retention counts from: the download, or the last delivery / read_attachment that used the copy. */
  retentionFrom: "download" | "last_use";
}

export const DEFAULT_ATTACHMENT_OPTIONS: AttachmentOptions = {
  timeoutMs: 10 * 60_000,
  minFreeBytes: 512 * 1024 * 1024,
  retentionMs: 24 * 3600_000,
  retentionFrom: "last_use",
};

export class AttachmentError extends Error {}

/** How the daemon reaches the console over HTTP (node token). */
export interface ConsoleHttp {
  base: string;
  token: string;
  fetch: typeof fetch;
}

export function consoleHttp(wsUrl: string, token: string, fetchFn: typeof fetch = fetch): ConsoleHttp {
  return { base: consoleBase(wsUrl), token, fetch: fetchFn };
}

/**
 * GET /nodes/attachments/{id}/url: a presigned download URL, or null when the console has none
 * (a legacy row kept in the database, an old console without the endpoint, any error): the caller
 * then falls back to GET /nodes/attachments/{id}.
 */
export async function attachmentUrl(http: ConsoleHttp, id: string, timeoutMs = 30_000): Promise<string | null> {
  try {
    const res = await http.fetch(`${http.base}/nodes/attachments/${encodeURIComponent(id)}/url`, {
      headers: { Authorization: `Bearer ${http.token}` }, redirect: "error", signal: AbortSignal.timeout(timeoutMs),
    });
    if (!res.ok) return null;
    const body = (await res.json()) as { url?: unknown };
    return typeof body.url === "string" && /^https?:\/\//.test(body.url) ? body.url : null;
  } catch {
    return null;
  }
}

/** An attachment on disk in the session's working directory. */
export interface SavedAttachment {
  ref: AttachmentRef;
  abs: string;
  /** Relative to the working directory: .attachments/<id>-<name>. */
  rel: string;
  /** An existing complete copy was used. */
  reused: boolean;
}

/** The file name part after the id: letters, digits, '.', '_' and '-' kept, the rest '_', at most 120 bytes (extension kept). */
export function sanitizeName(name: string): string {
  let s = (name || "").normalize("NFC").replace(/[^\p{L}\p{N}._-]/gu, "_").replace(/^\.+/, "_");
  if (!s) s = "file";
  if (Buffer.byteLength(s) <= MAX_NAME_BYTES) return s;
  const dot = s.lastIndexOf(".");
  const ext = dot > 0 && s.length - dot <= 16 ? s.slice(dot) : "";
  let stem = [...s.slice(0, s.length - ext.length)];
  while (stem.length && Buffer.byteLength(stem.join("") + ext) > MAX_NAME_BYTES) stem.pop();
  return stem.join("") + ext;
}

export function attachmentFileName(ref: Pick<AttachmentRef, "id" | "name">): string {
  return `${ref.id}-${sanitizeName(ref.name)}`;
}

/**
 * Downloads one attachment into <cwd>/.attachments/. A complete copy already there (same size and,
 * when known, sha256) is reused. Sources, in order: `presetUrl` (read_attachment's result) or a
 * fresh URL from the console; once more with a new URL when that fails (expired); then the legacy
 * GET /nodes/attachments/{id} with the node token. The body is streamed to a temporary file
 * (never held in memory), checked against the declared size and sha256, then renamed into place.
 */
export async function downloadAttachment(
  cwd: string, ref: AttachmentRef, http: ConsoleHttp, opts: AttachmentOptions, presetUrl?: string | null,
): Promise<SavedAttachment> {
  if (!ID.test(ref.id)) throw new AttachmentError(`bad attachment id ${JSON.stringify(ref.id)}`);
  if (!Number.isInteger(ref.size) || ref.size < 0 || ref.size > MAX_ATTACHMENT_BYTES) throw new AttachmentError(`bad size ${ref.size}`);
  if (!cwd) throw new AttachmentError("the session has no working directory");
  const root = await realpath(cwd).catch(() => {
    throw new AttachmentError("the working directory is missing");
  });
  const dir = await attachmentDir(root);
  const name = attachmentFileName(ref);
  const abs = join(dir, name);
  const rel = `${ATTACH_DIR}/${name}`;
  if (await completeCopy(abs, ref)) {
    if (opts.retentionFrom === "last_use") await touch(abs);
    return { ref, abs, rel, reused: true };
  }
  await checkFreeSpace(dir, ref.size, opts.minFreeBytes);
  const errors: string[] = [];
  const tryGet = async (url: string, auth: boolean): Promise<boolean> => {
    try {
      await streamTo(dir, name, abs, url, ref, opts.timeoutMs, auth ? http.token : undefined, http.fetch);
      return true;
    } catch (err) {
      errors.push((err as Error).message);
      return false;
    }
  };
  let url = presetUrl !== undefined ? presetUrl : await attachmentUrl(http, ref.id);
  if (url) {
    if (await tryGet(url, false)) return { ref, abs, rel, reused: false };
    url = await attachmentUrl(http, ref.id);
    if (url && (await tryGet(url, false))) return { ref, abs, rel, reused: false };
  }
  if (await tryGet(`${http.base}/nodes/attachments/${encodeURIComponent(ref.id)}`, true)) return { ref, abs, rel, reused: false };
  throw new AttachmentError(errors.at(-1) ?? "download failed");
}

/** <root>/.attachments as a real directory holding a .gitignore ("*", which also ignores itself). */
export async function attachmentDir(root: string): Promise<string> {
  const dir = await ensureDir(join(root, ATTACH_DIR)).catch((err: Error) => {
    throw new AttachmentError(err.message);
  });
  const gi = await lstat(join(dir, ".gitignore")).catch(() => null);
  if (!gi) await writeNew(join(dir, ".gitignore"), Buffer.from(GITIGNORE), true);
  return dir;
}

async function completeCopy(abs: string, ref: AttachmentRef): Promise<boolean> {
  const st = await lstat(abs).catch(() => null);
  if (!st?.isFile() || st.size !== ref.size) return false;
  if (!ref.sha256) return true;
  return (await hashFile(abs)) === ref.sha256;
}

export async function hashFile(abs: string): Promise<string> {
  const h = createHash("sha256");
  await pipeline(createReadStream(abs), new Transform({ transform(c: Buffer, _e, cb) { h.update(c); cb(); } }));
  return h.digest("hex");
}

async function touch(abs: string): Promise<void> {
  const now = new Date();
  await utimes(abs, now, now).catch(() => undefined);
}

/** Refuses a download that would leave less than minFree bytes on the disk. */
export async function checkFreeSpace(dir: string, size: number, minFree: number): Promise<void> {
  const st = await statfs(dir).catch(() => null);
  if (!st) return;
  const free = Number(st.bavail) * Number(st.bsize);
  if (free - size < minFree) throw new AttachmentError(`磁盘空间不足（剩余 ${formatSize(free)}，需要 ${formatSize(size)}，另需保留 ${formatSize(minFree)}）`);
}

async function streamTo(dir: string, name: string, abs: string, url: string, ref: AttachmentRef, timeoutMs: number, token: string | undefined, fetchFn: typeof fetch): Promise<void> {
  await streamDownload({ url, ...(token ? { token } : {}), fetchFn, timeoutMs, size: ref.size, ...(ref.sha256 ? { sha256: ref.sha256 } : {}), dir, tmpName: name, target: abs });
}

export interface StreamDownload {
  url: string;
  /** Node token: sent as Authorization, redirects refused (never to a presigned URL). */
  token?: string;
  fetchFn: typeof fetch;
  timeoutMs: number;
  /** Declared size: more bytes abort the download, fewer fail it. */
  size: number;
  /** Declared sha256 (hex); empty or absent: only the size is checked. */
  sha256?: string;
  /** Folder of the temporary file (the target's folder). */
  dir: string;
  tmpName: string;
  target: string;
}

/**
 * GETs url and streams the body into a temporary file next to the target (exclusive create, no
 * symlink followed), checks size and sha256, then renames it over the target (a symlink at the
 * name is replaced, not followed; a directory there is refused). Nothing is held in memory.
 */
export async function streamDownload(d: StreamDownload): Promise<void> {
  const signal = AbortSignal.timeout(d.timeoutMs);
  const timeout = () => `下载超时（${Math.round(d.timeoutMs / 1000)} 秒）`;
  const res = await d.fetchFn(d.url, { ...(d.token ? { headers: { Authorization: `Bearer ${d.token}` }, redirect: "error" as const } : {}), signal }).catch((err: Error) => {
    throw new AttachmentError(signal.aborted ? timeout() : `下载失败：${err.message}`);
  });
  if (!res.ok || !res.body) {
    await res.body?.cancel().catch(() => undefined);
    throw new AttachmentError(`下载失败：HTTP ${res.status}`);
  }
  const tmp = join(d.dir, `.${d.tmpName.slice(0, 40)}.${randomBytes(6).toString("hex")}.part`);
  const fh = await open(tmp, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o644);
  const h = createHash("sha256");
  let got = 0;
  try {
    const count = new Transform({
      transform(c: Buffer, _e, cb) {
        got += c.length;
        if (got > d.size) return cb(new AttachmentError(`下载的内容超过声明的大小 ${d.size}`));
        h.update(c);
        cb(null, c);
      },
    });
    await pipeline(Readable.fromWeb(res.body as import("node:stream/web").ReadableStream), count, fh.createWriteStream()).catch((err: Error) => {
      if (err instanceof AttachmentError) throw err;
      throw new AttachmentError(signal.aborted ? timeout() : `下载中断：${err.message}`);
    });
    if (got !== d.size) throw new AttachmentError(`下载不完整（${got}/${d.size} 字节）`);
    if (d.sha256 && h.digest("hex") !== d.sha256) throw new AttachmentError("校验和不一致");
    const st = await lstat(d.target).catch(() => null);
    if (st?.isDirectory()) throw new AttachmentError(`${d.target} 是一个目录`);
    await rename(tmp, d.target);
  } catch (err) {
    await fh.close().catch(() => undefined);
    await rm(tmp, { force: true });
    throw err;
  }
}

// ---- what goes to the runtime --------------------------------------------------------------

export type AttachmentRuntime = "claude" | "codex" | "antigravity";

const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const PDF = "application/pdf";
const MB = 1024 * 1024;

/**
 * Limits for handing an attachment to the runtime as a content block (#58).
 *
 * Claude, measured with Agent SDK 0.3.325 and its CLI (#58 t15, haiku):
 *   - images: PNGs of 6 MB, 15 MB base64 and 3000×1500 px were all read (the CLI scales them down);
 *     5 MB of base64 is kept as the conservative limit.
 *   - one PDF: 18.67 MB raw was read; from 18.88 MB raw (≈ 25,000,000 bytes of base64) the CLI drops
 *     the document silently (no error, the model says nothing is attached).
 *   - several PDFs: once the PDFs of the request — including those still in the conversation history
 *     — exceed ≈ 25 MB of base64, documents are dropped silently: 4 × 8 MB in one message left 2,
 *     2 × 12 MB left none, and one 12 MB PDF per turn for 4 turns left the model seeing no document
 *     (not even the new one) in turns 2 and 4. No error ever comes back, so a retry cannot be keyed
 *     on one; instead the daemon counts the PDF base64 it handed to the session (persisted with the
 *     session, reset on a compaction or a new SDK session) and gives only the path beyond 20 MB.
 *   An image next to a 16.8 MB-base64 PDF did not count against that budget.
 */
export const DIRECT_LIMITS = {
  /** Claude Code CLI: an image's base64 at most 5 MB; a PDF at most 15 MB raw (20 MB base64), and 20 MB of PDF base64 per conversation. */
  claude: { imageBase64: 5 * MB, pdf: 15 * MB, sessionPdfBase64: 20 * MB },
  /** agy: images as image blocks, same conservative limit as Claude; everything else a resource_link. */
  antigravity: { imageBase64: 5 * MB },
} as const;

export type ResourceLink = {
  type: "resource_link";
  uri: string;
  name: string;
  mimeType?: string;
  size?: number;
};

/** One attachment line of the [附件] block, or a failure. */
export interface AttachmentOutcome {
  ref: AttachmentRef;
  saved?: SavedAttachment;
  error?: string;
}

export interface Delivery {
  /** Appended to the message text. */
  note: string;
  /** Content blocks (Claude image/document, agy image) or Codex localImage paths. */
  images: LoadedImage[];
  /** agy resource_link blocks. */
  links: ResourceLink[];
}

export function base64Length(n: number): number {
  return Math.ceil(n / 3) * 4;
}

function baseType(t: string): string {
  return t.split(";")[0]!.trim().toLowerCase();
}

/** PDF base64 already handed to a Claude conversation (DIRECT_LIMITS.claude.sessionPdfBase64); planDelivery adds to used. */
export interface PdfBudget {
  used: number;
}

/** Decides per attachment what the runtime gets directly and writes the [附件] block. */
export async function planDelivery(runtime: AttachmentRuntime, outcomes: AttachmentOutcome[], pdfBudget: PdfBudget = { used: 0 }): Promise<Delivery> {
  const images: LoadedImage[] = [];
  const links: ResourceLink[] = [];
  const lines: string[] = [];
  for (const o of outcomes) {
    const head = `- ${o.ref.id} ${o.ref.name}（${o.ref.content_type}，${formatSize(o.ref.size)}）`;
    if (!o.saved) {
      lines.push(`${head}：下载失败（${o.error ?? "未知原因"}），可以稍后用 read_attachment("${o.ref.id}") 重试`);
      continue;
    }
    const t = baseType(o.ref.content_type);
    const how = await directOf(runtime, t, o.saved, images, links, pdfBudget);
    lines.push(`${head} → ${o.saved.abs}（${how}）`);
  }
  if (!lines.length) return { note: "", images, links };
  const note =
    `\n\n[附件]\n${lines.join("\n")}\n` +
    "用 read_attachment(id) 可以重新下载；assign_task、send_message 等工具带 attachments=[id] 可以把附件转给别人；save_attachment(id, path) 可以存为共享文件。";
  return { note, images, links };
}

const PATH_ONLY = "只给路径，请用文件或命令行工具读取";

async function directOf(runtime: AttachmentRuntime, t: string, s: SavedAttachment, images: LoadedImage[], links: ResourceLink[], pdfBudget: PdfBudget): Promise<string> {
  const isImage = IMAGE_TYPES.has(t);
  const size = s.ref.size;
  if (runtime === "codex") {
    if (isImage) {
      images.push({ id: s.ref.id, media_type: t as LoadedImage["media_type"], path: s.abs });
      return "已作为图片附上";
    }
    return PATH_ONLY;
  }
  if (isImage) {
    if (size > 0 && base64Length(size) <= DIRECT_LIMITS[runtime].imageBase64) {
      images.push({ id: s.ref.id, media_type: t as LoadedImage["media_type"], path: s.abs, data: await readFile(s.abs) });
      return "已作为图片附上";
    }
    if (runtime === "antigravity") links.push(resourceLink(s, t));
    return "未直接附上（超出图片大小上限），请用工具读取路径";
  }
  if (runtime === "claude" && t === PDF) {
    const b64 = base64Length(size);
    if (size > 0 && size <= DIRECT_LIMITS.claude.pdf && pdfBudget.used + b64 <= DIRECT_LIMITS.claude.sessionPdfBase64) {
      images.push({ id: s.ref.id, media_type: PDF, path: s.abs, data: await readFile(s.abs) });
      pdfBudget.used += b64;
      return "已作为文档附上";
    }
    return "未直接附上（超出 Claude 文档上限），请用工具读取路径";
  }
  if (runtime === "antigravity") {
    links.push(resourceLink(s, t));
    return `${PATH_ONLY}（另附 resource_link）`;
  }
  return PATH_ONLY;
}

function resourceLink(s: SavedAttachment, t: string): ResourceLink {
  return { type: "resource_link", uri: pathToFileURL(s.abs).href, name: s.ref.name, mimeType: t, size: s.ref.size };
}

export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < MB) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * MB) return `${(n / MB).toFixed(1)} MB`;
  return `${(n / 1024 / MB).toFixed(1)} GB`;
}

// ---- cleanup -------------------------------------------------------------------------------

/**
 * Removes files in the given .attachments folders whose mtime is older than maxAgeMs (and stale
 * .part files); .gitignore stays. Returns the folders that no longer exist (to forget them).
 */
export async function sweepAttachments(dirs: Iterable<string>, maxAgeMs: number, now = Date.now()): Promise<{ removed: number; gone: string[] }> {
  let removed = 0;
  const gone: string[] = [];
  if (maxAgeMs <= 0) return { removed, gone };
  for (const dir of dirs) {
    const st = await lstat(dir).catch(() => null);
    if (!st?.isDirectory() || st.isSymbolicLink()) {
      gone.push(dir);
      continue;
    }
    for (const e of await readdir(dir, { withFileTypes: true }).catch(() => [])) {
      if (e.name === ".gitignore" || !e.isFile()) continue;
      const p = join(dir, e.name);
      const fst = await lstat(p).catch(() => null);
      if (fst && now - fst.mtimeMs > maxAgeMs) {
        await rm(p, { force: true });
        removed++;
      }
    }
  }
  return { removed, gone };
}
