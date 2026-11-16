import { onBeforeUnmount, ref, type Ref } from "vue";
import { clampWidth, loadWidth, saveWidth } from "@/lib/panelWidth";

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
