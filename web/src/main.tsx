import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import React, { lazy, Suspense } from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { Toaster } from "sonner";
import { App } from "./App";
import { ThemeProvider, useTheme } from "./lib/theme";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 5_000, refetchOnWindowFocus: false } },
});

// Dev-only office renderer demo (no backend, no login): /dev/office under `npm run dev`.
const OfficeDemo =
  import.meta.env.DEV && window.location.pathname.startsWith("/dev/office") ? lazy(() => import("./office/demo/OfficeDemo")) : null;

function Toasts() {
  const { theme } = useTheme();
  return <Toaster theme={theme} position="top-center" richColors closeButton />;
}

const root = ReactDOM.createRoot(document.getElementById("root")!);

if (OfficeDemo) {
  root.render(
    <Suspense fallback={null}>
      <OfficeDemo />
    </Suspense>,
  );
} else {
  root.render(
    <React.StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <BrowserRouter>
            <App />
          </BrowserRouter>
          <Toasts />
        </QueryClientProvider>
      </ThemeProvider>
    </React.StrictMode>,
  );
}
