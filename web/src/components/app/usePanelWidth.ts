import { computed, onBeforeUnmount, ref, watch, type Ref } from "vue";
import { PANEL_DEFAULT, clampWidth, loadWidth, panelMax, saveWidth } from "@/lib/panelWidth";

const viewport = () => (typeof window === "undefined" ? 1440 : window.innerWidth);

export interface PanelWidth {
  /** The width to draw: the remembered one clamped to the board. */
  width: Ref<number>;
  /** The widest the panel may be on the board right now. */
  max: Ref<number>;
  set: (w: number) => void;
  /** Back to the default width. */
  reset: () => void;
}

/**
 * The thread panel's width for a user, the same with the list or a thread open. The remembered
 * width is re-clamped as the board changes (a narrower window squeezes it, a wider one gives it
 * back) and stored per user.
 */
export function usePanelWidth(userId: () => string, board: Ref<number>): PanelWidth {
  const pref = ref(loadWidth(userId()));
  watch(userId, (id) => (pref.value = loadWidth(id)));
  const width = computed(() => clampWidth(pref.value, board.value));
  const max = computed(() => panelMax(board.value));
  const set = (w: number) => {
    pref.value = clampWidth(w, board.value);
    saveWidth(userId(), pref.value);
  };
  return { width, max, set, reset: () => set(PANEL_DEFAULT) };
}

/** Live width of an element (the board right of the project sidebar); the window width until it is mounted or without ResizeObserver. */
export function useElementWidth(el: Ref<HTMLElement | null | undefined>): Ref<number> {
  const width = ref(viewport());
  let ro: ResizeObserver | null = null;
  const onResize = () => (width.value = el.value?.getBoundingClientRect().width || viewport());
  const stop = watch(
    el,
    (node) => {
      ro?.disconnect();
      ro = null;
      if (!node) return;
      onResize();
      if (typeof ResizeObserver === "function") {
        ro = new ResizeObserver(onResize);
        ro.observe(node);
      }
    },
    { immediate: true },
  );
  // Without ResizeObserver, follow the window instead.
  const fallback = typeof ResizeObserver !== "function" && typeof window !== "undefined";
  if (fallback) window.addEventListener("resize", onResize);
  onBeforeUnmount(() => {
    stop();
    ro?.disconnect();
    if (fallback) window.removeEventListener("resize", onResize);
  });
  return width;
}
