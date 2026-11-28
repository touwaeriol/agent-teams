// GitHub connections helpers (pure).
import { $t, intlLocale } from "@/i18n";
import type { GitConnectionView, RepoItem } from "@/api/views";

export const REPOS_SHOWN = 4;

/** 组织 / 个人账号 / 个人令牌（PAT）: an App installed on an organization links to its org settings. */
export function connectionKind(c: Pick<GitConnectionView, "kind" | "installation_settings_url">): string {
  if (c.kind === "token") return $t("个人令牌（PAT）");
  return c.installation_settings_url?.includes("/organizations/") ? $t("组织") : $t("个人账号");
}

export function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString(intlLocale(), { month: "long", day: "numeric" });
}

export function usedText(r: Pick<RepoItem, "used_by_projects">): string {
  return r.used_by_projects?.length ? $t("项目 {used_by_projects}", { used_by_projects: r.used_by_projects.map((p) => p.name).join("、") }) : $t("未使用");
}

/** GitHub's manifest flow: the browser posts the manifest to github.com. */
export function postToGitHub(action: string, manifest: string, doc: Document = document) {
  const form = doc.createElement("form");
  form.method = "post";
  form.action = action;
  const field = doc.createElement("input");
  field.type = "hidden";
  field.name = "manifest";
  field.value = manifest;
  form.appendChild(field);
  doc.body.appendChild(form);
  form.submit();
}
