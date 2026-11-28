// Pure helpers and the text fetcher of the deliverable viewer (web/src/pages/DesignViewer.tsx).
import { $t } from "@/i18n";
import { ref, toValue, watch, type MaybeRefOrGetter, type Ref } from "vue";
import type { DeliverableFiles, DeliverableVersion, ThreadView } from "@/api/views";
import { timeAgo } from "@/lib/utils";

/** "#12 标题 · dev-2 做的 · 3 分钟前" under the title. */
export function viewerSubtitle(thread: Pick<ThreadView, "no" | "title"> | undefined, author: string, updatedAt: string): string {
  return [thread ? `#${thread.no} ${thread.title}` : null, author ? $t("{author} 做的", { author }) : null, timeAgo(updatedAt)].filter(Boolean).join(" · ");
}

/** The message "通过，按这个做" sends to the lead. */
export function approvalText(label: string, title: string, version: number | undefined): string {
  return $t("{label}「{title}」{n}通过，按这个做。", { label, title, n: version ? $t("第 {version} 版", { version }) : "" });
}

/** The version to request when one is picked: 0 (= latest, follows new versions) for the newest. */
export function pickedVersion(files: Pick<DeliverableFiles, "versions" | "version">, picked: number): number {
  const latest = files.versions[0]?.version ?? files.version;
  return picked === latest ? 0 : picked;
}

/**
 * "v3（最新） · dev-2 修改 · 3 分钟前" in the version switcher: v1 is 发布, later versions 修改; the
 * publisher's thread is added when it is not the deliverable's own thread ("dev-1 修改（#2）").
 */
export function versionLabel(
  v: Pick<DeliverableVersion, "version" | "published_by" | "created_at" | "publisher_thread_no">,
  latest: number,
  threadNo?: number | null,
): string {
  const where = v.publisher_thread_no && v.publisher_thread_no !== threadNo ? `（#${v.publisher_thread_no}）` : "";
  const who = v.published_by === "user" ? $t("你") : v.published_by;
  return $t("v{version}{n} · {who} {n2}{where} · {created_at}", { version: v.version, n: v.version === latest ? $t("（最新）") : "", who, n2: v.version === 1 ? $t("发布") : $t("修改"), where, created_at: timeAgo(v.created_at) });
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
