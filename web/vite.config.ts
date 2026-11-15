import path from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// `npm run build` writes dist/ (git-ignored); the Go server serves it from WEB_DIR.
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  build: {
    outDir: path.resolve(__dirname, "dist"),
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Long-lived vendor chunks: the app chunk changes on every release, these rarely do.
        manualChunks: (id) => {
          if (!id.includes("node_modules")) return undefined;
          if (/react-markdown|remark|micromark|mdast|hast|unified|unist|vfile/.test(id)) return "markdown";
          if (/react-dom|react-router|scheduler|@tanstack|@radix-ui/.test(id)) return "vendor";
          return undefined;
        },
      },
    },
  },
  server: {
    proxy: Object.fromEntries(
      ["/api", "/auth", "/install-node.sh"].map((p) => [p, { target: process.env.API_TARGET ?? "http://127.0.0.1:3001", changeOrigin: false }]),
    ),
  },
});
