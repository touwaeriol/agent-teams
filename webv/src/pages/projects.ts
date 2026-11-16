import type { ProjectView } from "@/api/views";

/** Pinned first, then most recently updated; archived projects only if nothing else. */
export function recentProject(list: ProjectView[]): ProjectView | undefined {
  const live = list.filter((p) => !p.archived);
  const byRecent = (a: ProjectView, b: ProjectView) => Number(b.pinned) - Number(a.pinned) || Date.parse(b.updated_at) - Date.parse(a.updated_at);
  return live.sort(byRecent)[0] ?? list[0];
}
