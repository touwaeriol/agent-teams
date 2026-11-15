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
  session: string;
  secret: string;
  host: string;
  path: string;
}

interface SessionEntry {
  secret: string;
  openGen: number;
}

/**
 * Local unix socket answering the git credential helper and gh wrapper
 * (architecture.md §10.4). Callers prove which session they belong to with the
 * per-session secret placed in the helper arguments; the socket directory is 0700.
 * Only read tokens are served here; push tokens exist only inside git.push jobs.
 */
export class CredServer {
  private server: Server | null = null;
  private readonly sessions = new Map<string, SessionEntry>();

  constructor(readonly socketPath: string, private readonly broker: CredBroker, private readonly log: Logger) {}

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

  unregister(sessionId: string): void {
    this.sessions.delete(sessionId);
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
    try {
      const cred = await this.broker.get({ session_id: req.session, open_gen: entry.openGen }, req.host, req.path, "read");
      return { ok: true, username: cred.username, password: cred.password };
    } catch (err) {
      this.log.info({ session: req.session, err: (err as Error).message }, "credential request refused");
      return { ok: false, error: (err as Error).message };
    }
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
