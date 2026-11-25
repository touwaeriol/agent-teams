// Repositories of a project (shared by NewProject and the project settings): pure helpers for the
// ordered list and the picker. Base branches are not set here: they belong in the project context.
import type { GitConnectionView, GitRepoView, RepoItem } from "@/api/views";

/** A project repository: a GitHub one (git_connection_id) or a standalone Git repository (git_repo_id,
 * repo = its host/path slug; docs/v2/git-repos.md). */
export interface RepoRef {
  repo: string;
  git_connection_id: string | null;
  git_repo_id?: string | null;
}

/** The picker's source value for standalone repositories (beside the GitHub connection ids). */
export const STANDALONE = "standalone";

export const REPOS_HINT = "基准分支、分支约定等写在项目指令里（例如“仓库 org/web 的基准分支：develop”），统筹和组长会按它开分支和 PR；没写的仓库用默认分支。";

/** owner/repo from a pasted GitHub URL or the text as typed. */
export function normalizeRepo(text: string): string {
  const t = text.trim().replace(/\.git$/, "");
  const m = t.match(/github\.com[/:]([^/\s]+\/[^/\s#?]+)/);
  return m ? m[1] : t;
}

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** Appends a repository unless it is empty or already in the list. */
export function addRepo(list: RepoRef[], item: RepoRef): RepoRef[] {
  if (item.git_repo_id) {
    if (list.some((r) => r.git_repo_id === item.git_repo_id || same(r.repo, item.repo))) return list;
    return [...list, { repo: item.repo, git_connection_id: null, git_repo_id: item.git_repo_id }];
  }
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
  return list
    .map((r) => (r.git_repo_id ? { repo: r.repo, git_connection_id: null, git_repo_id: r.git_repo_id } : { repo: normalizeRepo(r.repo), git_connection_id: r.git_connection_id }))
    .filter((r) => r.repo && (r.git_connection_id || ("git_repo_id" in r && r.git_repo_id)));
}

/** Where a listed repository comes from: "GitHub · login", "GitLab · 单独添加", or a removed source. */
export function sourceLabel(item: RepoRef, connections: Pick<GitConnectionView, "id" | "login" | "name">[], gitRepos: Pick<GitRepoView, "id" | "provider">[]): string {
  if (item.git_repo_id) {
    const g = gitRepos.find((r) => r.id === item.git_repo_id);
    return g ? `${PROVIDERS[g.provider] ?? "Git"} · 单独添加` : "仓库已删除";
  }
  const c = connections.find((x) => x.id === item.git_connection_id);
  return c ? connectionLabel(c) : "连接已删除";
}

const PROVIDERS: Record<string, string> = { github: "GitHub", gitlab: "GitLab", gitea: "Gitea", bitbucket: "Bitbucket", generic: "自建 Git" };

/** Picker rows for standalone repositories not already listed. */
export function standaloneOptions(items: GitRepoView[], value: RepoRef[], currentProject?: string): RepoOption[] {
  return items
    .filter((g) => !value.some((v) => v.git_repo_id === g.id || same(v.repo, g.slug)))
    .map((g) => {
      const used = (g.projects ?? []).filter((p) => p.id !== currentProject);
      const note = used.length ? `已被 ${used.map((p) => p.name).join("、")} 使用` : g.access === "read" ? "只读" : "";
      return { value: g.slug, usedBy: note, private: false, keywords: `${g.name} ${g.url}` };
    });
}

export function connectionLabel(c: Pick<GitConnectionView, "login" | "name">): string {
  return `GitHub · ${c.login || c.name}`;
}

/** A GitHub connection made with a pasted personal access token rather than the platform App. */
export function isPatConnection(c: Pick<GitConnectionView, "kind"> | undefined): boolean {
  return c?.kind === "token";
}

/**
 * The risk of a PAT connection (audit #55 M4): sessions get the PAT itself, not a narrowed
 * short-lived token. Shown on the PAT connection card and when a project picks a PAT connection.
 */
export const PAT_WARNING =
  "使用 PAT 连接时，这个 PAT 会原样交给项目里的每个 AI 会话（组长和成员）用于 git/gh 操作。请使用只授权到本项目仓库、设置了过期时间的 fine-grained PAT；需要更严格的隔离请改用 GitHub App 连接（会话只拿到收窄过的短期令牌）。";

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
