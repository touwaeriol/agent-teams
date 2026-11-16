// Light by default; the choice is remembered in this browser. The `dark` class on <html> drives
// both our tokens (styles/base.css) and Element Plus' dark variables.
import { ref, watch } from "vue";

export type Theme = "light" | "dark";

export function readTheme(): Theme {
  try {
    return localStorage.getItem("theme") === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
}

const theme = ref<Theme>(readTheme());

watch(
  theme,
  (t) => {
    document.documentElement.classList.toggle("dark", t === "dark");
    try {
      localStorage.setItem("theme", t);
    } catch {
      /* private mode: keep it for this page only */
    }
  },
  { immediate: true },
);

export function useTheme() {
  return { theme, setTheme: (t: Theme) => (theme.value = t) };
}
