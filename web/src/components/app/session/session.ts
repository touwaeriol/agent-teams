// 会话 (multi-repository sessions): a project of kind session where one employee works in a folder
// holding every chosen repository side by side, and the user talks to it directly.
import type { ProjectView } from "@/api/views";
import { normalizeRepo, type RepoRef } from "../repo/repo";

/** A repository of a session with the branch its work branch is cut from ("" = default branch). */
export interface SessionRepo extends RepoRef {
  branch: string;
}

export const isSession = (p: Pick<ProjectView, "kind"> | undefined | null) => p?.kind === "session";

/** Where a sidebar row goes: a session opens its conversation, a project its board. */
export const homeOf = (p: Pick<ProjectView, "id" | "kind">) => (isSession(p) ? `/sessions/${p.id}` : `/projects/${p.id}`);

/** The folder a repository is checked out to inside the session's directory (ghops.Dir). */
export function repoDir(repo: string): string {
  return repo.replace(/\//g, "__").replace(/:/g, "_");
}

/** The repos field of POST /api/sessions and PUT /api/sessions/{pid}/repos. */
export function sessionReposPayload(list: SessionRepo[]) {
  return list
    .map((r) => {
      const branch = r.branch.trim() || undefined;
      return r.git_repo_id ? { repo: r.repo, git_connection_id: null, git_repo_id: r.git_repo_id, branch } : { repo: normalizeRepo(r.repo), git_connection_id: r.git_connection_id, branch };
    })
    .filter((r) => r.repo && (r.git_connection_id || ("git_repo_id" in r && r.git_repo_id)));
}

/** Keeps the branches already picked when the repository list changes (new ones start on the default branch). */
export function withBranches(list: RepoRef[], prev: SessionRepo[]): SessionRepo[] {
  return list.map((r) => ({ ...r, branch: prev.find((p) => p.repo.toLowerCase() === r.repo.toLowerCase())?.branch ?? "" }));
}
