import { $t } from "@/i18n";
import type { RoleView } from "@/api/views";
import { RUNTIME_LABEL } from "@/lib/present";

/** Lead + member roles as chips (NewProject “项目团队”). */
export function teamChips(roles: RoleView[]): { name: string; n: number; kind: string }[] {
  return roles
    .filter((r) => r.tier !== "coordinator")
    .sort((a, b) => (a.tier === "lead" ? -1 : b.tier === "lead" ? 1 : a.sort - b.sort))
    .map((r) => ({ name: r.tier === "lead" ? $t("组长") : r.title || r.role, n: r.tier === "lead" ? 1 : r.count, kind: RUNTIME_LABEL[r.runtime] ?? r.runtime }));
}
