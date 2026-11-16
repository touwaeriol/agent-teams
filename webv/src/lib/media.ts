import { onBeforeUnmount, ref, type Ref } from "vue";

function matches(query: string): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(query).matches;
}

/** Live media query. */
export function useMedia(query: string): Ref<boolean> {
  const on = ref(matches(query));
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return on;
  const mq = window.matchMedia(query);
  const update = () => (on.value = mq.matches);
  mq.addEventListener("change", update);
  onBeforeUnmount(() => mq.removeEventListener("change", update));
  return on;
}

/** Phone layout (below Tailwind's md). */
export const useIsMobile = () => useMedia("(max-width: 767px)");
