import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { DEFAULT_PASS_ENV } from "./sessions/env.js";
import type { AttachmentOptions } from "./sessions/attachments.js";

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
  /** Cap on concurrent session processes; undefined = no limit. */
  maxActive?: number;
  passEnv: string[];
  gitBaseUrl: string;
  claudeConfigDir: string;
  codexHome: string;
  /** HOME of the node's agy (Antigravity CLI) processes: its login and settings (AGENT_TEAMS_AGY_HOME). */
  agyHome: string;
  logLevel: string;
  /** node.usage period (AGENT_TEAMS_USAGE_INTERVAL_S; 0 disables); unset = default 5 min. */
  usageIntervalMs?: number;
  /** Startup warnings (insecure console URL allowed, token on the command line); the daemon logs them. */
  warnings: string[];
  /**
   * .attachments/ downloads (#58): AGENT_TEAMS_ATTACHMENT_RETENTION_H (hours a local copy is kept, default 24, 0 = forever),
   * AGENT_TEAMS_ATTACHMENT_RETENTION_FROM (last_use | download, default last_use), AGENT_TEAMS_ATTACHMENT_TIMEOUT_S
   * (total time for one attachment, all retries and fallbacks included, and for one write_shared_file upload; default 600), AGENT_TEAMS_ATTACHMENT_MIN_FREE_MB (disk space left after a download, default 512).
   */
  attachments?: Partial<AttachmentOptions>;
}

export class ConfigError extends Error {}

const FLAGS: Record<string, string> = {
  "--console": "AGENT_TEAMS_CONSOLE",
  "--token": "AGENT_TEAMS_NODE_TOKEN",
  "--root": "AGENT_TEAMS_ROOT",
  "--state": "AGENT_TEAMS_STATE",
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

const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"]);

/** True for localhost / 127.0.0.1 / ::1 (development and e2e consoles). */
export function isLoopbackHost(hostname: string): boolean {
  return LOOPBACK.has(hostname.toLowerCase());
}

/**
 * ws(s)://host/nodes/connect from http(s)://host or an explicit ws URL. Plain http/ws carries the
 * node token, every command and upgrade packages in clear text, so it is refused unless the host is
 * loopback or allowInsecure (AGENT_TEAMS_ALLOW_INSECURE=1) is set.
 */
export function toWsUrl(consoleUrl: string, allowInsecure = false): string {
  let u: URL;
  try {
    u = new URL(consoleUrl);
  } catch {
    throw new ConfigError(`invalid console URL ${consoleUrl}`);
  }
  if (u.protocol === "http:") u.protocol = "ws:";
  if (u.protocol === "https:") u.protocol = "wss:";
  if (u.protocol !== "ws:" && u.protocol !== "wss:") throw new ConfigError(`unsupported console URL ${consoleUrl}`);
  if (u.protocol === "ws:" && !allowInsecure && !isLoopbackHost(u.hostname)) {
    throw new ConfigError(`refusing plain-text console URL ${consoleUrl}: use https:// (or set AGENT_TEAMS_ALLOW_INSECURE=1 for a trusted network)`);
  }
  if (u.pathname === "/" || u.pathname === "") u.pathname = "/nodes/connect";
  return u.toString();
}

/** CLI flags > process env > ~/.config/agent-teams/node.env. */
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
  const allowInsecure = truthy(env.AGENT_TEAMS_ALLOW_INSECURE ?? file.AGENT_TEAMS_ALLOW_INSECURE);
  const wsUrl = toWsUrl(consoleUrl, allowInsecure);
  const warnings: string[] = [];
  if (wsUrl.startsWith("ws:") && !isLoopbackHost(new URL(wsUrl).hostname)) {
    warnings.push(`AGENT_TEAMS_ALLOW_INSECURE is set: the node token and all commands go to ${consoleUrl} in clear text`);
  }
  if (flags.AGENT_TEAMS_NODE_TOKEN !== undefined) {
    warnings.push("--token is visible to other users in the process list; put AGENT_TEAMS_NODE_TOKEN in the environment or in node.env instead");
  }
  const dataDir = env.AGENT_TEAMS_DATA ?? join(env.XDG_DATA_HOME ?? join(home, ".local", "share"), "agent-teams");
  return {
    consoleUrl, wsUrl, token, warnings,
    root: resolve(get("AGENT_TEAMS_ROOT") ?? join(home, ".agents_team")),
    stateDir: resolve(get("AGENT_TEAMS_STATE") ?? join(env.XDG_STATE_HOME ?? join(home, ".local", "state"), "agent-teams")),
    configDir, platformDir: join(dataDir, "platform"), installDir: dataDir,
    runtimeDir: join(env.XDG_RUNTIME_DIR ?? join(dataDir, "run"), "agent-teams"),
    maxActive: positiveInt(get("AGENT_TEAMS_MAX_SESSIONS")),
    passEnv: passList(get("AGENT_TEAMS_PASS_ENV")),
    gitBaseUrl: get("AGENT_TEAMS_GIT_BASE_URL") ?? "https://github.com",
    claudeConfigDir: resolve(env.CLAUDE_CONFIG_DIR ?? join(home, ".claude")),
    codexHome: resolve(env.CODEX_HOME ?? join(home, ".codex")),
    agyHome: resolve(get("AGENT_TEAMS_AGY_HOME") ?? join(dataDir, "agy-home")),
    logLevel: get("AGENT_TEAMS_LOG_LEVEL") ?? "info",
    ...usageInterval(get("AGENT_TEAMS_USAGE_INTERVAL_S")),
    attachments: attachmentOptions(get),
  };
}

function truthy(v: string | undefined): boolean {
  return v !== undefined && ["1", "true", "yes", "on"].includes(v.trim().toLowerCase());
}

/** The AGENT_TEAMS_ATTACHMENT_* settings that are set and valid. */
export function attachmentOptions(get: (k: string) => string | undefined): Partial<AttachmentOptions> {
  const out: Partial<AttachmentOptions> = {};
  const num = (k: string): number | undefined => {
    const v = get(k);
    const n = Number(v);
    return v !== undefined && v !== "" && Number.isFinite(n) && n >= 0 ? n : undefined;
  };
  const h = num("AGENT_TEAMS_ATTACHMENT_RETENTION_H");
  if (h !== undefined) out.retentionMs = Math.round(h * 3600_000);
  const from = get("AGENT_TEAMS_ATTACHMENT_RETENTION_FROM");
  if (from === "download" || from === "last_use") out.retentionFrom = from;
  const t = num("AGENT_TEAMS_ATTACHMENT_TIMEOUT_S");
  if (t) out.timeoutMs = Math.round(t * 1000);
  const f = num("AGENT_TEAMS_ATTACHMENT_MIN_FREE_MB");
  if (f !== undefined) out.minFreeBytes = Math.round(f * 1024 * 1024);
  return out;
}

function positiveInt(v: string | undefined): number | undefined {
  const n = Number(v);
  return Number.isInteger(n) && n > 0 ? n : undefined;
}

function usageInterval(v: string | undefined): { usageIntervalMs?: number } {
  const n = Number(v);
  return v !== undefined && v !== "" && Number.isInteger(n) && n >= 0 ? { usageIntervalMs: n * 1000 } : {};
}

function passList(v: string | undefined): string[] {
  if (!v) return DEFAULT_PASS_ENV;
  return v.split(/[,\s]+/).filter((x) => x && !x.startsWith("AGENT_TEAMS_"));
}
