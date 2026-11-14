import path from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Built into the Python package so `agent-teams serve` serves it at /.
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  build: {
    outDir: path.resolve(__dirname, "../src/agent_teams/server/static/app"),
    emptyOutDir: true,
  },
  server: {
    proxy: Object.fromEntries(
      ["/api", "/auth", "/install-node.sh"].map((p) => [p, "http://127.0.0.1:3001"]),
    ),
  },
});
