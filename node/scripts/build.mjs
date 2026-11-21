// Bundles src/cli.ts into dist/node.mjs (architecture.md §5.2). The Agent SDK, Codex CLI and
// MCP SDK stay external: they locate their native binaries relative to their own packages.
import { build } from "esbuild";
import { chmodSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
export const EXTERNAL = ["@anthropic-ai/claude-agent-sdk", "@openai/codex", "@modelcontextprotocol/sdk"];

await build({
  entryPoints: [join(root, "src/cli.ts")],
  outfile: join(root, "dist/node.mjs"),
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node22",
  sourcemap: "linked",
  external: [...EXTERNAL, ...EXTERNAL.map((e) => `${e}/*`), "bufferutil", "utf-8-validate"],
  banner: { js: `#!/usr/bin/env node\nimport { createRequire as __atCreateRequire } from "node:module";\nconst require = __atCreateRequire(import.meta.url);` },
  define: { "process.env.AGENT_TEAMS_BUILD_VERSION": JSON.stringify(pkg.version) },
  logLevel: "info",
});
chmodSync(join(root, "dist/node.mjs"), 0o755);

// The update launcher (src/launcher.ts): node builtins only, no external packages.
await build({
  entryPoints: [join(root, "src/launcher.ts")],
  outfile: join(root, "dist/launch.mjs"),
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node22",
  banner: { js: "#!/usr/bin/env node" },
  logLevel: "info",
});
chmodSync(join(root, "dist/launch.mjs"), 0o755);
