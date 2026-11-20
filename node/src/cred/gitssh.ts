import { spawn } from "node:child_process";
import { createPrivateKey, sign, type KeyObject } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AgentServer, keyLineBlob, sshString, SSH_AGENT_FAILURE, SSH_AGENT_IDENTITIES_ANSWER, SSH_AGENT_SIGN_RESPONSE, SSH_AGENTC_REQUEST_IDENTITIES, SSH_AGENTC_SIGN_REQUEST, WireReader } from "../ssh/agent.js";
import { shellQuote } from "../git/platform.js";
import { askSocket } from "./helper.js";

/**
 * `agent-teams-node git-ssh`: git's ssh (GIT_SSH_COMMAND) for standalone Git repositories over
 * SSH (git-repos.md §5). It asks the daemon for the repository's key with the session secret
 * (or, in a daemon git job, takes it from its own environment), keeps it in this process only,
 * serves it to ssh through a one-off agent socket in a private temp directory and pins the
 * upstream's host key. Anything that is not a standalone repository runs plain ssh unchanged.
 */

/** A key ssh may use for one connection. */
export interface GitSshKey {
  private_key: string; // PKCS#8 PEM
  public_key: string; // authorized_keys line
  known_hosts: string; // pinned host key line
}

/** ssh options that take a value (the destination is the first argument that is not one). */
const VALUE_FLAGS = new Set("BbcDEeFIiJLlmOopQRSWw".split(""));

export interface SshTarget {
  /** host[:port], the port only when not 22 (as the console names the repository's host). */
  host: string;
  /** Repository path from git's remote command, e.g. group/name.git. */
  path: string;
}

/** Where git is connecting: `[-p port] [-o …] [user@]host 'git-upload-pack '\''path'\'''`. */
export function parseSshArgs(args: string[]): SshTarget | null {
  let port = "";
  let i = 0;
  for (; i < args.length; i++) {
    const a = args[i]!;
    if (a === "--") {
      i++;
      break;
    }
    if (!a.startsWith("-") || a === "-") break;
    const flag = a[1]!;
    if (!VALUE_FLAGS.has(flag)) continue;
    const value = a.length > 2 ? a.slice(2) : args[++i];
    if (flag === "p" && value) port = value;
  }
  const dest = args[i];
  if (!dest) return null;
  const host = dest.includes("@") ? dest.slice(dest.lastIndexOf("@") + 1) : dest;
  const m = /^git-(?:upload|receive)-(?:pack|archive) '?(.+?)'?$/.exec(args.slice(i + 1).join(" ").trim());
  if (!host || !m) return null;
  const path = m[1]!.replace(/'\\''/g, "'").replace(/^~?\//, "");
  return { host: (port && port !== "22" ? `${host}:${port}` : host).toLowerCase(), path };
}

/** An in-memory key and its SSH public blob; signs what an agent client asks. */
export class HeldKey {
  private readonly priv: KeyObject;
  readonly publicBlob: Buffer;

  constructor(k: GitSshKey) {
    this.priv = createPrivateKey(k.private_key);
    const blob = keyLineBlob(k.public_key);
    if (!blob) throw new Error("bad public key");
    this.publicBlob = blob;
  }

  /** SSH signature blob for data (flags: SSH_AGENT_RSA_SHA2_256 = 2, _512 = 4). */
  sign(data: Buffer, flags: number): Buffer {
    const type = this.priv.asymmetricKeyType;
    if (type === "ed25519") return Buffer.concat([sshString("ssh-ed25519"), sshString(sign(null, data, this.priv))]);
    if (type === "rsa") {
      const [alg, hash] = flags & 4 ? ["rsa-sha2-512", "sha512"] : flags & 2 ? ["rsa-sha2-256", "sha256"] : ["ssh-rsa", "sha1"];
      return Buffer.concat([sshString(alg!), sshString(sign(hash!, data, this.priv))]);
    }
    if (type === "ec") {
      const curve = EC_CURVES[this.priv.asymmetricKeyDetails?.namedCurve ?? ""];
      if (!curve) throw new Error("unsupported curve");
      const sig = sign(curve.hash, data, { key: this.priv, dsaEncoding: "ieee-p1363" });
      const half = sig.length / 2;
      const body = Buffer.concat([sshString(mpint(sig.subarray(0, half))), sshString(mpint(sig.subarray(half)))]);
      return Buffer.concat([sshString(`ecdsa-sha2-${curve.name}`), sshString(body)]);
    }
    throw new Error(`unsupported key type ${type}`);
  }
}

const EC_CURVES: Record<string, { name: string; hash: string }> = {
  prime256v1: { name: "nistp256", hash: "sha256" },
  secp384r1: { name: "nistp384", hash: "sha384" },
  secp521r1: { name: "nistp521", hash: "sha512" },
};

/** SSH mpint body of an unsigned big-endian integer. */
function mpint(b: Buffer): Buffer {
  let i = 0;
  while (i < b.length - 1 && b[i] === 0) i++;
  const v = b.subarray(i);
  return v[0]! & 0x80 ? Buffer.concat([Buffer.from([0]), v]) : v;
}

/** Answers one agent request for key: its identity, and signatures with it only. */
export async function keyAgentMessage(msg: Buffer, key: HeldKey): Promise<Buffer> {
  const failure = Buffer.from([SSH_AGENT_FAILURE]);
  try {
    const r = new WireReader(msg);
    const type = r.byte();
    if (type === SSH_AGENTC_REQUEST_IDENTITIES) {
      const count = Buffer.alloc(4);
      count.writeUInt32BE(1);
      return Buffer.concat([Buffer.from([SSH_AGENT_IDENTITIES_ANSWER]), count, sshString(key.publicBlob), sshString("agent-teams git")]);
    }
    if (type === SSH_AGENTC_SIGN_REQUEST) {
      const blob = r.string();
      const data = r.string();
      const flags = r.uint32();
      if (!blob.equals(key.publicBlob)) return failure;
      return Buffer.concat([Buffer.from([SSH_AGENT_SIGN_RESPONSE]), sshString(key.sign(Buffer.from(data), flags))]);
    }
    return failure;
  } catch {
    return failure;
  }
}

/** ssh options for one connection with the held key: nothing else is offered, the host key is pinned. */
export function sshOptions(dir: string): string[] {
  const o = (k: string, v: string) => ["-o", `${k}=${v}`];
  return [
    ...o("IdentityAgent", join(dir, "agent.sock")), ...o("IdentityFile", join(dir, "key.pub")), ...o("IdentitiesOnly", "yes"),
    ...o("UserKnownHostsFile", join(dir, "known_hosts")), ...o("GlobalKnownHostsFile", "/dev/null"), ...o("StrictHostKeyChecking", "yes"),
    ...o("UpdateHostKeys", "no"), ...o("CheckHostIP", "no"), ...o("BatchMode", "yes"), ...o("PasswordAuthentication", "no"),
    ...o("KbdInteractiveAuthentication", "no"), ...o("ForwardAgent", "no"), ...o("ControlMaster", "no"), ...o("ControlPath", "none"),
  ];
}

/** GIT_SSH_COMMAND for the wrapper. */
export function gitSshCommand(nodeCmd: string[]): string {
  return [...nodeCmd, "git-ssh"].map(shellQuote).join(" ");
}

/** The key for target: a daemon git job's own (AT_GIT_SSH_*), else the session's from the daemon. */
export async function keyFor(target: SshTarget, env: NodeJS.ProcessEnv): Promise<GitSshKey | null> {
  if (env.AT_GIT_SSH_KEY && env.AT_GIT_SSH_PUB && env.AT_GIT_SSH_KNOWN_HOSTS) {
    return { private_key: env.AT_GIT_SSH_KEY, public_key: env.AT_GIT_SSH_PUB, known_hosts: env.AT_GIT_SSH_KNOWN_HOSTS };
  }
  if (!env.AT_CRED_SOCK || !env.AT_CRED_SESSION || !env.AT_CRED_SECRET) return null;
  const reply = await askSocket(env.AT_CRED_SOCK, { op: "ssh", session: env.AT_CRED_SESSION, secret: env.AT_CRED_SECRET, host: target.host, path: target.path }, 40_000);
  return reply.ok && reply.ssh_key ? reply.ssh_key : null;
}

/** The child's environment: without the session secret or a job's key. */
function childEnv(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const out = { ...env };
  for (const k of ["AT_CRED_SECRET", "AT_GIT_SSH_KEY", "AT_GIT_SSH_PUB", "AT_GIT_SSH_KNOWN_HOSTS"]) delete out[k];
  return out;
}

function runSsh(args: string[], env: NodeJS.ProcessEnv): Promise<number> {
  return new Promise((resolve) => {
    // "ssh" from PATH: the platform wrapper when the session has remote servers (it only adds
    // their Host blocks, which never match a Git host).
    const child = spawn("ssh", args, { stdio: "inherit", env });
    child.on("close", (code) => resolve(code ?? 255));
    child.on("error", () => resolve(127));
  });
}

export async function gitSshMain(args: string[], env: NodeJS.ProcessEnv): Promise<number> {
  const target = parseSshArgs(args);
  const raw = target ? await keyFor(target, env) : null;
  if (!raw) return runSsh(args, childEnv(env));
  let key: HeldKey;
  try {
    key = new HeldKey(raw);
  } catch (err) {
    process.stderr.write(`agent-teams: the repository's SSH key cannot be used: ${(err as Error).message}\n`);
    return 255;
  }
  const dir = mkdtempSync(join(tmpdir(), "agent-teams-git-ssh-"));
  const agent = new AgentServer(join(dir, "agent.sock"), (m) => keyAgentMessage(m, key));
  try {
    writeFileSync(join(dir, "key.pub"), raw.public_key.trim() + "\n", { mode: 0o600 });
    writeFileSync(join(dir, "known_hosts"), raw.known_hosts.trim() + "\n", { mode: 0o600 });
    await agent.start();
    return await runSsh([...sshOptions(dir), ...args], childEnv(env));
  } finally {
    agent.close();
    rmSync(dir, { recursive: true, force: true });
  }
}

/** host[:port] (port only when not the default) and path of a standalone repository's remote URL. */
export function upstreamOf(url: string): { transport: "https" | "ssh"; host: string; path: string } | null {
  const u = /^(https|ssh):\/\/(?:[^@/]+@)?([^/:]+)(?::(\d+))?\/(.+)$/.exec(url);
  if (u) {
    const def = u[1] === "https" ? "443" : "22";
    const host = (u[3] && u[3] !== def ? `${u[2]}:${u[3]}` : u[2]!).toLowerCase();
    return { transport: u[1] as "https" | "ssh", host, path: u[4]! };
  }
  const scp = /^[^@/]+@([^:/]+):(.+)$/.exec(url);
  return scp ? { transport: "ssh", host: scp[1]!.toLowerCase(), path: scp[2]! } : null;
}
