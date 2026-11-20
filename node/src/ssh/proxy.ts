import { chmodSync, existsSync, rmSync } from "node:fs";
import { connect, createServer, type Server, type Socket } from "node:net";
import { dirname } from "node:path";
import type { Duplex } from "node:stream";
import { ensurePrivateDir } from "./config.js";

/** First line ssh-proxy sends on ssh.sock; after an ok reply the connection is the raw SSH stream. */
export interface ProxyRequest {
  op: "ssh";
  session: string;
  secret: string;
  server: string;
}

export interface ProxyReply {
  ok: boolean;
  error?: string;
}

/**
 * Decides a proxy request: checks the session secret and that the server is one the session
 * may use, then opens the gateway WebSocket. Resolves to the stream to pipe, or an error text.
 */
export type ProxyOpener = (req: ProxyRequest) => Promise<{ stream: Duplex; done: () => void } | { error: string }>;

const MAX_LINE = 4096;

/**
 * ssh.sock (remote-servers.md §6.4, §7.2): ProxyCommand connects, sends one JSON line, and
 * after {"ok":true} the unix connection is piped to a new /nodes/ssh WebSocket.
 */
export class ProxyServer {
  private server: Server | null = null;

  constructor(readonly socketPath: string, private readonly open: ProxyOpener) {}

  async start(): Promise<void> {
    ensurePrivateDir(dirname(this.socketPath));
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

  private onConnection(sock: Socket): void {
    let buf = Buffer.alloc(0);
    const onData = (d: Buffer) => {
      buf = Buffer.concat([buf, d]);
      const nl = buf.indexOf(0x0a);
      if (nl < 0) {
        if (buf.length > MAX_LINE) sock.destroy();
        return;
      }
      sock.off("data", onData);
      sock.pause();
      const rest = buf.subarray(nl + 1);
      void this.answer(sock, buf.subarray(0, nl).toString("utf8"), rest);
    };
    sock.on("data", onData);
    sock.on("error", () => sock.destroy());
  }

  private async answer(sock: Socket, line: string, rest: Buffer): Promise<void> {
    const req = parseRequest(line);
    const res = req ? await this.open(req).catch((err: Error) => ({ error: err.message })) : { error: "bad request" };
    if ("error" in res) {
      sock.end(JSON.stringify({ ok: false, error: res.error } satisfies ProxyReply) + "\n");
      return;
    }
    if (sock.destroyed) {
      res.stream.destroy();
      res.done();
      return;
    }
    sock.write(JSON.stringify({ ok: true } satisfies ProxyReply) + "\n");
    pipeBoth(sock, res.stream, rest, res.done);
  }
}

/** Pipes a and b both ways (with backpressure); either side closing or failing tears both down. */
export function pipeBoth(a: Socket, b: Duplex, early: Buffer, done: () => void): void {
  let finished = false;
  const finish = () => {
    if (finished) return;
    finished = true;
    a.destroy();
    b.destroy();
    done();
  };
  if (early.length) b.write(early);
  a.pipe(b);
  b.pipe(a);
  a.resume();
  for (const s of [a, b]) {
    s.on("error", finish);
    s.on("close", finish);
  }
  a.on("end", () => b.end());
  b.on("end", () => a.end());
}

function parseRequest(line: string): ProxyRequest | null {
  try {
    const v = JSON.parse(line) as ProxyRequest;
    return v && v.op === "ssh" && typeof v.session === "string" && typeof v.secret === "string" && typeof v.server === "string" ? v : null;
  } catch {
    return null;
  }
}

/**
 * `agent-teams-node ssh-proxy --socket S --server NAME` (ssh's ProxyCommand): the session and
 * its secret come from the inherited environment (AT_CRED_SESSION / AT_CRED_SECRET), never from
 * arguments. Pipes stdin/stdout to the gateway; on refusal writes the reason to stderr and exits 1.
 */
export async function sshProxyMain(argv: string[], env: NodeJS.ProcessEnv, io: { stdin: NodeJS.ReadableStream; stdout: NodeJS.WritableStream; stderr: NodeJS.WritableStream } = process): Promise<number> {
  const args: Record<string, string> = {};
  for (let i = 0; i < argv.length; i += 2) args[argv[i]!.replace(/^--/, "")] = argv[i + 1] ?? "";
  const fail = (msg: string) => {
    io.stderr.write(`agent-teams ssh: ${msg}\n`);
    return 1;
  };
  if (!args.socket || !args.server) return fail("usage: ssh-proxy --socket PATH --server NAME");
  if (!env.AT_CRED_SESSION || !env.AT_CRED_SECRET) return fail("this shell is not an agent-teams session with remote servers (AT_CRED_SESSION missing)");
  const req: ProxyRequest = { op: "ssh", session: env.AT_CRED_SESSION, secret: env.AT_CRED_SECRET, server: args.server };
  return new Promise<number>((resolve) => {
    const sock = connect(args.socket!);
    let buf = Buffer.alloc(0);
    let piping = false;
    sock.on("connect", () => sock.write(JSON.stringify(req) + "\n"));
    const onData = (d: Buffer) => {
      buf = Buffer.concat([buf, d]);
      const nl = buf.indexOf(0x0a);
      if (nl < 0) return;
      sock.off("data", onData);
      let reply: ProxyReply;
      try {
        reply = JSON.parse(buf.subarray(0, nl).toString("utf8"));
      } catch {
        reply = { ok: false, error: "bad reply from the node daemon" };
      }
      if (!reply.ok) {
        sock.destroy();
        return resolve(fail(reply.error ?? "refused"));
      }
      piping = true;
      const rest = buf.subarray(nl + 1);
      if (rest.length) io.stdout.write(rest);
      sock.pipe(io.stdout as NodeJS.WritableStream);
      io.stdin.pipe(sock);
      io.stdin.on("end", () => sock.end());
    };
    sock.on("data", onData);
    sock.on("error", (err) => {
      if (!piping) return resolve(fail(`cannot reach the node daemon: ${err.message}`));
      resolve(0);
    });
    sock.on("close", () => resolve(piping ? 0 : fail("the node daemon closed the connection")));
  });
}
