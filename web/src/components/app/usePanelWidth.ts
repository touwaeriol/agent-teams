import { computed, onBeforeUnmount, ref, watch, type Ref } from "vue";
import { clampDetail, clampWidth, detailDefault, loadDetail, loadWidth, saveDetail, saveWidth } from "@/lib/panelWidth";

const viewport = () => (typeof window === "undefined" ? 1440 : window.innerWidth);

/** The right panel width for a user, kept within the window as it resizes and remembered per user. */
export function usePanelWidth(userId: () => string): [Ref<number>, (w: number) => void] {
  const width = ref(clampWidth(loadWidth(userId()), viewport()));
  const onResize = () => (width.value = clampWidth(width.value, viewport()));
  window.addEventListener("resize", onResize);
  onBeforeUnmount(() => window.removeEventListener("resize", onResize));
  const set = (w: number) => {
    width.value = clampWidth(w, viewport());
    saveWidth(userId(), width.value);
  };
  return [width, set];
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
  if (typeof window !== "undefined") window.addEventListener("resize", onResize);
  onBeforeUnmount(() => {
    stop();
    ro?.disconnect();
    if (typeof window !== "undefined") window.removeEventListener("resize", onResize);
  });
  return width;
}

export interface DetailWidth {
  /** The width to draw: the remembered one clamped to the board, or the default. */
  width: Ref<number>;
  /** The default (widest) width for the board right now. */
  max: Ref<number>;
  set: (w: number) => void;
  /** Back to the default: fill the board but the coordinator strip. */
  reset: () => void;
}

/**
 * Width of the panel with a thread open: by default the whole board but the coordinator
 * strip; a dragged width is remembered per user (separately from the list panel's) and
 * re-clamped as the board changes. A width at the default is stored as "follow the default".
 */
export function useDetailWidth(userId: () => string, board: Ref<number>): DetailWidth {
  const pref = ref<number | null>(loadDetail(userId()));
  watch(userId, (id) => (pref.value = loadDetail(id)));
  const width = computed(() => clampDetail(pref.value, board.value));
  const max = computed(() => detailDefault(board.value));
  const set = (w: number) => {
    const next = clampDetail(w, board.value);
    pref.value = next >= max.value ? null : next;
    saveDetail(userId(), pref.value, board.value);
  };
  const reset = () => {
    pref.value = null;
    saveDetail(userId(), null, board.value);
  };
  return { width, max, set, reset };
}
