import type { ProjectView } from "@/api/views";

export function matchesProject(p: Pick<ProjectView, "name" | "description" | "repos">, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return [p.name, p.description, ...(p.repos ?? []).map((r) => r.repo)].some((s) => s.toLowerCase().includes(needle));
}

/** The sidebar's groups: pinned, the rest, and archived (folded). */
export function sidebarGroups(list: ProjectView[], q: string) {
  const shown = list.filter((p) => matchesProject(p, q));
  const groups = [
    { title: "已置顶", items: shown.filter((p) => p.pinned && !p.archived) },
    { title: "项目", items: shown.filter((p) => !p.pinned && !p.archived) },
  ].filter((g) => g.items.length > 0);
  return { shown, groups, archived: shown.filter((p) => p.archived) };
}
