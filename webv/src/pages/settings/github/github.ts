// GitHub connections helpers (pure).
import type { GitConnectionView, RepoItem } from "@/api/views";

export const REPOS_SHOWN = 4;

/** 组织 / 个人账号: an App installed on an organization links to its org settings. */
export function connectionKind(c: Pick<GitConnectionView, "kind" | "installation_settings_url">): string {
  return c.kind === "app" && c.installation_settings_url?.includes("/organizations/") ? "组织" : "个人账号";
}

export function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString("zh-CN", { month: "long", day: "numeric" });
}

export function usedText(r: Pick<RepoItem, "used_by_projects">): string {
  return r.used_by_projects?.length ? `项目 ${r.used_by_projects.map((p) => p.name).join("、")}` : "未使用";
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
