import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, readdirSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { shellQuote } from "../git/platform.js";

/** Unix socket paths must fit sun_path (108 bytes on Linux, 104 on macOS); same margin as cred.sock. */
const MAX_SOCKET_PATH = 100;

/** The gateway's host key is pinned under this alias in every session's known_hosts (remote-servers.md §5.4). */
export const GATEWAY_HOST_ALIAS = "agent-teams-gateway";

/**
 * Parent of every session directory: $XDG_RUNTIME_DIR/agent-teams-ssh (tmpfs), else
 * <data>/run-ssh. Deliberately not runtimeDir, which the Claude sandbox denies (§7.2, §8.3).
 */
export function sshBaseDir(dataDir: string, env: NodeJS.ProcessEnv = process.env): string {
  return env.XDG_RUNTIME_DIR ? join(env.XDG_RUNTIME_DIR, "agent-teams-ssh") : join(dataDir, "run-ssh");
}

function hash12(s: string): string {
  return createHash("sha256").update(s).digest("hex").slice(0, 12);
}

/** Prefix of the short fallback parents under the temp directory (cleared at start). */
export function shortBasePrefix(uid = process.getuid?.() ?? 0): string {
  return `agent-teams-ssh-${uid}-`;
}

/**
 * <base>/<session>/ when <session dir>/agent.sock fits sun_path, else
 * <tmp>/agent-teams-ssh-<uid>-<sha(base)>/<sha(session)>/ (§7.2).
 */
export function sessionSshDir(base: string, sessionId: string, tmp = tmpdir()): string {
  const direct = join(base, sessionId.replace(/[^A-Za-z0-9_.-]/g, "_"));
  if (Buffer.byteLength(join(direct, "agent.sock")) <= MAX_SOCKET_PATH) return direct;
  return join(tmp, `${shortBasePrefix()}${hash12(base)}`, hash12(sessionId));
}

/** <runtimeDir>/ssh.sock, or a short temp path when that would not fit (same rule as credSocketPath). */
export function sshSocketPath(runtimeDir: string, tmp = tmpdir()): string {
  const direct = join(runtimeDir, "ssh.sock");
  if (Buffer.byteLength(direct) <= MAX_SOCKET_PATH) return direct;
  return join(tmp, `agent-teams-${process.getuid?.() ?? 0}-${hash12(runtimeDir)}`, "ssh.sock");
}

/** Creates a 0700 directory (and its parent) owned by this user; refuses one owned by someone else. */
export function ensurePrivateDir(dir: string): void {
  for (const d of [dirname(dir), dir]) {
    mkdirSync(d, { recursive: true, mode: 0o700 });
    const uid = process.getuid?.();
    if (uid !== undefined && statSync(d).uid !== uid) throw new Error(`${d} belongs to another user`);
    chmodSync(d, 0o700);
  }
}

/**
 * Removes every session directory a previous daemon left behind (private keys died with it):
 * the base directory and the short fallback parents under the temp directory (§7.7).
 */
export function clearSshDirs(base: string, tmp = tmpdir()): void {
  rmSync(base, { recursive: true, force: true });
  const prefix = shortBasePrefix();
  let names: string[] = [];
  try {
    names = readdirSync(tmp);
  } catch {
    return;
  }
  for (const n of names) if (n.startsWith(prefix)) rmSync(join(tmp, n), { recursive: true, force: true });
}

/** A value for ssh_config: % escaped (ssh expands tokens), double-quoted when it has spaces. */
function cfgPath(p: string): string {
  const v = p.replace(/%/g, "%%");
  return /[\s"]/.test(v) ? `"${v.replace(/"/g, '\\"')}"` : v;
}

export interface SshConfigInput {
  servers: string[];
  /** ProxyCommand prefix, e.g. [node, cli.mjs]. */
  nodeCmd: string[];
  proxySocket: string;
  agentSocket: string;
  knownHosts: string;
}

/**
 * The session's ssh_config (§7.2): one Host block per server, User = the server name (the
 * gateway takes the real username from the server's settings), ProxyCommand through this
 * daemon, the gateway pinned by HostKeyAlias. No secret is written: ssh-proxy reads the
 * session secret from its inherited environment.
 */
export function renderSshConfig(i: SshConfigInput): string {
  const names = i.servers.filter((n) => /^[a-z0-9][a-z0-9-]{0,62}$/.test(n));
  const lines = ["# agent-teams: servers this session may use; removed when the session ends"];
  for (const name of names) {
    const proxy = [...i.nodeCmd, "ssh-proxy", "--socket", i.proxySocket, "--server", name].map(shellQuote).join(" ").replace(/%/g, "%%");
    lines.push(`Host ${name}`, `  User ${name}`, `  HostKeyAlias ${GATEWAY_HOST_ALIAS}`, `  ProxyCommand ${proxy}`);
  }
  if (names.length) {
    lines.push(
      `Host ${names.join(" ")}`,
      `  IdentityAgent ${cfgPath(i.agentSocket)}`,
      `  UserKnownHostsFile ${cfgPath(i.knownHosts)}`,
      "  GlobalKnownHostsFile /dev/null",
      "  StrictHostKeyChecking yes",
      "  UpdateHostKeys no",
      "  CheckHostIP no",
      "  PasswordAuthentication no",
      "  KbdInteractiveAuthentication no",
      "  ForwardAgent no",
      "  ForwardX11 no",
      "  ControlMaster no",
      "  ControlPath none",
      "  ServerAliveInterval 30",
    );
  }
  return lines.join("\n") + "\n";
}

/** Writes a file atomically (temp file + rename), 0600. */
export function writePrivateFileAtomic(path: string, body: string): void {
  const tmp = `${path}.${process.pid}.tmp`;
  writeFileSync(tmp, body, { mode: 0o600 });
  chmodSync(tmp, 0o600);
  renameSync(tmp, path);
}
