import { appendFileSync, existsSync, readFileSync, renameSync, writeFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

/** Reads a JSONL file; a torn last line (crash mid-write) is skipped. */
export function readJsonl<T>(path: string): T[] {
  if (!existsSync(path)) return [];
  const out: T[] = [];
  for (const line of readFileSync(path, "utf8").split("\n")) {
    const parsed = parseLine<T>(line);
    if (parsed !== undefined) out.push(parsed);
  }
  return out;
}

function parseLine<T>(line: string): T | undefined {
  if (!line.trim()) return undefined;
  try {
    return JSON.parse(line) as T;
  } catch {
    return undefined;
  }
}

export function appendJsonl(path: string, value: unknown): void {
  appendFileSync(path, JSON.stringify(value) + "\n", { mode: 0o600 });
}

/** Atomic rewrite (write temp + rename). */
export function writeJsonlAtomic(path: string, values: unknown[]): void {
  const body = values.map((v) => JSON.stringify(v) + "\n").join("");
  writeFileAtomic(path, body);
}

export function writeFileAtomic(path: string, body: string): void {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const tmp = `${path}.tmp-${process.pid}`;
  writeFileSync(tmp, body, { mode: 0o600 });
  renameSync(tmp, path);
}

export function readJsonFile<T>(path: string, fallback: T): T {
  if (!existsSync(path)) return fallback;
  try {
    return JSON.parse(readFileSync(path, "utf8")) as T;
  } catch {
    return fallback;
  }
}
