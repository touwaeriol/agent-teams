import { createHash } from "node:crypto";
import { constants } from "node:fs";
import { lstat, open, readdir, realpath } from "node:fs/promises";
import { basename, extname, isAbsolute, join, relative, resolve, sep } from "node:path";
import bundle from "../../../protocol/gen/bundle.schema.json" with { type: "json" };
import type { ToolUpload, UploadFile } from "../protocol.js";
import { newId } from "../util/ids.js";
import { isInside } from "../util/paths.js";

/** Limits of publish_file (the console checks them again). */
export const MAX_BYTES = 5 * 1024 * 1024;
export const MAX_FILES = 64;
export const CHUNK_BYTES = 1024 * 1024;
/** Per-file limit of write_shared_file / read_shared_file (the console checks it again). */
export const MAX_SHARED_BYTES = 20 * 1024 * 1024;
/** Folder (relative to the session's working directory) shared files are fetched into; write_shared_file strips it from a default path. */
export const SHARED_DIR = ".shared";
/** SharedPath of the tool contract (tools/common.json), taken from the generated bundle so it never drifts. */
const SHARED_PATH = new RegExp((bundle as unknown as { $defs: { SharedPath: { pattern: string } } }).$defs.SharedPath.pattern);
const MAX_SHARED_PATH_BYTES = 255;
const SHARED_PATH_RULES = "relative, '/'-separated, 1 to 8 parts, at most 255 bytes; no part empty, starting with a dot or starting/ending with a space; no backslash or control characters";

/** Allowed extensions and the type they are served with (same list as the console). */
const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".md": "text/markdown; charset=utf-8",
  ".markdown": "text/markdown; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf",
  ".json": "application/json; charset=utf-8", ".csv": "text/csv; charset=utf-8", ".txt": "text/plain; charset=utf-8",
  ".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
  ".woff": "font/woff", ".woff2": "font/woff2",
};
const SKIP_DIRS = new Set(["node_modules", "__MACOSX"]);
const NAME = /^[A-Za-z0-9_][A-Za-z0-9._ -]*$/;

export class UploadError extends Error {}

export interface PreparedUpload {
  upload: ToolUpload;
  /** Base64 chunk payloads, in order (file.chunk index = position). */
  chunks: string[];
}

interface Picked {
  path: string;
  abs: string;
}

export function contentType(path: string): string | undefined {
  return TYPES[extname(path).toLowerCase()];
}

/** A stored file path: relative segments that do not start with a dot, an allowed extension (same rule as the console). */
export function validFilePath(path: string): boolean {
  return path.length > 0 && path.length <= 300 && !!contentType(path) && path.split("/").every((part) => NAME.test(part));
}

/** A stored shared-files path (tools/common.json#SharedPath). */
export function validSharedPath(path: string): boolean {
  return path.length > 0 && Buffer.byteLength(path, "utf8") <= MAX_SHARED_PATH_BYTES && SHARED_PATH.test(path);
}

/**
 * Reads what publish_file points at: one file, or a directory with index.html (a small site).
 * The path must stay inside the employee's working directory after resolving symlinks; hidden
 * files, node_modules and symlinks inside a directory are skipped.
 */
export async function prepareUpload(cwd: string, rawPath: unknown): Promise<PreparedUpload> {
  const { real: abs } = await resolveInside(cwd, rawPath, "publish_file only publishes files inside your working directory");
  const st = await lstat(abs);
  if (!st.isDirectory() && !st.isFile()) throw new UploadError(`${String(rawPath)}: not a regular file`);
  const picked = st.isDirectory() ? await walkSite(abs) : [checkFile({ path: basename(abs), abs })];
  checkSize((await Promise.all(picked.map((p) => lstat(p.abs)))).reduce((n, s) => n + s.size, 0), MAX_BYTES);
  const bodies = await readAll(picked, MAX_BYTES);
  return buildUpload(picked.map((p, i) => ({ path: p.path, body: bodies[i]!, contentType: contentType(p.path)! })));
}

/**
 * Reads the single file write_shared_file points at (args.file): a regular file inside the working
 * directory after resolving symlinks (directories are refused), any type, at most MAX_SHARED_BYTES.
 * The target path is args.path, or args.file normalised to a path relative to the working directory
 * with a leading .shared/ removed (a file fetched with read_shared_file goes back to the same path).
 */
export async function prepareSharedUpload(cwd: string, rawFile: unknown, rawTarget: unknown): Promise<PreparedUpload> {
  if (rawTarget !== undefined && rawTarget !== null && (typeof rawTarget !== "string" || !validSharedPath(rawTarget))) {
    throw new UploadError(`path ${JSON.stringify(rawTarget)} is not a valid shared path (${SHARED_PATH_RULES})`);
  }
  const { real, rel } = await resolveInside(cwd, rawFile, "write_shared_file only uploads files inside your working directory");
  const st = await lstat(real);
  if (st.isDirectory()) throw new UploadError(`${String(rawFile)} is a directory; write_shared_file uploads one file`);
  if (!st.isFile()) throw new UploadError(`${String(rawFile)}: not a regular file`);
  checkSize(st.size, MAX_SHARED_BYTES);
  const target = typeof rawTarget === "string" ? rawTarget : defaultSharedPath(rel);
  if (!validSharedPath(target)) {
    throw new UploadError(`the default shared path ${JSON.stringify(target)} (derived from file) is not valid (${SHARED_PATH_RULES}); pass path to choose a target`);
  }
  const [body] = await readAll([{ path: String(rawFile), abs: real }], MAX_SHARED_BYTES);
  return buildUpload([{ path: target, body: body!, contentType: contentType(target) ?? "application/octet-stream" }]);
}

/** The default target of write_shared_file: the file's path relative to cwd, without a leading .shared/. */
export function defaultSharedPath(rel: string): string {
  const parts = rel.split("/");
  return (parts[0] === SHARED_DIR && parts.length > 1 ? parts.slice(1) : parts).join("/");
}

/** The upload frame and its base64 chunks for files read into memory (all files concatenated, CHUNK_BYTES each). */
function buildUpload(entries: { path: string; body: Buffer; contentType: string }[]): PreparedUpload {
  const files: UploadFile[] = entries.map((e) => ({ path: e.path, size: e.body.length, sha256: sha256(e.body), content_type: e.contentType }));
  const chunks = chunk(Buffer.concat(entries.map((e) => e.body))).map((c) => c.toString("base64"));
  return { upload: { upload_id: newId("up_"), chunks: chunks.length, files: files as ToolUpload["files"] }, chunks };
}

/**
 * Resolves a path given by the employee: it must exist and stay inside the working directory after
 * resolving symlinks. Returns the real path and the normalised '/'-separated path relative to cwd
 * (lexical when the given path is inside cwd, so a symlink keeps its own name).
 */
async function resolveInside(cwd: string, rawPath: unknown, outside: string): Promise<{ real: string; rel: string }> {
  if (typeof rawPath !== "string" || !rawPath.trim() || rawPath.includes("\0")) throw new UploadError("path is required");
  const root = await realpath(cwd);
  const wanted = isAbsolute(rawPath) ? resolve(rawPath) : resolve(cwd, rawPath);
  const real = await realpath(wanted).catch(() => {
    throw new UploadError(`no such file: ${rawPath}`);
  });
  if (!isInside(root, real) || real === root) throw new UploadError(outside);
  const lexical = relative(resolve(cwd), wanted);
  const rel = lexical && isInside(resolve(cwd), wanted) ? lexical : relative(root, real);
  return { real, rel: rel.split(sep).join("/") };
}

/** Reads the picked files in order within `max` bytes in total (files may change after lstat). */
async function readAll(picked: Picked[], max: number): Promise<Buffer[]> {
  const bodies: Buffer[] = [];
  let total = 0;
  for (const p of picked) {
    const body = await readRegular(p, max - total, max);
    total += body.length;
    bodies.push(body);
  }
  return bodies;
}

/**
 * Reads one file without following a symlink swapped in after the path check and without
 * blocking on a FIFO (O_NOFOLLOW | O_NONBLOCK, then fstat must say regular file), and stops
 * once more than `budget` bytes arrive.
 */
async function readRegular(p: Picked, budget: number, max: number): Promise<Buffer> {
  const fh = await open(p.abs, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK).catch(() => {
    throw new UploadError(`${p.path}: cannot open (symlinks are not published)`);
  });
  try {
    if (!(await fh.stat()).isFile()) throw new UploadError(`${p.path}: not a regular file`);
    const parts: Buffer[] = [];
    let got = 0;
    for (;;) {
      const buf = Buffer.alloc(Math.min(CHUNK_BYTES, budget - got + 1));
      const { bytesRead } = await fh.read(buf, 0, buf.length, null);
      if (bytesRead === 0) break;
      parts.push(buf.subarray(0, bytesRead));
      got += bytesRead;
      if (got > budget) checkSize(max - budget + got, max);
    }
    return Buffer.concat(parts);
  } finally {
    await fh.close();
  }
}

function checkSize(total: number, max: number): void {
  if (total > max) throw new UploadError(`files add up to ${(total / 1048576).toFixed(1)} MB; the limit is ${max / 1048576} MB`);
}

function checkFile(p: Picked): Picked {
  if (!contentType(p.path)) throw new UploadError(`${p.path}: unsupported file type (html, md, svg, png, jpg, gif, webp, pdf, json, csv, txt; css, js and fonts inside a site)`);
  if (!p.path.split("/").every((part) => NAME.test(part))) throw new UploadError(`${p.path}: file names may not start with a dot or contain special characters`);
  return p;
}

async function walkSite(dir: string): Promise<Picked[]> {
  const out: Picked[] = [];
  await walk(dir, dir, out);
  if (!out.some((p) => p.path === "index.html")) throw new UploadError("a published directory needs an index.html at its top");
  return out.sort((a, b) => a.path.localeCompare(b.path));
}

async function walk(root: string, dir: string, out: Picked[]): Promise<void> {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    if (entry.name.startsWith(".") || SKIP_DIRS.has(entry.name) || entry.isSymbolicLink()) continue;
    const abs = join(dir, entry.name);
    if (entry.isDirectory()) {
      await walk(root, abs, out);
      continue;
    }
    if (!entry.isFile()) continue;
    if (out.length >= MAX_FILES) throw new UploadError(`a published directory may hold at most ${MAX_FILES} files`);
    out.push(checkFile({ path: relative(root, abs).split(sep).join("/"), abs }));
  }
}

function sha256(b: Buffer): string {
  return createHash("sha256").update(b).digest("hex");
}

function chunk(all: Buffer): Buffer[] {
  const out: Buffer[] = [];
  for (let off = 0; off < all.length; off += CHUNK_BYTES) out.push(all.subarray(off, off + CHUNK_BYTES));
  return out;
}
