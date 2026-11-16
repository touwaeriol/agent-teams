import path from "node:path";
import vue from "@vitejs/plugin-vue";
import vueJsx from "@vitejs/plugin-vue-jsx";
import { existsSync } from "node:fs";
import Components, { type ComponentResolver } from "unplugin-vue-components/vite";
import { defineConfig } from "vitest/config";

/**
 * <el-dropdown-item> → `import { ElDropdownItem } from "element-plus/es/components/dropdown/index.mjs"`.
 * Per-component paths, not "element-plus/es": that index keeps the full installer (every
 * component, ~900 kB) alive. The directory is the longest kebab prefix that exists.
 */
const elementPlus: ComponentResolver = {
  type: "component",
  resolve(name) {
    if (!/^El[A-Z]/.test(name)) return undefined;
    const words = name.slice(2).split(/(?=[A-Z])/).map((w) => w.toLowerCase());
    for (let n = words.length; n > 0; n--) {
      const from = `element-plus/es/components/${words.slice(0, n).join("-")}/index.mjs`;
      if (existsSync(path.resolve(__dirname, "node_modules", from))) return { name, from };
    }
    return undefined;
  },
};

// `npm run build` writes dist/ (git-ignored); the Go server serves it from WEB_DIR.
// server/Dockerfile builds it with `vite build --outDir /out/web` (see README.md, "Build").
export default defineConfig({
  plugins: [
    vue(),
    vueJsx(),
    // <el-*> tags in templates import just that component (tree-shaken); the full Element Plus
    // stylesheet is loaded once in main.ts so the theme overrides in styles/element.css apply.
    Components({ resolvers: [elementPlus], dts: "src/components.d.ts", dirs: [] }),
  ],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  build: {
    outDir: path.resolve(__dirname, "dist"),
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Long-lived vendor chunks: the app chunk changes on every release, these rarely do.
        manualChunks: (id) => {
          if (!id.includes("node_modules")) return undefined;
          if (/remark|micromark|mdast|hast|unified|unist|vfile/.test(id)) return "markdown";
          if (/element-plus|@element-plus|@floating-ui|async-validator|dayjs|lodash/.test(id)) return "element";
          if (/[\\/]vue[\\/]|@vue|vue-router|pinia|@tanstack/.test(id)) return "vendor";
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
  test: {
    // Node by default (fast, file fixtures); component tests opt in with `// @vitest-environment jsdom`.
    environment: "node",
    environmentOptions: { jsdom: { url: "http://localhost/" } },
    // Node ≥ 25 ships its own (empty) global localStorage, which hides jsdom's.
    poolOptions: { forks: { execArgv: ["--no-experimental-webstorage"] } },
    include: ["src/**/*.test.ts"],
    setupFiles: ["src/__tests__/setup.ts"],
  },
});
