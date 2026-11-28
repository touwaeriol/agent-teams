// The app proper, imported by main.ts once the interface language is loaded, so module-level
// strings (t() in plain .ts files) are already in that language.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { createPinia } from "pinia";
import { createApp } from "vue";
import { ApiError } from "./api/client";
import App from "./App.vue";
import i18n from "./i18n";
import { makeRouter } from "./router";

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

createApp(App)
  .use(i18n)
  .use(createPinia())
  .use(VueQueryPlugin, { queryClient })
  .use(makeRouter(queryClient))
  .mount("#app");
