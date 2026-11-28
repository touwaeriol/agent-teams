import { $t } from "@/i18n";
import type { ProjectView } from "@/api/views";

/** Pinned first, then most recently updated; archived projects only if nothing else. */
export function recentProject(list: ProjectView[]): ProjectView | undefined {
  const live = list.filter((p) => !p.archived);
  const byRecent = (a: ProjectView, b: ProjectView) => Number(b.pinned) - Number(a.pinned) || Date.parse(b.updated_at) - Date.parse(a.updated_at);
  return live.sort(byRecent)[0] ?? list[0];
}

/** The projects page (/projects): projects only (sessions live in the sidebar), pinned then the
 * others by last update, archived ones last; filtered by the search text. */
export function projectGroups(list: ProjectView[], q = "") {
  const needle = q.trim().toLowerCase();
  const shown = list
    .filter((p) => p.kind !== "session")
    .filter((p) => !needle || [p.name, p.description, ...(p.repos ?? []).map((r) => r.repo)].some((s) => s.toLowerCase().includes(needle)))
    .sort((a, b) => Number(a.archived) - Number(b.archived) || Date.parse(b.updated_at) - Date.parse(a.updated_at));
  return [
    { title: $t("已置顶"), items: shown.filter((p) => p.pinned && !p.archived) },
    { title: $t("其他项目"), items: shown.filter((p) => !p.pinned || p.archived) },
  ].filter((g) => g.items.length > 0);
}
