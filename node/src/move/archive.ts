// The session-move archive (protocol.md §4.11): a gzip of named entries, written by session.export
// and read back by session.open.import. Entry names are relative, "/"-separated and never climb out.
import { gunzipSync, gzipSync } from "node:zlib";

const MAGIC = Buffer.from("AT-MOVE1\n");

/** Largest archive either side handles (compressed), and the most it may unpack to. */
export const MAX_ARCHIVE_BYTES = 128 * 1024 * 1024;
export const MAX_UNPACKED_BYTES = 512 * 1024 * 1024;

export interface Entry {
  name: string;
  data: Buffer;
}

export class ArchiveError extends Error {}

/** gzip(MAGIC, then per entry: u32 name length, name, u32 data length, data). */
export function pack(entries: Entry[]): Buffer {
  const parts: Buffer[] = [MAGIC];
  let raw = MAGIC.length;
  for (const e of entries) {
    checkName(e.name);
    const name = Buffer.from(e.name, "utf8");
    const head = Buffer.alloc(8);
    head.writeUInt32BE(name.length, 0);
    head.writeUInt32BE(e.data.length, 4);
    parts.push(head.subarray(0, 4), name, head.subarray(4), e.data);
    raw += 8 + name.length + e.data.length;
    if (raw > MAX_UNPACKED_BYTES) throw new ArchiveError(`the session is too large to move (over ${MAX_UNPACKED_BYTES >> 20} MB)`);
  }
  const out = gzipSync(Buffer.concat(parts), { level: 6 });
  if (out.length > MAX_ARCHIVE_BYTES) throw new ArchiveError(`the session archive is too large to move (${out.length >> 20} MB)`);
  return out;
}

export function unpack(archive: Buffer): Entry[] {
  let raw: Buffer;
  try {
    raw = gunzipSync(archive, { maxOutputLength: MAX_UNPACKED_BYTES });
  } catch (err) {
    throw new ArchiveError(`archive is not readable: ${(err as Error).message}`);
  }
  if (!raw.subarray(0, MAGIC.length).equals(MAGIC)) throw new ArchiveError("archive has no AT-MOVE1 header");
  const out: Entry[] = [];
  const seen = new Set<string>();
  let at = MAGIC.length;
  while (at < raw.length) {
    const nameLen = u32(raw, at);
    const name = raw.subarray(at + 4, at + 4 + nameLen).toString("utf8");
    const dataLen = u32(raw, at + 4 + nameLen);
    const start = at + 8 + nameLen;
    if (start + dataLen > raw.length) throw new ArchiveError("archive is truncated");
    checkName(name);
    if (seen.has(name)) throw new ArchiveError(`archive has ${name} twice`);
    seen.add(name);
    out.push({ name, data: raw.subarray(start, start + dataLen) });
    at = start + dataLen;
  }
  return out;
}

function u32(buf: Buffer, at: number): number {
  if (at + 4 > buf.length) throw new ArchiveError("archive is truncated");
  return buf.readUInt32BE(at);
}

/** Relative, "/"-separated, no empty / "." / ".." segment, no backslash or NUL. */
export function checkName(name: string): void {
  const bad = !name || name.length > 4096 || name.startsWith("/") || /[\\\0]/.test(name) || name.split("/").some((s) => s === "" || s === "." || s === "..");
  if (bad) throw new ArchiveError(`bad archive entry name: ${JSON.stringify(name.slice(0, 200))}`);
}
