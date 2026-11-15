import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { loadConfig, parseEnvFile, toWsUrl } from "../src/config.js";
import { backoffDelay } from "../src/link/client.js";

describe("config", () => {
  it("reads node.env like v1 and lets env and flags override it", () => {
    const home = mkdtempSync(join(tmpdir(), "at-cfg-"));
    try {
      mkdirSync(join(home, ".config/agent-teams"), { recursive: true });
      writeFileSync(join(home, ".config/agent-teams/node.env"), "AGENT_TEAMS_CONSOLE=https://console.example\nAGENT_TEAMS_NODE=n1\nAGENT_TEAMS_NODE_TOKEN=tok-file\nAGENT_TEAMS_ROOT=/srv/at\nAGENT_TEAMS_MAX_RUNS=6\nPATH=/x\n");
      const env = { HOME: home, AGENT_TEAMS_NODE_TOKEN: "tok-env" } as NodeJS.ProcessEnv;
      const c = loadConfig(["--root", "/srv/other"], env);
      expect(c).toMatchObject({ wsUrl: "wss://console.example/nodes/connect", token: "tok-env", root: "/srv/other", maxActive: 6, configDir: join(home, ".config/agent-teams") });
      expect(loadConfig(["--token=tok-flag", "--max-sessions", "2"], env)).toMatchObject({ token: "tok-flag", maxActive: 2 });
      expect(() => loadConfig([], { HOME: join(home, "nowhere") })).toThrow(/AGENT_TEAMS_CONSOLE/);
      expect(() => loadConfig(["--bogus", "1"], env)).toThrow(/unknown option/);
    } finally {
      rmSync(home, { recursive: true, force: true });
    }
  });

  it("parses env files and console URLs", () => {
    expect(parseEnvFile(`# c\nexport A="1 2"\nB='x'\nbad line\nC=`)).toEqual({ A: "1 2", B: "x", C: "" });
    expect(toWsUrl("http://h:3001")).toBe("ws://h:3001/nodes/connect");
    expect(toWsUrl("wss://h/nodes/v2/connect")).toBe("wss://h/nodes/v2/connect");
    expect(() => toWsUrl("ftp://h")).toThrow();
  });

  it("backs off exponentially from 1 s to 30 s with ±20% jitter", () => {
    for (let i = 0; i < 50; i++) {
      const d0 = backoffDelay(0);
      expect(d0).toBeGreaterThanOrEqual(800);
      expect(d0).toBeLessThanOrEqual(1200);
      const d9 = backoffDelay(9);
      expect(d9).toBeGreaterThanOrEqual(24000);
      expect(d9).toBeLessThanOrEqual(36000);
    }
  });
});
