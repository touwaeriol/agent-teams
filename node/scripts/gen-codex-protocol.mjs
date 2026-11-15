// Regenerates src/codex-protocol from the pinned codex CLI (architecture.md §4).
import { execFileSync } from "node:child_process";
import { rmSync, mkdirSync, writeFileSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const out = join(root, "src", "codex-protocol");
const require = createRequire(import.meta.url);
const codexBin = join(dirname(require.resolve("@openai/codex/package.json")), "bin", "codex.js");
const version = JSON.parse(readFileSync(require.resolve("@openai/codex/package.json"), "utf8")).version;

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
execFileSync(process.execPath, [codexBin, "app-server", "generate-ts", "--out", out], { stdio: "inherit" });
writeFileSync(join(out, "VERSION"), `${version}\n`);
