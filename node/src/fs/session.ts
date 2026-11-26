import { lstatSync, mkdirSync, readlinkSync, rmSync, symlinkSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import type { SessionOpen } from "../protocol.js";

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

function lstatOrNull(p: string) {
  try {
    return lstatSync(p);
  } catch {
    return null;
  }
}

/** What the employee is told about the folders (appended to the system prompt). */
export function sharedFolderPrompt(dirs: SharedDirs): string {
  return [
    "## Shared folder",
    "Your working directory holds the project's repositories (code: commit and push it, anything left uncommitted on this node is lost) and three links into the user's shared folder, a network file system mounted on every node of this user that survives sessions:",
    `- .project-files/ → ${dirs.project} ($AGENT_TEAMS_PROJECT_DIR): this project's files. Put documents, data, reports, exports and anything other employees, threads or later sessions of the project need here, never in a repository unless it belongs to the code. The user sees these files on the project's 共享文件 page.`,
    `- .user-files/ → ${dirs.shared} ($AGENT_TEAMS_SHARED_DIR): files shared by all of the user's projects.`,
    `- .attachments/ → ${sharedAttachmentDir(dirs)}: files the user attached to messages (kept with the project).`,
    "Do not put secrets (keys, passwords, tokens) there. Several nodes may use the folder at once: a SQLite database there must be written from one session at a time (rollback journal, not WAL).",
  ].join("\n");
}
