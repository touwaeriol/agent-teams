import { spawn, spawnSync } from "node:child_process";
import { connect } from "node:net";
import { join } from "node:path";
import { existsSync, readdirSync } from "node:fs";
import { ghOnPath } from "../git/gh-install.js";
import type { CredSocketRequest } from "./server.js";
import { shellQuote } from "../git/platform.js";

/** GIT_CONFIG_* variables that make git ask this daemon for credentials (architecture.md §10.4). */
export function gitCredentialEnv(nodeCmd: string[], socket: string, session: string, secret: string): Record<string, string> {
  const helper = `!${[...nodeCmd, "git-credential", "--socket", socket, "--session", session, "--secret", secret].map(shellQuote).join(" ")}`;
  return {
    GIT_TERMINAL_PROMPT: "0",
    GIT_CONFIG_COUNT: "3",
    GIT_CONFIG_KEY_0: "credential.https://github.com.helper",
    GIT_CONFIG_VALUE_0: "",
    GIT_CONFIG_KEY_1: "credential.https://github.com.helper",
    GIT_CONFIG_VALUE_1: helper,
    GIT_CONFIG_KEY_2: "credential.https://github.com.useHttpPath",
    GIT_CONFIG_VALUE_2: "true",
    AT_CRED_SOCK: socket,
    AT_CRED_SESSION: session,
    AT_CRED_SECRET: secret,
  };
}

export function askSocket(socket: string, req: CredSocketRequest, timeoutMs = 11 * 60_000): Promise<SocketReply> {
  return new Promise((resolve) => {
    const sock = connect(socket);
    let buf = "";
    const timer = setTimeout(() => {
      sock.destroy();
      resolve({ ok: false, error: "timeout" });
    }, timeoutMs);
    sock.setEncoding("utf8");
    sock.on("connect", () => sock.write(JSON.stringify(req) + "\n"));
    sock.on("data", (d) => (buf += d));
    sock.on("end", () => {
      clearTimeout(timer);
      resolve(parseReply(buf));
    });
    sock.on("error", (err) => {
      clearTimeout(timer);
      resolve({ ok: false, error: err.message });
    });
  });
}

function parseReply(text: string): SocketReply {
  try {
    return JSON.parse(text);
  } catch {
    return { ok: false, error: "bad reply" };
  }
}

/** Parses git's credential protocol input (key=value lines). */
export function parseCredentialInput(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) out[line.slice(0, i)] = line.slice(i + 1);
  }
  return out;
}

/**
 * `agent-teams-node git-credential --socket S --session ID --secret X get|store|erase`.
 * get asks the daemon for a token; erase (git rejected the token) makes the daemon drop its
 * cached token for this session and repository, so the next get asks the console again;
 * store is ignored (nothing is ever written).
 */
export async function gitCredentialMain(args: Record<string, string>, op: string, stdin: string): Promise<{ out: string; code: number }> {
  if (op !== "get" && op !== "erase") return { out: "", code: 0 };
  const input = parseCredentialInput(stdin);
  if (input.protocol !== "https" || !input.host) return { out: "", code: 0 };
  if (op === "erase") {
    await askSocket(args.socket!, { op: "erase", session: args.session!, secret: args.secret!, host: input.host, path: input.path ?? "" }, 10_000);
    return { out: "", code: 0 };
  }
  const reply = await askSocket(args.socket!, { session: args.session!, secret: args.secret!, host: input.host, path: input.path ?? "" }, 40_000);
  if (!reply.ok) return { out: "quit=1\n", code: 0 };
  return { out: `username=${reply.username}\npassword=${reply.password}\n`, code: 0 };
}

export interface SocketReply {
  ok: boolean;
  username?: string;
  password?: string;
  error?: string;
  gh?: string;
  config_dir?: string;
  token_error?: string;
}

/**
 * `gh` wrapper (architecture.md §10.4): asks the daemon for the real gh (downloaded when the node
 * has none), an empty per-session config dir and a short-lived token for the repository gh will
 * talk to, then runs gh with GH_TOKEN/GITHUB_TOKEN only in that child's environment.
 */
export async function ghMain(argv: string[], env: NodeJS.ProcessEnv, cwd = process.cwd()): Promise<number> {
  const plan = await ghPlan(argv, env, cwd);
  if ("error" in plan) {
    process.stderr.write(`agent-teams: ${plan.error}\n`);
    return 127;
  }
  if (plan.note) process.stderr.write(`agent-teams: ${plan.note}\n`);
  return new Promise((resolve) => {
    const child = spawn(plan.bin, argv, { stdio: "inherit", env: plan.env, cwd });
    child.on("close", (code) => resolve(code ?? 1));
    child.on("error", () => resolve(127));
  });
}

export type GhPlan = { bin: string; env: NodeJS.ProcessEnv; note?: string } | { error: string };

/** What ghMain runs: the binary, its environment (with the token) and a note for stderr. */
export async function ghPlan(argv: string[], env: NodeJS.ProcessEnv, cwd: string): Promise<GhPlan> {
  if (!env.AT_CRED_SOCK) {
    const bin = ghOnPath(env.PATH ?? "", env.AT_GH_WRAPPER_DIR ? [env.AT_GH_WRAPPER_DIR] : []);
    return bin ? { bin, env } : { error: "gh is not installed on this node" };
  }
  const path = repoForGh(argv, env, cwd) ?? "";
  const reply: SocketReply = await askSocket(env.AT_CRED_SOCK, { op: "gh", session: env.AT_CRED_SESSION ?? "", secret: env.AT_CRED_SECRET ?? "", host: "github.com", path });
  if (!reply.ok || !reply.gh) return { error: reply.error ?? "gh is not available on this node" };
  const quiet = reply.password || NO_AUTH_COMMANDS.has(argv[0] ?? "");
  return { bin: reply.gh, env: ghChildEnv(env, reply), ...(quiet ? {} : { note: `no GitHub token (${reply.token_error ?? "refused"}); gh runs unauthenticated` }) };
}

/** gh commands that never need a token: no note about a missing one. */
const NO_AUTH_COMMANDS = new Set(["--version", "version", "help", "--help", "-h", "completion"]);

/** Token variables gh reads; an inherited one is dropped so gh only ever sees the narrowed token. */
const GH_TOKEN_VARS = ["GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"];

/**
 * The child's environment: never written anywhere, and without the socket secret. The token
 * (minted for github.com) goes only into GH_TOKEN/GITHUB_TOKEN, which gh sends to github.com
 * only; never into the enterprise variables, whatever GH_HOST says.
 */
export function ghChildEnv(env: NodeJS.ProcessEnv, reply: SocketReply): NodeJS.ProcessEnv {
  const out: NodeJS.ProcessEnv = { ...env, GH_HOST: env.GH_HOST ?? "github.com", GH_PROMPT_DISABLED: "1", GH_NO_UPDATE_NOTIFIER: "1", GH_NO_EXTENSION_UPDATE_NOTIFIER: "1" };
  for (const k of GH_TOKEN_VARS) delete out[k];
  if (reply.config_dir) out.GH_CONFIG_DIR = reply.config_dir;
  if (reply.password) {
    out.GH_TOKEN = reply.password;
    out.GITHUB_TOKEN = reply.password;
  }
  delete out.AT_CRED_SECRET;
  return out;
}

/**
 * The repository gh will act on, as owner/name.git: -R/--repo, then GH_REPO, then the cwd's
 * origin, then the only repository directly under the cwd (an employee directory with one checkout).
 */
export function repoForGh(argv: string[], env: NodeJS.ProcessEnv, cwd: string): string | null {
  const flag = repoFlag(argv) ?? env.GH_REPO;
  if (flag) return repoSlugPath(flag);
  const here = repoPathFromRemote(cwd);
  if (here) return here;
  const found = new Set<string>();
  for (const d of safeReaddir(cwd)) {
    const p = existsSync(join(cwd, d, ".git")) ? repoPathFromRemote(join(cwd, d)) : null;
    if (p) found.add(p);
  }
  return found.size === 1 ? [...found][0]! : null;
}

function repoFlag(argv: string[]): string | null {
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]!;
    if (a === "-R" || a === "--repo") return argv[i + 1] ?? null;
    if (a.startsWith("--repo=")) return a.slice(7);
    if (a.startsWith("-R") && a.length > 2) return a.slice(2);
  }
  return null;
}

/** owner/name.git from owner/name, HOST/owner/name or a URL. */
export function repoSlugPath(v: string): string | null {
  const url = githubPath(v);
  if (url) return url;
  const parts = v.replace(/\.git$/, "").split("/").filter(Boolean);
  return parts.length >= 2 ? `${parts.at(-2)}/${parts.at(-1)}.git` : null;
}

function safeReaddir(dir: string): string[] {
  try {
    return readdirSync(dir);
  } catch {
    return [];
  }
}

/** owner/name.git from `git remote get-url origin` when it points at github.com. */
export function repoPathFromRemote(cwd = process.cwd()): string | null {
  const r = spawnSync("git", ["-C", cwd, "remote", "get-url", "origin"], { encoding: "utf8" });
  return r.status === 0 ? githubPath(r.stdout.trim()) : null;
}

export function githubPath(url: string): string | null {
  const m = /github\.com[:/]([^/]+\/[^/]+?)(\.git)?\/?$/.exec(url);
  return m ? `${m[1]}.git` : null;
}
