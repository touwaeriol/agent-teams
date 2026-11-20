import { chmodSync, existsSync, mkdirSync, rmSync, statSync } from "node:fs";
import { createServer, type Server, type Socket } from "node:net";
import { dirname, join } from "node:path";
import { createHash, timingSafeEqual } from "node:crypto";
import { tmpdir } from "node:os";
import { randomSecret } from "../util/ids.js";
import type { Logger } from "../util/log.js";
import type { CredBroker } from "./broker.js";

/** Unix socket paths must fit sun_path (108 bytes on Linux, 104 on macOS). */
const MAX_SOCKET_PATH = 100;

/**
 * credSocketPath is <runtimeDir>/cred.sock, or a short per-runtime-dir path under the
 * system temp directory when that would not fit in sun_path (deep XDG_RUNTIME_DIR).
 */
export function credSocketPath(runtimeDir: string, tmp = tmpdir()): string {
  const direct = join(runtimeDir, "cred.sock");
  if (Buffer.byteLength(direct) <= MAX_SOCKET_PATH) return direct;
  const tag = createHash("sha256").update(runtimeDir).digest("hex").slice(0, 12);
  const uid = process.getuid?.() ?? 0;
  return join(tmp, `agent-teams-${uid}-${tag}`, "cred.sock");
}

export interface CredSocketRequest {
  /**
   * "cred" (default): a token for git; "gh": the real gh binary, its config dir and a token;
   * "erase": git rejected the token, drop the cached one for this session and repository;
   * "ssh": a standalone repository's SSH key for the git-ssh wrapper (git-repos.md §5).
   */
  op?: "cred" | "gh" | "erase" | "ssh";
  session: string;
  secret: string;
  host: string;
  path: string;
}

export interface CredServerOptions {
  /** Resolves (downloading when needed) the real gh binary. */
  gh?: () => Promise<string>;
  /** Per-session GH_CONFIG_DIR parent, outside every worktree. */
  ghConfigRoot?: string;
}

interface SessionEntry {
  secret: string;
  openGen: number;
}

/**
 * Local unix socket answering the git credential helper and gh wrapper
 * (architecture.md §10.4). Callers prove which session they belong to with the
 * per-session secret placed in the helper arguments; the socket directory is 0700.
 * The console decides the token's scope by the session's role: leads and members get one
 * that can push branches and open PRs on the project's repositories, the coordinator none.
 */
export class CredServer {
  private server: Server | null = null;
  private readonly sessions = new Map<string, SessionEntry>();

  constructor(readonly socketPath: string, private readonly broker: CredBroker, private readonly log: Logger, private readonly opts: CredServerOptions = {}) {}

  async start(): Promise<void> {
    const dir = dirname(this.socketPath);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    const uid = process.getuid?.();
    if (uid !== undefined && statSync(dir).uid !== uid) throw new Error(`${dir} belongs to another user`);
    chmodSync(dir, 0o700);
    if (existsSync(this.socketPath)) rmSync(this.socketPath);
    const server = createServer((sock) => this.onConnection(sock));
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(this.socketPath, () => {
        server.off("error", reject);
        resolve();
      });
    });
    chmodSync(this.socketPath, 0o600);
    this.server = server;
  }

  async stop(): Promise<void> {
    const s = this.server;
    this.server = null;
    if (s) await new Promise<void>((resolve) => s.close(() => resolve()));
    rmSync(this.socketPath, { force: true });
  }

  /** New secret for a session (each open); returns it for the helper arguments. */
  register(sessionId: string, openGen: number): string {
    const secret = randomSecret(24);
    this.sessions.set(sessionId, { secret, openGen });
    return secret;
  }

  /** The session's open_gen when the secret is right (ssh.sock uses the same per-session secret), else null. */
  verify(sessionId: string, secret: string): number | null {
    const entry = this.sessions.get(sessionId);
    return entry && safeEqual(entry.secret, secret) ? entry.openGen : null;
  }

  unregister(sessionId: string): void {
    this.sessions.delete(sessionId);
    this.broker.forget((s) => "session_id" in s && s.session_id === sessionId);
    const dir = this.ghConfigDir(sessionId);
    if (dir) rmSync(dir, { recursive: true, force: true });
  }

  /** Empty per-session gh config directory (gh writes nothing secret there: the token comes from GH_TOKEN). */
  private ghConfigDir(sessionId: string): string | null {
    if (!this.opts.ghConfigRoot) return null;
    return join(this.opts.ghConfigRoot, sessionId.replace(/[^A-Za-z0-9_.-]/g, "_"));
  }

  private onConnection(sock: Socket): void {
    let buf = "";
    sock.setEncoding("utf8");
    sock.on("data", (d) => {
      buf += d;
      if (buf.length > 64 * 1024) return void sock.destroy();
      const nl = buf.indexOf("\n");
      if (nl >= 0) void this.answer(sock, buf.slice(0, nl));
    });
    sock.on("error", () => sock.destroy());
  }

  private async answer(sock: Socket, line: string): Promise<void> {
    sock.end(JSON.stringify(await this.lookup(line)) + "\n");
  }

  private async lookup(line: string): Promise<Record<string, unknown>> {
    const req = parse(line);
    const entry = req ? this.sessions.get(req.session) : undefined;
    if (!req || !entry || !safeEqual(entry.secret, req.secret)) return { ok: false, error: "unknown session" };
    if (req.op === "erase") return this.eraseReply(req, entry);
    if (req.op === "ssh") return this.sshReply(req, entry);
    return req.op === "gh" ? this.ghReply(req, entry) : this.credReply(req, entry);
  }

  /**
   * git erase: the next get for this session and repository asks the console again, with
   * force_refresh so the console mints a new token rather than returning its cached one.
   */
  private eraseReply(req: CredSocketRequest, entry: SessionEntry): Record<string, unknown> {
    const dropped = this.broker.invalidate({ session_id: req.session, open_gen: entry.openGen }, req.host, req.path);
    this.log.info({ session: req.session, dropped }, "credential erased by git");
    return { ok: true };
  }

  /** The broker's token for one repository (the console decides the scope by the session's role, §10.4). */
  private async credReply(req: CredSocketRequest, entry: SessionEntry): Promise<Record<string, unknown>> {
    try {
      const cred = await this.broker.get({ session_id: req.session, open_gen: entry.openGen }, req.host, req.path, "read");
      if (!cred.password) return { ok: false, error: "no token for this repository" };
      return { ok: true, username: cred.username, password: cred.password };
    } catch (err) {
      this.log.info({ session: req.session, err: (err as Error).message }, "credential request refused");
      return { ok: false, error: (err as Error).message };
    }
  }

  /** git-ssh wrapper: the repository's key when the console hands one to this session (else plain ssh). */
  private async sshReply(req: CredSocketRequest, entry: SessionEntry): Promise<Record<string, unknown>> {
    try {
      const cred = await this.broker.get({ session_id: req.session, open_gen: entry.openGen }, req.host, req.path, "read");
      return cred.sshKey ? { ok: true, username: cred.username, ssh_key: cred.sshKey } : { ok: false, error: "no ssh key" };
    } catch (err) {
      return { ok: false, error: (err as Error).message };
    }
  }

  /** gh wrapper: the real binary and config dir always; the token when the repository is known and allowed. */
  private async ghReply(req: CredSocketRequest, entry: SessionEntry): Promise<Record<string, unknown>> {
    let gh: string;
    try {
      if (!this.opts.gh) throw new Error("gh is not configured on this node");
      gh = await this.opts.gh();
    } catch (err) {
      this.log.warn({ err: (err as Error).message }, "gh unavailable");
      return { ok: false, error: `gh is not available on this node: ${(err as Error).message}` };
    }
    const configDir = this.ghConfigDir(req.session);
    if (configDir) mkdirSync(configDir, { recursive: true, mode: 0o700 });
    const out: Record<string, unknown> = { ok: true, gh, ...(configDir ? { config_dir: configDir } : {}) };
    if (!req.path) return { ...out, token_error: "no repository: run gh inside a repository directory or pass -R owner/repo" };
    const cred = await this.credReply(req, entry);
    return cred.ok ? { ...out, password: cred.password } : { ...out, token_error: cred.error };
  }
}

function parse(line: string): CredSocketRequest | null {
  try {
    const v = JSON.parse(line) as CredSocketRequest;
    return typeof v.session === "string" && typeof v.secret === "string" ? v : null;
  } catch {
    return null;
  }
}

function safeEqual(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}
