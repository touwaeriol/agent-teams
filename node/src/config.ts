import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { DEFAULT_PASS_ENV } from "./sessions/env.js";

export interface NodeConfig {
  consoleUrl: string;
  wsUrl: string;
  token: string;
  root: string;
  stateDir: string;
  configDir: string;
  platformDir: string;
  runtimeDir: string;
  installDir: string;
  maxActive: number;
  passEnv: string[];
  gitBaseUrl: string;
  claudeConfigDir: string;
  codexHome: string;
  logLevel: string;
}

export class ConfigError extends Error {}

const FLAGS: Record<string, string> = {
  "--console": "AGENT_TEAMS_CONSOLE",
  "--token": "AGENT_TEAMS_NODE_TOKEN",
  "--root": "AGENT_TEAMS_ROOT",
  "--state": "AGENT_TEAMS_STATE",
  "--max-runs": "AGENT_TEAMS_MAX_RUNS",
  "--max-sessions": "AGENT_TEAMS_MAX_SESSIONS",
  "--env-file": "AGENT_TEAMS_ENV_FILE",
  "--log-level": "AGENT_TEAMS_LOG_LEVEL",
  "--git-base-url": "AGENT_TEAMS_GIT_BASE_URL",
};

/** Parses KEY=VALUE lines (node.env written by install-node.sh); quotes are stripped. */
export function parseEnvFile(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const m = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$/.exec(line);
    if (m) out[m[1]!] = unquote(m[2]!.trim());
  }
  return out;
}

function unquote(v: string): string {
  if (v.length >= 2 && ((v[0] === '"' && v.at(-1) === '"') || (v[0] === "'" && v.at(-1) === "'"))) return v.slice(1, -1);
  return v;
}

/** --flag value / --flag=value pairs → env-style keys. */
export function parseFlags(argv: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (let i = 0; i < argv.length; i++) {
    const [flag, inline] = argv[i]!.split(/=(.*)/s, 2) as [string, string | undefined];
    const key = FLAGS[flag];
    if (!key) throw new ConfigError(`unknown option ${flag}`);
    const value = inline ?? argv[++i];
    if (value === undefined) throw new ConfigError(`${flag} needs a value`);
    out[key] = value;
  }
  return out;
}

/** ws(s)://host/nodes/connect from http(s)://host or an explicit ws URL. */
export function toWsUrl(consoleUrl: string): string {
  const u = new URL(consoleUrl);
  if (u.protocol === "http:") u.protocol = "ws:";
  if (u.protocol === "https:") u.protocol = "wss:";
  if (u.protocol !== "ws:" && u.protocol !== "wss:") throw new ConfigError(`unsupported console URL ${consoleUrl}`);
  if (u.pathname === "/" || u.pathname === "") u.pathname = "/nodes/connect";
  return u.toString();
}

/** CLI flags > process env > ~/.config/agent-teams/node.env (same keys as v1). */
export function loadConfig(argv: string[], env: NodeJS.ProcessEnv = process.env): NodeConfig {
  const flags = parseFlags(argv);
  const home = env.HOME ?? homedir();
  const configDir = join(env.XDG_CONFIG_HOME ?? join(home, ".config"), "agent-teams");
  const envFile = flags.AGENT_TEAMS_ENV_FILE ?? env.AGENT_TEAMS_ENV_FILE ?? join(configDir, "node.env");
  const file = existsSync(envFile) ? parseEnvFile(readFileSync(envFile, "utf8")) : {};
  const get = (k: string) => flags[k] ?? env[k] ?? file[k];
  const consoleUrl = get("AGENT_TEAMS_CONSOLE");
  const token = get("AGENT_TEAMS_NODE_TOKEN");
  if (!consoleUrl) throw new ConfigError("set --console or AGENT_TEAMS_CONSOLE");
  if (!token) throw new ConfigError("set AGENT_TEAMS_NODE_TOKEN (shown when you add the node in the console)");
  const dataDir = join(env.XDG_DATA_HOME ?? join(home, ".local", "share"), "agent-teams");
  return {
    consoleUrl, wsUrl: toWsUrl(consoleUrl), token,
    root: resolve(get("AGENT_TEAMS_ROOT") ?? join(home, ".agents_team")),
    stateDir: resolve(get("AGENT_TEAMS_STATE") ?? join(env.XDG_STATE_HOME ?? join(home, ".local", "state"), "agent-teams")),
    configDir, platformDir: join(dataDir, "platform"), installDir: join(dataDir, "node"),
    runtimeDir: join(env.XDG_RUNTIME_DIR ?? join(dataDir, "run"), "agent-teams"),
    maxActive: positiveInt(get("AGENT_TEAMS_MAX_SESSIONS") ?? get("AGENT_TEAMS_MAX_RUNS"), 4),
    passEnv: passList(get("AGENT_TEAMS_PASS_ENV")),
    gitBaseUrl: get("AGENT_TEAMS_GIT_BASE_URL") ?? "https://github.com",
    claudeConfigDir: resolve(env.CLAUDE_CONFIG_DIR ?? join(home, ".claude")),
    codexHome: resolve(env.CODEX_HOME ?? join(home, ".codex")),
    logLevel: get("AGENT_TEAMS_LOG_LEVEL") ?? "info",
  };
}

function positiveInt(v: string | undefined, fallback: number): number {
  const n = Number(v);
  return Number.isInteger(n) && n > 0 ? n : fallback;
}

function passList(v: string | undefined): string[] {
  if (!v) return DEFAULT_PASS_ENV;
  return v.split(/[,\s]+/).filter((x) => x && !x.startsWith("AGENT_TEAMS_"));
}
