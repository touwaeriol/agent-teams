// Repositories of a project (shared by NewProject and the project settings): pure helpers for the
// ordered list and the picker. Base branches are not set here: they belong in the project context.
import type { GitConnectionView, RepoItem } from "@/api/views";

export interface RepoRef {
  repo: string;
  git_connection_id: string | null;
}

export const REPOS_HINT = "基准分支、分支约定等写在项目上下文里（例如“仓库 org/web 的基准分支：develop”），统筹和组长会按它开分支和 PR；没写的仓库用默认分支。";

/** owner/repo from a pasted GitHub URL or the text as typed. */
export function normalizeRepo(text: string): string {
  const t = text.trim().replace(/\.git$/, "");
  const m = t.match(/github\.com[/:]([^/\s]+\/[^/\s#?]+)/);
  return m ? m[1] : t;
}

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** Appends a repository unless it is empty or already in the list. */
export function addRepo(list: RepoRef[], item: RepoRef): RepoRef[] {
  const repo = normalizeRepo(item.repo);
  if (!repo || !item.git_connection_id || list.some((r) => same(r.repo, repo))) return list;
  return [...list, { repo, git_connection_id: item.git_connection_id }];
}

export function removeRepo(list: RepoRef[], index: number): RepoRef[] {
  return list.filter((_, i) => i !== index);
}

/** Moves the repository at index by delta (-1 up, +1 down); out-of-range moves change nothing. */
export function moveRepo(list: RepoRef[], index: number, delta: number): RepoRef[] {
  const to = index + delta;
  if (to < 0 || to >= list.length) return list;
  const next = [...list];
  [next[index], next[to]] = [next[to], next[index]];
  return next;
}

/** The repos field of POST/PATCH /api/projects. */
export function reposPayload(list: RepoRef[]): RepoRef[] {
  return list.map((r) => ({ repo: normalizeRepo(r.repo), git_connection_id: r.git_connection_id })).filter((r) => r.repo && r.git_connection_id);
}

export function connectionLabel(c: Pick<GitConnectionView, "login" | "name">): string {
  return `GitHub · ${c.login || c.name}`;
}

export interface RepoOption {
  value: string;
  /** Right-hand note: other projects using it, or "private" (drawn as a lock). */
  usedBy: string;
  private: boolean;
  keywords: string;
}

/** Picker rows: repositories not already listed, with which other projects use them. */
export function repoOptions(items: RepoItem[], value: RepoRef[], currentProject?: string): RepoOption[] {
  return items
    .filter((r) => !value.some((v) => same(v.repo, r.full_name)))
    .map((r) => {
      const used = (r.used_by_projects ?? []).filter((p) => p.id !== currentProject);
      return { value: r.full_name, usedBy: used.length ? `已被 ${used.map((p) => p.name).join("、")} 使用` : "", private: Boolean(r.private), keywords: r.description ?? "" };
    });
}

/** The Combobox filter: the name and description contain the typed text (case-insensitive). */
export function matchesRepo(o: Pick<RepoOption, "value" | "keywords">, text: string): boolean {
  const needle = text.trim().toLowerCase();
  return !needle || `${o.value} ${o.keywords}`.toLowerCase().includes(needle);
}

/** Repos a project uses first, then by last push (GitHub connections page). */
export function sortRepos(list: RepoItem[]): RepoItem[] {
  const used = (r: RepoItem) => (r.used_by_projects?.length ? 1 : 0);
  return list.slice().sort((a, b) => used(b) - used(a) || Date.parse(b.pushed_at ?? "0") - Date.parse(a.pushed_at ?? "0"));
}
