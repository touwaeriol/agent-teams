// pretest: the tests load protocol/scripts/validator.mjs (the console's validator), which needs
// protocol/'s own dependencies (ajv). Install them once when protocol/node_modules is missing
// (a fresh checkout, CI), so `npm test` in node/ works on its own.
import { execSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const protocol = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "protocol");
if (!existsSync(join(protocol, "node_modules", "ajv"))) {
  console.log("installing protocol/ dependencies (npm ci) for the tests");
  execSync("npm ci --no-audit --no-fund", { cwd: protocol, stdio: "inherit" });
}
