import { spawnSync } from "node:child_process";
import { describe, expect, it } from "vitest";
import { disableCoreDumps } from "../src/util/coredump.js";

describe("disableCoreDumps", () => {
  it("sets RLIMIT_CORE to 0 on the daemon's own pid with prlimit", () => {
    const calls: string[][] = [];
    const r = disableCoreDumps({}, "linux", 42, (cmd, args) => (calls.push([cmd, ...args]), { status: 0 }));
    expect(r).toBe("off");
    expect(calls).toEqual([["prlimit", "--pid", "42", "--core=0:0"]]);
  });

  it("reports a failure (no prlimit) and keeps the limit when asked or off Linux", () => {
    expect(disableCoreDumps({}, "linux", 1, () => ({ status: null }))).toBe("failed");
    const never = () => { throw new Error("must not run"); };
    expect(disableCoreDumps({ AGENT_TEAMS_CORE_DUMPS: "on" }, "linux", 1, never)).toBe("kept");
    expect(disableCoreDumps({}, "darwin", 1, never)).toBe("kept");
  });

  it("really lowers this process's core limit where prlimit exists", () => {
    if (process.platform !== "linux" || spawnSync("sh", ["-c", "command -v prlimit"]).status !== 0) return;
    const child = spawnSync(process.execPath, ["-e", `
      const { spawnSync } = require("node:child_process");
      spawnSync("prlimit", ["--pid", String(process.pid), "--core=0:0"]);
      process.stdout.write(spawnSync("sh", ["-c", "ulimit -c"], { encoding: "utf8" }).stdout.trim());
    `], { encoding: "utf8" });
    expect(child.stdout).toBe("0");
  });
});
