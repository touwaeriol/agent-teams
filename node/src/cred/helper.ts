import { spawn, spawnSync } from "node:child_process";
import { connect } from "node:net";
import { delimiter, join } from "node:path";
import { existsSync } from "node:fs";
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

export function askSocket(socket: string, req: CredSocketRequest, timeoutMs = 40_000): Promise<{ ok: boolean; username?: string; password?: string; error?: string }> {
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

function parseReply(text: string): { ok: boolean; username?: string; password?: string; error?: string } {
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

/** `agent-teams-node git-credential --socket S --session ID --secret X get` */
export async function gitCredentialMain(args: Record<string, string>, op: string, stdin: string): Promise<{ out: string; code: number }> {
  if (op !== "get") return { out: "", code: 0 };
  const input = parseCredentialInput(stdin);
  if (input.protocol !== "https" || !input.host) return { out: "", code: 0 };
  const reply = await askSocket(args.socket!, { session: args.session!, secret: args.secret!, host: input.host, path: input.path ?? "" });
  if (!reply.ok) return { out: "quit=1\n", code: 0 };
  return { out: `username=${reply.username}\npassword=${reply.password}\n`, code: 0 };
}

/** `gh` wrapper: fetches a read token for the cwd's GitHub repo, then runs the real gh with GH_TOKEN. */
export async function ghMain(argv: string[], env: NodeJS.ProcessEnv): Promise<number> {
  const real = findRealGh(env.PATH ?? "", env.AT_GH_WRAPPER_DIR);
  if (!real) {
    process.stderr.write("gh is not installed on this node\n");
    return 127;
  }
  const path = repoPathFromRemote();
  const reply = env.AT_CRED_SOCK && path ? await askSocket(env.AT_CRED_SOCK, { session: env.AT_CRED_SESSION ?? "", secret: env.AT_CRED_SECRET ?? "", host: "github.com", path }) : { ok: false };
  const childEnv: NodeJS.ProcessEnv = { ...env, ...(reply.ok && reply.password ? { GH_TOKEN: reply.password } : {}) };
  delete childEnv.AT_CRED_SECRET;
  return new Promise((resolve) => {
    const child = spawn(real, argv, { stdio: "inherit", env: childEnv });
    child.on("close", (code) => resolve(code ?? 1));
    child.on("error", () => resolve(127));
  });
}

function findRealGh(pathVar: string, wrapperDir?: string): string | null {
  for (const dir of pathVar.split(delimiter)) {
    if (!dir || dir === wrapperDir) continue;
    const p = join(dir, "gh");
    if (existsSync(p)) return p;
  }
  return null;
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
