import { createApp } from "vue";
import { initLocale } from "./i18n";
import "./lib/theme";
// Order matters: tokens + preflight, Element Plus, its theme, then Tailwind utilities (win last).
import "./styles/base.css";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "./styles/element.css";
import "./styles/index.css";

// Dev-only office renderer demo (no backend, no login): /dev/office under `npm run dev`.
const officeDemo = import.meta.env.DEV && window.location.pathname.startsWith("/dev/office");

if (officeDemo) {
  void import("./office/demo/OfficeDemo.vue").then((m) => createApp(m.default).mount("#app"));
} else {
  // The language first, then the app (bootstrap.ts), so every string loads in that language.
  void initLocale().then(() => import("./bootstrap"));
}
