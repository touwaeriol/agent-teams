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

/** Thread wording said of a session, where the one employee is talked to directly. */
export function sessionText(text: string): string {
  return text.replace(/组长/g, "员工").replace(/讨论串/g, "会话");
}

/** The repository chip under the new-session box: "选择仓库", "org/web · develop" or "org/web +2". */
export function reposLabel(list: SessionRepo[]): string {
  if (list.length === 0) return "选择仓库";
  const first = list[0].branch ? `${list[0].repo} · ${list[0].branch}` : list[0].repo;
  return list.length > 1 ? `${first} +${list.length - 1}` : first;
}

/** What the new-session box remembers between sessions (per browser): repositories, node, role. */
export interface SessionDefaults {
  repos: SessionRepo[];
  node: string;
  role: string;
}

export const DEFAULTS_KEY = "at:new-session";

export function parseDefaults(raw: string | null): SessionDefaults | null {
  try {
    const v = JSON.parse(raw ?? "null") as Partial<SessionDefaults> | null;
    if (!v || !Array.isArray(v.repos)) return null;
    const repos = v.repos.filter((r) => r && typeof r.repo === "string").map((r) => ({ ...r, branch: typeof r.branch === "string" ? r.branch : "" }));
    return { repos, node: typeof v.node === "string" ? v.node : "", role: typeof v.role === "string" ? v.role : "" };
  } catch {
    return null;
  }
}
