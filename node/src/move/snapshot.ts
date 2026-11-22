// Worktree snapshots for session moves (protocol.md §4.11): everything an employee has in a checkout
// that the other node cannot fetch (local commits, staged / unstaged / untracked files) as one git
// bundle, restored on the other node without touching what the checkout already shares with origin.
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { gitEnv, run, type RunResult } from "../git/exec.js";
import { untrustedRepoArgs } from "../git/safe.js";

export interface Snapshot {
  /** The checkout's HEAD. */
  head: string;
  /** A commit on top of HEAD whose tree is the working tree (untracked files included, ignored ones not). */
  snapshot: string;
  bundle: Buffer;
}

export class SnapshotError extends Error {}

const IDENTITY = { GIT_AUTHOR_NAME: "Agent Teams", GIT_AUTHOR_EMAIL: "agent-teams@localhost", GIT_COMMITTER_NAME: "Agent Teams", GIT_COMMITTER_EMAIL: "agent-teams@localhost" };

/**
 * Snapshots a checkout without changing it: a scratch index gets HEAD plus `git add -A`, its tree
 * becomes a commit on HEAD, and the bundle holds that commit minus whatever any remote-tracking
 * ref already has (the other node fetched those). The temporary ref is removed again.
 */
export async function snapshotRepo(cwd: string, ref: string, hooksDir: string, tmpDir: string): Promise<Snapshot> {
  const safe = await untrustedRepoArgs(cwd, hooksDir);
  const scratch = mkdtempSync(join(tmpDir, "at-move-"));
  const index = join(scratch, "index");
  const git = (args: string[], extra: Record<string, string> = {}) => run("git", [...safe, "-C", cwd, ...args], { env: gitEnv({ GIT_INDEX_FILE: index, ...extra }), timeoutMs: 10 * 60_000 });
  try {
    const head = out(await git(["rev-parse", "--verify", "HEAD"]), "no HEAD");
    must(await git(["read-tree", "HEAD"]), "read-tree");
    must(await git(["add", "-A", "--", "."]), "add");
    const tree = out(await git(["write-tree"]), "write-tree");
    const snapshot = out(await git(["commit-tree", tree, "-p", head, "-m", "agent-teams: session move snapshot"], IDENTITY), "commit-tree");
    must(await git(["update-ref", ref, snapshot]), "update-ref");
    try {
      const file = join(scratch, "snapshot.bundle");
      must(await git(["bundle", "create", file, ref, "--not", "--remotes"]), "bundle create");
      return { head, snapshot, bundle: readFileSync(file) };
    } finally {
      await git(["update-ref", "-d", ref]);
    }
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

/**
 * Puts a snapshot into a checkout on this node: the bundle's objects go into its repository (its
 * prerequisites must be there: the checkout was just prepared and fetched), the branch moves to
 * the source's HEAD and the working tree to the snapshot, with the index at HEAD (nothing staged).
 */
export async function restoreRepo(cwd: string, bundlePath: string, snap: Pick<Snapshot, "head" | "snapshot">, hooksDir: string): Promise<void> {
  const safe = await untrustedRepoArgs(cwd, hooksDir);
  const git = (args: string[]) => run("git", [...safe, "-C", cwd, ...args], { env: gitEnv(), timeoutMs: 10 * 60_000 });
  must(await git(["bundle", "verify", "--quiet", bundlePath]), "bundle verify (commits missing here)");
  must(await git(["bundle", "unbundle", bundlePath]), "bundle unbundle");
  const parent = out(await git(["rev-parse", "--verify", `${snap.snapshot}^`]), "snapshot parent");
  if (parent !== snap.head) throw new SnapshotError(`snapshot ${snap.snapshot} is not on ${snap.head}`);
  must(await git(["reset", "--hard", "--quiet", snap.snapshot]), "reset to snapshot");
  must(await git(["reset", "--mixed", "--quiet", snap.head]), "reset index to HEAD");
}

function must(r: RunResult, what: string): void {
  if (r.code !== 0) throw new SnapshotError(`git ${what} failed: ${(r.stderr || r.stdout).trim().slice(0, 500)}`);
}

function out(r: RunResult, what: string): string {
  must(r, what);
  return r.stdout.trim();
}
