import { constants } from "node:fs";
import { createHash } from "node:crypto";
import { lstat, mkdir, open, realpath, rm } from "node:fs/promises";
import { join } from "node:path";
import type { ToolDownload } from "../protocol.js";
import { MAX_BYTES, MAX_FILES, validFilePath } from "./upload.js";

/** Folder (relative to the session's working directory) read_deliverable writes into; git-ignored by its own .gitignore. */
export const DOWNLOAD_DIR = ".agent-deliverables";
const DELIVERABLE_ID = /^dl_[A-Za-z0-9]+$/;
const GITIGNORE = "# Written by agent-teams read_deliverable; never committed.\n*\n";

export class DownloadError extends Error {}

export interface WrittenDownload {
  /** Absolute folder of the version. */
  abs: string;
  /** The same folder relative to the working directory. */
  rel: string;
  files: string[];
}

/**
 * Writes the files of a read_deliverable result under <cwd>/.agent-deliverables/<id>/v<N>/.
 * Everything the console sent is checked again before a byte lands on disk: the id and version
 * that make up the folder, every file path (relative, no dot segments, allowed extensions), the
 * chunk count, the total size and each checksum. The folders must be real directories (never
 * symlinks), the version folder is replaced, and files are created exclusively without following
 * links.
 */
export async function writeDownload(cwd: string, dl: ToolDownload, chunks: ReadonlyMap<number, string>): Promise<WrittenDownload> {
  checkMeta(dl);
  const bodies = splitFiles(dl, joinChunks(dl.chunks, chunks, MAX_BYTES, "read_deliverable"));
  const root = await realpath(cwd).catch(() => {
    throw new DownloadError("the working directory is missing");
  });
  const top = await ensureDir(join(root, DOWNLOAD_DIR));
  await writeNew(join(top, ".gitignore"), Buffer.from(GITIGNORE), true);
  const idDir = await ensureDir(join(top, dl.deliverable_id));
  const rel = `${DOWNLOAD_DIR}/${dl.deliverable_id}/v${dl.version}`;
  const verDir = join(idDir, `v${dl.version}`);
  await rm(verDir, { recursive: true, force: true });
  await mkdir(verDir, { mode: 0o755 });
  for (const [i, f] of dl.files.entries()) {
    const parts = f.path.split("/");
    let dir = verDir;
    for (const part of parts.slice(0, -1)) dir = await ensureDir(join(dir, part));
    await writeNew(join(dir, parts.at(-1)!), bodies[i]!, false);
  }
  return { abs: verDir, rel, files: dl.files.map((f) => f.path) };
}

function checkMeta(dl: ToolDownload): void {
  if (!DELIVERABLE_ID.test(dl.deliverable_id) || !Number.isInteger(dl.version) || dl.version < 1) throw new DownloadError("bad deliverable id or version");
  if (!dl.files.length || dl.files.length > MAX_FILES) throw new DownloadError(`a deliverable has 1 to ${MAX_FILES} files`);
  const seen = new Set<string>();
  for (const f of dl.files) {
    if (!validFilePath(f.path) || seen.has(f.path)) throw new DownloadError(`refusing file name ${JSON.stringify(f.path)}`);
    seen.add(f.path);
    if (!Number.isInteger(f.size) || f.size < 0) throw new DownloadError(`bad size of ${f.path}`);
  }
}

function joinChunks(count: number, chunks: ReadonlyMap<number, string>, max: number, tool: string): Buffer {
  if (!Number.isInteger(count) || count < 0) throw new DownloadError("bad chunk count");
  const parts: Buffer[] = [];
  let total = 0;
  for (let i = 0; i < count; i++) {
    const data = chunks.get(i);
    if (data === undefined) throw new DownloadError(`file content incomplete (${chunks.size}/${count} pieces arrived); call ${tool} again`);
    const buf = Buffer.from(data, "base64");
    total += buf.length;
    if (total > max) throw new DownloadError(`files exceed ${max / 1048576} MB`);
    parts.push(buf);
  }
  return Buffer.concat(parts);
}

function splitFiles(dl: { files: { path: string; size: number; sha256: string }[] }, all: Buffer): Buffer[] {
  const declared = dl.files.reduce((n, f) => n + f.size, 0);
  if (declared !== all.length) throw new DownloadError("file sizes do not match the content");
  let off = 0;
  return dl.files.map((f) => {
    const body = all.subarray(off, off + f.size);
    off += f.size;
    if (f.sha256 && createHash("sha256").update(body).digest("hex") !== f.sha256) throw new DownloadError(`checksum mismatch: ${f.path}`);
    return body;
  });
}

/** Creates the directory if needed; an existing entry must be a real directory, not a symlink or file. */
export async function ensureDir(path: string): Promise<string> {
  const st = await lstat(path).catch(() => null);
  if (!st) {
    await mkdir(path, { mode: 0o755 });
    return path;
  }
  if (st.isSymbolicLink() || !st.isDirectory()) throw new DownloadError(`${path} exists and is not a directory`);
  return path;
}

/** Writes a file without following a symlink at its name (replace: truncate an existing regular file). */
export async function writeNew(path: string, body: Buffer, replace: boolean): Promise<void> {
  const flags = constants.O_WRONLY | constants.O_CREAT | constants.O_NOFOLLOW | (replace ? constants.O_TRUNC : constants.O_EXCL);
  const fh = await open(path, flags, 0o644).catch((err: NodeJS.ErrnoException) => {
    throw new DownloadError(`cannot write ${path}: ${err.code ?? err.message}`);
  });
  try {
    await fh.writeFile(body);
  } finally {
    await fh.close();
  }
}
