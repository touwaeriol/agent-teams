import { closeSync, mkdirSync, openSync, rmSync, statSync } from "node:fs";
import { dirname } from "node:path";
import { sleep } from "../util/async.js";

export class LockTimeout extends Error {
  readonly code = "locked_timeout";
}

const STALE_MS = 15 * 60_000;
const inProcess = new Map<string, Promise<void>>();

/** Per-path mutex: in-process queue plus an O_EXCL lock file shared with other processes. */
export async function withLock<T>(path: string, fn: () => Promise<T>, timeoutMs = 5 * 60_000): Promise<T> {
  const prev = inProcess.get(path) ?? Promise.resolve();
  let release!: () => void;
  const mine = new Promise<void>((r) => (release = r));
  inProcess.set(path, prev.then(() => mine));
  await prev;
  try {
    await acquireFile(path, timeoutMs);
    try {
      return await fn();
    } finally {
      rmSync(path, { force: true });
    }
  } finally {
    release();
    if (inProcess.get(path) === mine) inProcess.delete(path);
  }
}

async function acquireFile(path: string, timeoutMs: number): Promise<void> {
  mkdirSync(dirname(path), { recursive: true });
  const deadline = Date.now() + timeoutMs;
  while (!tryCreate(path)) {
    if (Date.now() > deadline) throw new LockTimeout(`timed out waiting for ${path}`);
    await sleep(200);
  }
}

function tryCreate(path: string): boolean {
  try {
    closeSync(openSync(path, "wx", 0o600));
    return true;
  } catch {
    removeIfStale(path);
    return false;
  }
}

function removeIfStale(path: string): void {
  try {
    if (Date.now() - statSync(path).mtimeMs > STALE_MS) rmSync(path, { force: true });
  } catch {
    // vanished in between
  }
}
