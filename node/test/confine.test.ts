import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { confinedClaudeSpawn, probeConfine, setprivConfine, unconfined } from "../src/sessions/confine.js";
import { onPath } from "../src/isolation.js";
import { claudeOpen, startHarness } from "./helpers/daemon.js";

const real = process.platform === "linux" && onPath("setpriv");

describe("session confinement", () => {
  it("wraps a command with setpriv: no_new_privs, and the capability drops as root", () => {
    expect(setprivConfine("setpriv", false)("claude", ["--x"])).toEqual({ cmd: "setpriv", args: ["--no-new-privs", "--", "claude", "--x"] });
    expect(setprivConfine("/usr/bin/setpriv", true)("codex", ["app-server"])).toEqual({
      cmd: "/usr/bin/setpriv", args: ["--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--", "codex", "app-server"],
    });
    expect(unconfined("agy", ["acp"])).toEqual({ cmd: "agy", args: ["acp"] });
    expect(probeConfine({})).toBe(unconfined);
    expect(probeConfine({ AGENT_TEAMS_CONFINE: "off" })).toBe(unconfined);
  });

  it.runIf(real)("leaves a session no way to gain privileges or mount", () => {
    const confine = probeConfine({ AGENT_TEAMS_CONFINE: "on" });
    expect(confine).not.toBe(unconfined);
    const sh = (script: string) => {
      const c = confine("sh", ["-c", script]);
      return spawnSync(c.cmd, c.args, { encoding: "utf8" });
    };
    expect(sh("grep NoNewPrivs /proc/self/status").stdout).toMatch(/NoNewPrivs:\s+1/);
    expect(sh("grep CapEff /proc/self/status").stdout).toMatch(/CapEff:\s+0+\n/);
    const dir = mkdtempSync(join(tmpdir(), "at-confine-"));
    try {
      expect(sh(`mount -t tmpfs none ${dir}`).status).not.toBe(0);
      if (onPath("fusermount3")) expect(sh(`fusermount3 -u ${dir}`).status).not.toBe(0);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it.runIf(real)("runs Claude Code through the wrapper and forwards its stderr", async () => {
    const lines: string[] = [];
    const spawnFn = confinedClaudeSpawn(probeConfine({ AGENT_TEAMS_CONFINE: "on" }), (d) => lines.push(d));
    const child = spawnFn({ command: "sh", args: ["-c", "grep NoNewPrivs /proc/self/status >&2; echo out"], env: { PATH: process.env.PATH }, signal: new AbortController().signal });
    let out = "";
    child.stdout.on("data", (d: Buffer) => (out += d.toString()));
    const code = await new Promise<number | null>((res) => child.on("exit", (c) => res(c)));
    await new Promise((r) => setTimeout(r, 20));
    expect(code).toBe(0);
    expect(out.trim()).toBe("out");
    expect(lines.join("")).toMatch(/NoNewPrivs:\s+1/);
  });

  it("gives Claude sessions the confined spawn, and none when unconfined", async () => {
    for (const [confine, wrapped] of [[setprivConfine("setpriv", false), true], [unconfined, false]] as const) {
      const h = await startHarness({ confine });
      try {
        h.console.command("session.open", claudeOpen("s_C", 1));
        await h.console.waitType("session.opened", (b) => b.session_id === "s_C");
        expect(typeof h.log.options.at(-1)!.spawnClaudeCodeProcess === "function").toBe(wrapped);
      } finally {
        await h.close();
      }
    }
  });
});
