import { watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { toast } from "./toast";

/**
 * Show a message that came back in the URL after a redirect (e.g. from
 * GitHub), then drop those parameters from the address bar.
 */
export function useFlash(messages: Record<string, (value: string) => string>, onAny?: () => void) {
  const route = useRoute();
  const router = useRouter();
  watch(
    () => route.query,
    (query) => {
      const get = (k: string) => (typeof query[k] === "string" ? (query[k] as string) : "");
      const error = get("error");
      const found = Object.keys(messages).filter((k) => k in query);
      if (!error && found.length === 0) return;
      if (error) toast.error(error);
      found.forEach((k) => toast.success(messages[k](get(k))));
      onAny?.();
      const next = { ...query };
      ["error", ...found].forEach((k) => delete next[k]);
      void router.replace({ query: next });
    },
    { immediate: true },
  );
}
