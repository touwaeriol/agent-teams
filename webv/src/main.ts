import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { createPinia } from "pinia";
import { createApp } from "vue";
import { ApiError } from "./api/client";
import App from "./App.vue";
import { makeRouter } from "./router";
import "./lib/theme";
// Order matters: tokens + preflight, Element Plus, its theme, then Tailwind utilities (win last).
import "./styles/base.css";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "./styles/element.css";
import "./styles/index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      // 4xx answers are final; retry only network errors and 5xx.
      retry: (count, error) => count < 2 && !(error instanceof ApiError && error.status < 500),
    },
  },
});

// A 401 anywhere means the session ended: drop the user so the app shows the login page.
queryClient.getQueryCache().subscribe((event) => {
  const error = event.query.state.error;
  if (event.type === "updated" && error instanceof ApiError && error.status === 401 && event.query.queryKey[0] !== "me") {
    queryClient.setQueryData(["me"], null);
  }
});

// Dev-only office renderer demo (no backend, no login): /dev/office under `npm run dev`.
const officeDemo = import.meta.env.DEV && window.location.pathname.startsWith("/dev/office");

if (officeDemo) {
  void import("./office/demo/OfficeDemo.vue").then((m) => createApp(m.default).mount("#app"));
} else {
  createApp(App)
    .use(createPinia())
    .use(VueQueryPlugin, { queryClient })
    .use(makeRouter(queryClient))
    .mount("#app");
}
