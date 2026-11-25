import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { loadConfig, parseEnvFile, toWsUrl } from "../src/config.js";
import { backoffDelay } from "../src/link/client.js";

describe("config", () => {
  it("reads node.env and lets env and flags override it", () => {
    const home = mkdtempSync(join(tmpdir(), "at-cfg-"));
    try {
      mkdirSync(join(home, ".config/agent-teams"), { recursive: true });
      writeFileSync(join(home, ".config/agent-teams/node.env"), "AGENT_TEAMS_CONSOLE=https://console.example\nAGENT_TEAMS_NODE=n1\nAGENT_TEAMS_NODE_TOKEN=tok-file\nAGENT_TEAMS_ROOT=/srv/at\nAGENT_TEAMS_MAX_SESSIONS=6\nPATH=/x\n");
      const env = { HOME: home, AGENT_TEAMS_NODE_TOKEN: "tok-env" } as NodeJS.ProcessEnv;
      const c = loadConfig(["--root", "/srv/other"], env);
      expect(c).toMatchObject({ wsUrl: "wss://console.example/nodes/connect", token: "tok-env", root: "/srv/other", maxActive: 6, configDir: join(home, ".config/agent-teams") });
      expect(loadConfig(["--token=tok-flag", "--max-sessions", "2"], env)).toMatchObject({ token: "tok-flag", maxActive: 2 });
      // The retired v1 key is ignored: the default applies.
      writeFileSync(join(home, ".config/agent-teams/node.env"), "AGENT_TEAMS_CONSOLE=https://c\nAGENT_TEAMS_NODE_TOKEN=t\nAGENT_TEAMS_MAX_RUNS=6\n");
      expect(loadConfig([], { HOME: home }).maxActive).toBeUndefined();
      expect(() => loadConfig([], { HOME: join(home, "nowhere") })).toThrow(/AGENT_TEAMS_CONSOLE/);
      expect(() => loadConfig(["--bogus", "1"], env)).toThrow(/unknown option/);
      expect(loadConfig([], env).usageIntervalMs).toBeUndefined();
      expect(loadConfig([], { ...env, AGENT_TEAMS_USAGE_INTERVAL_S: "60" }).usageIntervalMs).toBe(60_000);
      expect(loadConfig([], { ...env, AGENT_TEAMS_USAGE_INTERVAL_S: "0" }).usageIntervalMs).toBe(0);
      expect(loadConfig([], { ...env, AGENT_TEAMS_USAGE_INTERVAL_S: "soon" }).usageIntervalMs).toBeUndefined();
    } finally {
      rmSync(home, { recursive: true, force: true });
    }
  });

  it("parses env files and console URLs", () => {
    expect(parseEnvFile(`# c\nexport A="1 2"\nB='x'\nbad line\nC=`)).toEqual({ A: "1 2", B: "x", C: "" });
    expect(toWsUrl("https://h")).toBe("wss://h/nodes/connect");
    expect(toWsUrl("http://127.0.0.1:3001")).toBe("ws://127.0.0.1:3001/nodes/connect");
    expect(toWsUrl("wss://h/nodes/v2/connect")).toBe("wss://h/nodes/v2/connect");
    expect(() => toWsUrl("ftp://h")).toThrow();
  });

  it("refuses plain-text console URLs except loopback or with AGENT_TEAMS_ALLOW_INSECURE", () => {
    expect(() => toWsUrl("http://h:3001")).toThrow(/plain-text/);
    expect(() => toWsUrl("ws://console.example/nodes/connect")).toThrow(/plain-text/);
    expect(() => toWsUrl("http://127.0.0.1.evil.example")).toThrow(/plain-text/);
    expect(() => toWsUrl("not a url")).toThrow(/invalid console URL/);
    expect(toWsUrl("http://localhost:3001")).toBe("ws://localhost:3001/nodes/connect");
    expect(toWsUrl("ws://[::1]:3001/nodes/connect")).toBe("ws://[::1]:3001/nodes/connect");
    expect(toWsUrl("http://h:3001", true)).toBe("ws://h:3001/nodes/connect");
    const env = { HOME: "/nonexistent", AGENT_TEAMS_ENV_FILE: "/nonexistent/node.env", AGENT_TEAMS_NODE_TOKEN: "t" };
    expect(() => loadConfig(["--console", "http://h:3001"], env)).toThrow(/plain-text/);
    expect(loadConfig(["--console", "http://127.0.0.1:3001"], env).warnings).toEqual([]);
    const insecure = loadConfig(["--console", "http://h:3001"], { ...env, AGENT_TEAMS_ALLOW_INSECURE: "1" });
    expect(insecure.wsUrl).toBe("ws://h:3001/nodes/connect");
    expect(insecure.warnings.join("\n")).toMatch(/clear text/);
    expect(() => loadConfig(["--console", "http://h:3001"], { ...env, AGENT_TEAMS_ALLOW_INSECURE: "0" })).toThrow(/plain-text/);
  });

  it("warns when the node token is given with --token", () => {
    const env = { HOME: "/nonexistent", AGENT_TEAMS_ENV_FILE: "/nonexistent/node.env", AGENT_TEAMS_CONSOLE: "https://c" };
    expect(loadConfig(["--token", "tok"], env).warnings.join("\n")).toMatch(/--token is visible/);
    expect(loadConfig([], { ...env, AGENT_TEAMS_NODE_TOKEN: "tok" }).warnings).toEqual([]);
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
