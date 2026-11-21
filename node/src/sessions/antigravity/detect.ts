// Antigravity CLI (agy) detection, each step a separate function (thread #41; thread #42's CLI
// detection framework can call them as they are): detectInstalled finds the binary, detectVersion
// runs `agy --version` (offline, no login), detectLogin reads env names and token-file metadata.
// Never `agy -p`: without a login it waits 60 s for an OAuth code. The ACP handshake (session/new
// answers -32000 when not logged in) is only a fallback and closes the session it opened.
import { execFile } from "node:child_process";
import { accessSync, constants, existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { delimiter, dirname, join } from "node:path";
import type { AntigravityRuntime } from "../../protocol.js";
import type { Logger } from "../../util/log.js";
import { AcpClient, AcpFailure, ACP_AUTH_REQUIRED } from "./acp.js";

export type AgyAuth = NonNullable<AntigravityRuntime["auth"]>;

/** Session-independent environment names agy reads (doc: Gemini API key, base URL, ADC / Vertex). */
export const AGY_PASS_ENV = ["GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL", "AGY_ADC_AUTH", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_QUOTA_PROJECT"];

/**
 * Files an OAuth login may leave under agy's HOME. Not verified: agy 1.5.14 was only run without a
 * login; it said "Using file-based token storage because container environment detected" and keeps
 * plugin-secrets.json in ~/.config/antigravity/. Any of these, non-empty, counts as logged in.
 */
export const AGY_TOKEN_FILES = [
  ".gemini/antigravity-cli/oauth_creds.json",
  ".gemini/antigravity-cli/credentials.json",
  ".gemini/antigravity-cli/token.json",
  ".gemini/oauth_creds.json",
  ".config/antigravity/oauth_creds.json",
  ".config/antigravity/credentials.json",
  ".config/antigravity/token.json",
];

/**
 * The agy executable: an explicit path (AGENT_TEAMS_AGY_BIN), else `agy` on PATH, else where the
 * install script puts it (~/.local/bin/agy of the daemon user or of agy's own HOME). null: not installed.
 */
export function detectInstalled(o: { explicit?: string; path?: string; homes?: string[] } = {}): string | null {
  const candidates = [
    ...(o.explicit ? [o.explicit] : []),
    ...(o.path ?? process.env.PATH ?? "").split(delimiter).filter(Boolean).map((d) => join(d, "agy")),
    ...(o.homes ?? [homedir()]).map((h) => join(h, ".local", "bin", "agy")),
  ];
  return candidates.find(executable) ?? null;
}

function executable(p: string): boolean {
  try {
    accessSync(p, constants.X_OK);
    return statSync(p).isFile();
  } catch {
    return false;
  }
}

/** `agy --version` (5 s, offline): "1.5.14", or null when it does not answer with a version. */
export function detectVersion(command: string[], env: Record<string, string>, timeoutMs = 5000): Promise<string | null> {
  const [cmd, ...pre] = command;
  return new Promise((resolve) => {
    execFile(cmd!, [...pre, "--version"], { env, timeout: timeoutMs, maxBuffer: 64 * 1024 }, (err, stdout) => {
      if (err) return resolve(null);
      resolve(/\d+\.\d+(\.\d+)?([-+][0-9A-Za-z.-]+)?/.exec(String(stdout))?.[0] ?? null);
    });
  });
}

/**
 * The login kind without a network call: GEMINI_API_KEY (with modelProvider gemini, which the
 * daemon's settings add), AGY_ADC_AUTH, or a non-empty token file under agy's HOME. Only file
 * metadata is read, never a token. null: none of them (maybe still logged in through the keyring).
 */
export function detectLogin(home: string, env: Record<string, string | undefined>): AgyAuth | null {
  if (env.GEMINI_API_KEY) return "api_key";
  if (env.AGY_ADC_AUTH && env.AGY_ADC_AUTH !== "false" && env.AGY_ADC_AUTH !== "0") return "adc";
  return AGY_TOKEN_FILES.some((f) => nonEmpty(join(home, f))) ? "oauth" : null;
}

function nonEmpty(p: string): boolean {
  try {
    const s = statSync(p);
    return s.isFile() && s.size > 0;
  } catch {
    return false;
  }
}

/**
 * Fallback login check over ACP: initialize + session/new in a scratch directory. -32000 means
 * not logged in; a session that opens is closed again (session/close) before agy exits.
 */
export async function detectLoginAcp(command: string[], env: Record<string, string>, cwd: string, log: Logger, timeoutMs = 15_000): Promise<AgyAuth> {
  const client = new AcpClient({ command, args: ["acp", "--process-per-session"], env, cwd, log });
  client.on("exit", () => undefined);
  try {
    await client.start();
    await client.request("initialize", { protocolVersion: 1, clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false } }, timeoutMs);
    const s = await client.request<{ sessionId?: string }>("session/new", { cwd, mcpServers: [] }, timeoutMs);
    if (s?.sessionId) await client.request("session/close", { sessionId: s.sessionId }, 3000).catch(() => undefined);
    return "oauth";
  } catch (err) {
    if (err instanceof AcpFailure && err.error.code === ACP_AUTH_REQUIRED) return "none";
    log.warn({ err: (err as Error).message }, "agy login check failed");
    return "none";
  } finally {
    client.stop();
  }
}

export interface AgyProbeOptions {
  /** agy's HOME (the node-level agy home; logins and settings live there). */
  home: string;
  env: Record<string, string>;
  /** The resolved agy command (detectInstalled), null when not installed. */
  command: string[] | null;
  log: Logger;
  /** Run the ACP fallback when no env or token file tells (default true). */
  acpFallback?: boolean;
}

/** hello.runtimes.antigravity: installed, version and login kind (reported even when not installed). */
export async function probeAntigravity(o: AgyProbeOptions): Promise<AntigravityRuntime> {
  if (!o.command) return { cli: "", auth: "none", installed: false };
  const cli = (await detectVersion(o.command, o.env)) ?? "";
  if (!cli) return { cli: "", auth: "none", installed: false };
  let auth = detectLogin(o.home, o.env);
  if (!auth && o.acpFallback !== false) auth = await detectLoginAcp(o.command, o.env, scratchDir(o.home), o.log);
  return { cli, auth: auth ?? "none", installed: true };
}

function scratchDir(home: string): string {
  const d = join(home, "probe");
  mkdirSync(d, { recursive: true, mode: 0o700 });
  return d;
}

/** agy's user settings file under its HOME (doc: ~/.gemini/antigravity-cli/settings.json). */
export function agySettingsFile(home: string): string {
  return join(home, ".gemini", "antigravity-cli", "settings.json");
}

/**
 * Pins the settings the daemon relies on, keeping whatever else an admin put there: every write
 * and command goes through a permission request (toolPermission request-review, agentMode not
 * auto), nothing outside the workspace, no surveys, the platform MCP tools pre-allowed, and
 * modelProvider gemini when an API key is used. Key names come from the docs (not yet verified
 * against a logged-in agy).
 */
export function writeAgySettings(home: string, env: Record<string, string | undefined>): void {
  const file = agySettingsFile(home);
  let cur: Record<string, unknown> = {};
  try {
    if (existsSync(file)) cur = JSON.parse(readFileSync(file, "utf8")) as Record<string, unknown>;
  } catch {
    cur = {};
  }
  const perms = (cur.permissions && typeof cur.permissions === "object" ? cur.permissions : {}) as Record<string, unknown>;
  const allow = Array.isArray(perms.allow) ? (perms.allow as unknown[]).filter((x) => typeof x === "string") as string[] : [];
  const next: Record<string, unknown> = {
    ...cur,
    agentMode: "",
    toolPermission: "request-review",
    allowNonWorkspaceAccess: false,
    showFeedbackSurvey: false,
    permissions: { ...perms, allow: allow.includes("mcp(platform/*)") ? allow : [...allow, "mcp(platform/*)"] },
    ...(env.GEMINI_API_KEY ? { modelProvider: "gemini" } : {}),
  };
  mkdirSync(dirname(file), { recursive: true, mode: 0o700 });
  writeFileSync(file, JSON.stringify(next, null, 2) + "\n", { mode: 0o600 });
}
