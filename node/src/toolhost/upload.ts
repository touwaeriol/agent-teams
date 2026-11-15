import { createHash } from "node:crypto";
import { lstat, readdir, readFile, realpath } from "node:fs/promises";
import { basename, extname, isAbsolute, join, relative, resolve, sep } from "node:path";
import type { ToolUpload, UploadFile } from "../protocol.js";
import { newId } from "../util/ids.js";
import { isInside } from "../util/paths.js";

/** Limits of publish_file (the console checks them again). */
export const MAX_BYTES = 5 * 1024 * 1024;
export const MAX_FILES = 64;
export const CHUNK_BYTES = 1024 * 1024;

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

/**
 * Reads what publish_file points at: one file, or a directory with index.html (a small site).
 * The path must stay inside the employee's working directory after resolving symlinks; hidden
 * files, node_modules and symlinks inside a directory are skipped.
 */
export async function prepareUpload(cwd: string, rawPath: unknown): Promise<PreparedUpload> {
  const abs = await resolveInside(cwd, rawPath);
  const st = await lstat(abs);
  const picked = st.isDirectory() ? await walkSite(abs) : [checkFile({ path: basename(abs), abs })];
  checkSize((await Promise.all(picked.map((p) => lstat(p.abs)))).reduce((n, s) => n + s.size, 0));
  const bodies = await Promise.all(picked.map((p) => readFile(p.abs)));
  checkSize(bodies.reduce((n, b) => n + b.length, 0));
  const files: UploadFile[] = picked.map((p, i) => ({ path: p.path, size: bodies[i]!.length, sha256: sha256(bodies[i]!), content_type: contentType(p.path)! }));
  const chunks = chunk(Buffer.concat(bodies)).map((c) => c.toString("base64"));
  return { upload: { upload_id: newId("up_"), chunks: chunks.length, files: files as ToolUpload["files"] }, chunks };
}

async function resolveInside(cwd: string, rawPath: unknown): Promise<string> {
  if (typeof rawPath !== "string" || !rawPath.trim() || rawPath.includes("\0")) throw new UploadError("path is required");
  const root = await realpath(cwd);
  const wanted = isAbsolute(rawPath) ? resolve(rawPath) : resolve(cwd, rawPath);
  const real = await realpath(wanted).catch(() => {
    throw new UploadError(`no such file: ${rawPath}`);
  });
  if (!isInside(root, real) || real === root) throw new UploadError("publish_file only publishes files inside your working directory");
  return real;
}

function checkSize(total: number): void {
  if (total > MAX_BYTES) throw new UploadError(`files add up to ${(total / 1048576).toFixed(1)} MB; the limit is ${MAX_BYTES / 1048576} MB`);
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
