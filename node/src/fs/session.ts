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

/** What the employee is told about the folders (appended to the system prompt). */
export function sharedFolderPrompt(dirs: SharedDirs): string {
  return [
    "## Shared folder",
    `This project's persistent folder is ${dirs.project} ($AGENT_TEAMS_PROJECT_DIR); the user's folder shared by all their projects is ${dirs.shared} ($AGENT_TEAMS_SHARED_DIR).`,
    "Both are a network file system mounted on every node of this user and survive the session: put files there that other employees, threads or later sessions need. Code still lives in the git worktree.",
    "Several nodes may use the folders at once: a SQLite database there must be written from one session at a time (rollback journal, not WAL).",
  ].join("\n");
}
