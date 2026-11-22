// Where the runtimes keep a conversation on disk, for session moves (protocol.md §4.11).
import { existsSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

/** Claude Code's projects/<dir> for a cwd: every non-alphanumeric character becomes "-", over 200 a hash suffix (CLI rule). */
export function claudeProjectDirName(absCwd: string): string {
  const s = absCwd.replace(/[^a-zA-Z0-9]/g, "-");
  if (s.length <= 200) return s;
  return `${s.slice(0, 200)}-${Math.abs(javaHash(absCwd)).toString(36)}`;
}

function javaHash(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  return h;
}

/** The Codex rollout of a thread: $CODEX_HOME/{sessions,archived_sessions}/…/rollout-…-<thread id>.jsonl. */
export function findRollout(codexHome: string, threadId: string): string | null {
  const suffix = `-${threadId}.jsonl`;
  for (const top of ["sessions", "archived_sessions"]) {
    const hit = findFile(join(codexHome, top), (name) => name.startsWith("rollout-") && name.endsWith(suffix), 5);
    if (hit) return hit;
  }
  return null;
}

function findFile(dir: string, match: (name: string) => boolean, depth: number): string | null {
  if (depth < 0 || !existsSync(dir)) return null;
  // Newest first: the date directories sort by name.
  for (const ent of readdirSync(dir, { withFileTypes: true }).sort((a, b) => b.name.localeCompare(a.name))) {
    const p = join(dir, ent.name);
    if (ent.isFile() && match(ent.name)) return p;
    if (ent.isDirectory()) {
      const hit = findFile(p, match, depth - 1);
      if (hit) return hit;
    }
  }
  return null;
}

/** Regular files under dir as paths relative to it ("/"-separated), skipping symlinks. */
export function listFiles(dir: string): string[] {
  if (!existsSync(dir) || !statSync(dir).isDirectory()) return [];
  const out: string[] = [];
  const walk = (d: string) => {
    for (const ent of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, ent.name);
      if (ent.isDirectory()) walk(p);
      else if (ent.isFile()) out.push(relative(dir, p).split("\\").join("/"));
    }
  };
  walk(dir);
  return out.sort();
}

/**
 * Rewrites the source node's absolute paths (its root, Claude config dir, CODEX_HOME) to this
 * node's in a transcript, longest first so a path inside another is not cut in half.
 */
export function rewritePaths(text: string, pairs: Array<[from: string, to: string]>): string {
  const useful = pairs.filter(([from, to]) => from && from !== to).sort((a, b) => b[0].length - a[0].length);
  if (useful.length === 0) return text;
  const to = new Map(useful);
  // One pass: a replacement is never rewritten again by a shorter pair.
  const re = new RegExp(useful.map(([from]) => from.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|"), "g");
  return text.replace(re, (m) => to.get(m) ?? m);
}
