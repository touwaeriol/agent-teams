// Pure helpers and the text fetcher of the deliverable viewer (web/src/pages/DesignViewer.tsx).
import { ref, toValue, watch, type MaybeRefOrGetter, type Ref } from "vue";
import type { DeliverableFiles, DeliverableVersion, ThreadView } from "@/api/views";
import { timeAgo } from "@/lib/utils";

/** "#12 标题 · dev-2 做的 · 3 分钟前" under the title. */
export function viewerSubtitle(thread: Pick<ThreadView, "no" | "title"> | undefined, author: string, updatedAt: string): string {
  return [thread ? `#${thread.no} ${thread.title}` : null, author ? `${author} 做的` : null, timeAgo(updatedAt)].filter(Boolean).join(" · ");
}

/** The message "通过，按这个做" sends to the lead. */
export function approvalText(label: string, title: string, version: number | undefined): string {
  return `${label}「${title}」${version ? `第 ${version} 版` : ""}通过，按这个做。`;
}

/** The version to request when one is picked: 0 (= latest, follows new versions) for the newest. */
export function pickedVersion(files: Pick<DeliverableFiles, "versions" | "version">, picked: number): number {
  const latest = files.versions[0]?.version ?? files.version;
  return picked === latest ? 0 : picked;
}

export function versionLabel(v: Pick<DeliverableVersion, "version" | "published_by" | "created_at">, latest: number): string {
  return `第 ${v.version} 版${v.version === latest ? "（最新）" : ""} · ${v.published_by} · ${timeAgo(v.created_at)}`;
}

/** The text at a signed URL (no cookies, no referrer); refetched when the URL changes. */
export function useText(url: MaybeRefOrGetter<string>): { text: Ref<string | undefined>; error: Ref<boolean> } {
  const text = ref<string>();
  const error = ref(false);
  let current = "";
  watch(
    () => toValue(url),
    (u) => {
      current = u;
      text.value = undefined;
      error.value = false;
      fetch(u, { credentials: "omit", referrerPolicy: "no-referrer" })
        .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
        .then((t) => current === u && (text.value = t))
        .catch(() => current === u && (error.value = true));
    },
    { immediate: true },
  );
  return { text, error };
}
