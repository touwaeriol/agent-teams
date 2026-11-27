import { lstatSync, mkdirSync, readlinkSync, realpathSync, rmSync, symlinkSync, unlinkSync } from "node:fs";
import { dirname, join } from "node:path";
import type { SessionOpen } from "../protocol.js";
import { isInside } from "../util/paths.js";

/** The names of the links a working directory gets into the shared folder. */
const LINK_NAMES = [".project-files", ".user-files", ".attachments"];

export interface SharedDirs {
  project: string;
  shared: string;
}

/**
 * The session spec with the shared-folder directories writable: the Claude Bash sandbox
 * (extra_write_paths) and Codex workspace-write (sandbox_workspace_write.writable_roots).
 * Claude's file tools get them as additionalDirectories (DriverContext.extraDirs). The
 * original spec is not changed.
 */
export function withSharedDirs(spec: SessionOpen, dirs: SharedDirs): SessionOpen {
  const add = [dirs.project, dirs.shared];
  const out: SessionOpen = { ...spec };
  if (spec.claude?.sandbox?.enabled && !spec.claude.read_only) {
    const sb = spec.claude.sandbox;
    out.claude = { ...spec.claude, sandbox: { ...sb, extra_write_paths: [...(sb.extra_write_paths ?? []), ...add] } };
  }
  if (spec.codex && spec.codex.sandbox === "workspace-write") {
    const config = (spec.codex.config ?? {}) as Record<string, unknown>;
    const ws = (config.sandbox_workspace_write ?? {}) as { writable_roots?: string[] };
    out.codex = { ...spec.codex, config: { ...config, sandbox_workspace_write: { ...ws, writable_roots: [...(ws.writable_roots ?? []), ...add] } } };
  }
  return out;
}

/** Where attachments of a session with the shared folder are kept: the project's attachments/. */
export function sharedAttachmentDir(dirs: SharedDirs): string {
  return join(dirs.project, "attachments");
}

/** The links a working directory gets, by name, to the shared folder. */
export function sharedLinks(dirs: SharedDirs): [string, string][] {
  return [
    [".project-files", dirs.project],
    [".user-files", dirs.shared],
    [".attachments", sharedAttachmentDir(dirs)],
  ];
}

/**
 * Puts the links into the working directory (not a git repository: the repositories are folders
 * inside it). A link to elsewhere is replaced; an old local .attachments folder (downloaded copies,
 * fetched again on demand) is removed; any other file in the way is left alone and reported.
 */
export function linkSharedDirs(absCwd: string, dirs: SharedDirs): string[] {
  const problems: string[] = [];
  mkdirSync(sharedAttachmentDir(dirs), { recursive: true });
  for (const [name, target] of sharedLinks(dirs)) {
    const at = join(absCwd, name);
    const st = lstatOrNull(at);
    if (st?.isSymbolicLink()) {
      if (readlinkSync(at) === target) continue;
      unlinkSync(at);
    } else if (st?.isDirectory() && name === ".attachments") {
      rmSync(at, { recursive: true, force: true });
    } else if (st) {
      problems.push(`${at} exists and is not a link`);
      continue;
    }
    symlinkSync(target, at, "dir");
  }
  return problems;
}

/**
 * Removes the shared-folder links from a working directory whose session has no folder (off, not
 * mounted here): only our own symlinks by name, never a file or folder the employee made.
 */
export function unlinkSharedDirs(absCwd: string): void {
  for (const name of LINK_NAMES) {
    const at = join(absCwd, name);
    if (lstatOrNull(at)?.isSymbolicLink()) unlinkSync(at);
  }
}

/**
 * The attachments directory is safe to download into: a real directory (not a symlink an employee
 * swapped in) whose real path stays inside the volume root.
 */
export function safeAttachmentDir(dir: string, volumeRoot: string): boolean {
  if (!lstatOrNull(dir)?.isDirectory()) return false;
  try {
    return isInside(realpathSync(volumeRoot), realpathSync(dir));
  } catch {
    return false;
  }
}

function lstatOrNull(p: string) {
  try {
    return lstatSync(p);
  } catch {
    return null;
  }
}

/** The memory directory's name inside the project's and the user's folder. */
export const MEMORY_DIR = ".memory";

/** The project's and the user's memory directory. */
export function memoryDirs(dirs: SharedDirs): [string, string] {
  return [join(dirs.project, MEMORY_DIR), join(dirs.shared, MEMORY_DIR)];
}

/**
 * Creates the memory directories when missing and returns their real paths; throws when one cannot
 * be made or is not a plain directory of its folder (anyone writing the folder could make .memory a
 * link to elsewhere, which would open that place to the sessions allowed into the memory).
 */
export function ensureMemoryDirs(dirs: SharedDirs): string[] {
  return memoryDirs(dirs).map((d, i) => {
    mkdirSync(d, { recursive: true });
    const parent = realpathSync(i === 0 ? dirs.project : dirs.shared);
    if (lstatSync(d).isSymbolicLink()) throw new Error(`${d} is a symbolic link`);
    const real = realpathSync(d);
    if (dirname(real) !== parent) throw new Error(`${d} is not a directory of its folder`);
    return real;
  });
}

/** What a session with only the memory directories (the coordinator) is told about them. */
export function memoryFolderPrompt(dirs: SharedDirs): string {
  const [project, user] = sharedLinks(dirs);
  const [pm, um] = memoryDirs(dirs);
  return [
    "## Memory files",
    `The memory is files in the user's shared folder: the project's in ${project[0]}/${MEMORY_DIR}/ (${pm}), the user's own in ${user[0]}/${MEMORY_DIR}/ (${um}). Read, Glob, Grep, Edit and Write work there and nowhere else; give Glob and Grep the directory as path.`,
  ].join("\n");
}

/** What a read-only session with the whole folder is told about the one place it may write. */
export function memoryWritePrompt(dirs: SharedDirs): string {
  const [project, user] = sharedLinks(dirs);
  return `The memory files are the one thing you may change: Edit and Write work in ${project[0]}/${MEMORY_DIR}/ and ${user[0]}/${MEMORY_DIR}/ and nowhere else.`;
}

/** What the employee is told about the folders (appended to the system prompt). */
export function sharedFolderPrompt(dirs: SharedDirs): string {
  return [
    "## Shared folder",
    "Your working directory holds the project's repositories (code: commit and push it, anything left uncommitted on this node is lost) and three links into the user's shared folder, a network file system mounted on every node of this user that survives sessions:",
    `- .project-files/ → ${dirs.project} ($AGENT_TEAMS_PROJECT_DIR): this project's files. Put documents, data, reports, exports and anything other employees, threads or later sessions of the project need here, never in a repository unless it belongs to the code. The user sees these files on the project's 共享文件 page.`,
    `- .user-files/ → ${dirs.shared} ($AGENT_TEAMS_SHARED_DIR): files shared by all of the user's projects.`,
    `- .attachments/ → ${sharedAttachmentDir(dirs)}: files the user attached to messages (kept with the project).`,
    "- .project-files/.deliverables/<id>/v<N>/ holds the published versions of the project's deliverables. The platform keeps it: read from it freely, but do not write, move or delete there (whatever is there is what gets served). Publish with publish_file and fetch with read_deliverable as usual.",
    "Do not put secrets (keys, passwords, tokens) there. Several nodes may use the folder at once: a SQLite database there must be written from one session at a time (rollback journal, not WAL).",
  ].join("\n");
}
