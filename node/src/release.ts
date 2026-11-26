// Release layout shared by the daemon (node.upgrade) and the launcher (architecture.md §14.3):
//
//   <data>/releases/<id>/   one unpacked release each (id = <version>+<build>)
//   <data>/current          symlink → releases/<id>, the release the launcher starts
//   <data>/previous         symlink → the release before it (rollback target)
//   <data>/upgrade.json     the state of the last update (switched → confirmed | rolled_back)
//   <data>/bin/launch.mjs   the launcher (systemd); the container image runs its own copy
//
// Only node: builtins here: the launcher bundles this file and must run without node_modules.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readlinkSync, renameSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { basename, join } from "node:path";

export const RELEASE_ID_RE = /^\d+\.\d+\.\d+(\+[0-9a-f]{8,16})?$/;
/** A switched release that is not confirmed after this many starts, or this long, is rolled back. */
export const MAX_UNCONFIRMED_BOOTS = 3;
export const CONFIRM_DEADLINE_MS = 5 * 60_000;
/** The daemon's exit code when the external shared-folder mount went away: the launcher exits with it too, so the container restarts and the volume plugin mounts again. */
export const EXIT_FS_LOST = 76;

export type UpgradeStateName = "switched" | "confirmed" | "rolled_back";

export interface UpgradeState {
  state: UpgradeStateName;
  from: string;
  to: string;
  /** Launcher starts since the switch (switched only). */
  boots: number;
  switched_at: number;
  error?: string;
  /** The daemon told the console about this outcome. */
  reported?: boolean;
}

export function defaultDataDir(env: NodeJS.ProcessEnv = process.env): string {
  return env.AGENT_TEAMS_DATA ?? join(env.XDG_DATA_HOME ?? join(homedir(), ".local", "share"), "agent-teams");
}

/** First 8 hex of sha256(node.mjs): the build part of the release id. */
export function buildOf(file: string): string {
  return createHash("sha256").update(readFileSync(file)).digest("hex").slice(0, 8);
}

export function releaseId(version: string, build: string): string {
  return build ? `${version}+${build}` : version;
}

export class ReleaseLayout {
  constructor(readonly data: string) {}

  get releases(): string {
    return join(this.data, "releases");
  }
  get statePath(): string {
    return join(this.data, "upgrade.json");
  }
  dir(id: string): string {
    return join(this.releases, id);
  }

  /** The release id a link points at (releases/<id> → <id>; an old node-<v> dir keeps its name). */
  target(link: "current" | "previous"): string | null {
    try {
      return basename(readlinkSync(join(this.data, link)));
    } catch {
      return null;
    }
  }

  /** Absolute directory behind a link, or null. */
  resolve(link: "current" | "previous"): string | null {
    try {
      const t = readlinkSync(join(this.data, link));
      return t.startsWith("/") ? t : join(this.data, t);
    } catch {
      return null;
    }
  }

  /** Points link at a path relative to the data dir, atomically (symlink to a temp name, then rename). */
  point(link: "current" | "previous", relTarget: string): void {
    const path = join(this.data, link);
    const tmp = `${path}.tmp-${process.pid}`;
    rmSync(tmp, { force: true });
    symlinkSync(relTarget, tmp);
    renameSync(tmp, path);
  }

  rawTarget(link: "current" | "previous"): string | null {
    try {
      return readlinkSync(join(this.data, link));
    } catch {
      return null;
    }
  }

  /** previous ← current, current ← releases/<id>. */
  switchTo(id: string): void {
    const old = this.rawTarget("current");
    if (old && basename(old) !== id) this.point("previous", old);
    this.point("current", join("releases", id));
  }

  /** current ↔ previous; false when there is no previous release. */
  swapBack(): boolean {
    const prev = this.rawTarget("previous");
    const cur = this.rawTarget("current");
    if (!prev || !existsSync(join(this.data, prev))) return false;
    this.point("current", prev);
    if (cur) this.point("previous", cur);
    return true;
  }

  readState(): UpgradeState | null {
    try {
      return JSON.parse(readFileSync(this.statePath, "utf8")) as UpgradeState;
    } catch {
      return null;
    }
  }

  writeState(s: UpgradeState): void {
    mkdirSync(this.data, { recursive: true });
    const tmp = `${this.statePath}.tmp-${process.pid}`;
    writeFileSync(tmp, JSON.stringify(s, null, 2) + "\n");
    renameSync(tmp, this.statePath);
  }
}
