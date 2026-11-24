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

/** A session's state in the sidebar (ProjectView.session_status), drawn as a small mark; null: nothing to show. */
export type SessionMark = "working" | "queued" | "finished" | "resolved" | "attention" | "paused" | "failed";

export const SESSION_MARK_LABEL: Record<SessionMark, string> = {
  working: "进行中",
  queued: "排队中",
  finished: "已完成",
  resolved: "已解决",
  attention: "等你处理",
  paused: "已暂停",
  failed: "失败",
};

/**
 * The mark of a session row (ProjectView.session_status, the same rule as the session page's
 * threadPhase): open cards show as the 等你处理 count instead (null here); a session whose employee
 * is not in a turn is 已完成, one the user confirmed is 已解决, one whose node is offline or whose
 * session crashed 等你处理. Projects have none.
 */
export function sessionMark(p: Pick<ProjectView, "kind" | "waiting" | "archived"> & { session_status?: string }): SessionMark | null {
  if (!isSession(p) || p.archived || p.waiting > 0) return null;
  switch (p.session_status) {
    case "working":
    case "running":
    case "review":
      return "working";
    case "queued":
      return "queued";
    case "finished":
      return "finished";
    case "resolved":
      return "resolved";
    // The employee's node is offline, its session crashed or no node can run it; or the thread waits on the user.
    case "attention":
    case "waiting":
      return "attention";
    case "paused":
      return "paused";
    case "failed":
      return "failed";
  }
  return null;
}

/** Tokens in Chinese units: 8,123 / 7.7万 / 100万. */
export function zhTokens(n: number): string {
  if (n < 10_000) return n.toLocaleString("en-US");
  const w = n / 10_000;
  return `${w >= 100 ? Math.round(w) : Number(w.toFixed(1))}万`;
}

/** A session's context total (ThreadView.session): null when the runtime reports none (Antigravity) or no turn ended yet. */
export interface ContextTotal {
  used: number;
  max: number;
  /** Share of the window, 0–100. */
  pct: number;
  /** "已使用 8% · 7.7万 / 100万". */
  text: string;
}

export function contextTotal(s: { context_used?: number | null; context_max?: number | null } | null | undefined): ContextTotal | null {
  const used = s?.context_used;
  const max = s?.context_max;
  if (used == null || !max || max <= 0) return null;
  const pct = Math.min(100, (used / max) * 100);
  const shown = used > 0 && pct < 1 ? "<1%" : `${Math.round(pct)}%`;
  return { used, max, pct, text: `已使用 ${shown} · ${zhTokens(used)} / ${zhTokens(max)}` };
}

/** The node of the new-session box: the remembered or picked one while it exists and is online, else auto (自动选择). */
export function usableNode(id: string, nodes: { id: string; online: boolean }[], auto: string): string {
  return id !== auto && nodes.some((n) => n.id === id && n.online) ? id : auto;
}
