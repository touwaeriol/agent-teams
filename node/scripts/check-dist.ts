// Starts the bundled dist/node.mjs against the in-repo fake console (isolated HOME, explicit flags)
// and waits for hello + sessions.report.   npx tsx scripts/check-dist.ts
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { FakeConsole } from "../test/helpers/fake-console.js";

const home = mkdtempSync(join(tmpdir(), "at-dist-"));
const fc = new FakeConsole();
const url = await fc.start();
const env = { PATH: process.env.PATH ?? "", HOME: home, XDG_RUNTIME_DIR: join(home, "run"), AGENT_TEAMS_LOG_LEVEL: "warn" };
const child = spawn(process.execPath, ["dist/node.mjs", "run", "--console", url, "--token", fc.token, "--root", join(home, "root"), "--state", join(home, "state")], { env, stdio: "inherit" });
try {
  const hello = await fc.waitType("hello", () => true, 60_000);
  await fc.waitType("sessions.report", () => true, 10_000);
  console.log("dist daemon connected:", JSON.stringify({ daemon: hello.body.daemon, runtimes: hello.body.runtimes, isolation: hello.body.isolation }));
  if (fc.invalid.length) throw new Error(fc.invalid.join("; "));
} finally {
  child.kill("SIGTERM");
  await new Promise((r) => child.once("exit", r));
  await fc.stop();
  rmSync(home, { recursive: true, force: true });
}
