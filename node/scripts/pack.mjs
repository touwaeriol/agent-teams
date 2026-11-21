// Builds the release tarball the console serves at /node/dist/agent-teams-node-<release>.tgz
// (release = <version>+<first 8 hex of sha256(node.mjs)>, the id the daemon reports):
// dist/node.mjs + dist/launch.mjs + a package.json whose dependencies are the externals, pinned exactly, plus a
// lock file (npm-shrinkwrap.json) so nodes install with `npm ci --omit=dev`.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync, renameSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
const EXTERNAL = ["@anthropic-ai/claude-agent-sdk", "@openai/codex", "@modelcontextprotocol/sdk", "zod"];
const stage = join(root, "dist", "package");

rmSync(stage, { recursive: true, force: true });
mkdirSync(stage, { recursive: true });
copyFileSync(join(root, "dist/node.mjs"), join(stage, "node.mjs"));
copyFileSync(join(root, "dist/launch.mjs"), join(stage, "launch.mjs"));
const build = createHash("sha256").update(readFileSync(join(stage, "node.mjs"))).digest("hex").slice(0, 8);
const dependencies = Object.fromEntries(EXTERNAL.map((name) => [name, installedVersion(name)]));
writeFileSync(join(stage, "package.json"), JSON.stringify({
  name: "agent-teams-node", version: pkg.version, private: true, type: "module",
  description: pkg.description, bin: { "agent-teams-node": "node.mjs" }, engines: pkg.engines, dependencies,
}, null, 2) + "\n");
execFileSync("npm", ["install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund"], { cwd: stage, stdio: "inherit" });
// npm pack never ships package-lock.json; npm-shrinkwrap.json is packed and honoured by `npm ci`.
renameSync(join(stage, "package-lock.json"), join(stage, "npm-shrinkwrap.json"));
const out = execFileSync("npm", ["pack", "--json", "--pack-destination", join(root, "dist")], { cwd: stage, encoding: "utf8" });
const file = JSON.parse(out)[0].filename;
const target = join(root, "dist", `agent-teams-node-${pkg.version}+${build}.tgz`);
renameSync(join(root, "dist", file), target);
process.stdout.write(`${target}\n`);

function installedVersion(name) {
  return JSON.parse(readFileSync(join(root, "node_modules", name, "package.json"), "utf8")).version;
}
