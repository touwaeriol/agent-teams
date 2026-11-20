import { generateKeyPairSync, sign, type KeyObject } from "node:crypto";
import { chmodSync, rmSync } from "node:fs";
import { createServer, type Server, type Socket } from "node:net";

/**
 * A minimal ssh-agent (draft-miller-ssh-agent) for one session (remote-servers.md §4.2):
 * the Ed25519 private key lives only in this process, ssh reaches it through IdentityAgent.
 * Only REQUEST_IDENTITIES (answers with the session certificate) and SIGN_REQUEST (for that
 * certificate or its plain public key) are served; every other request gets SSH_AGENT_FAILURE.
 */

export const SSH_AGENT_FAILURE = 5;
export const SSH_AGENTC_REQUEST_IDENTITIES = 11;
export const SSH_AGENT_IDENTITIES_ANSWER = 12;
export const SSH_AGENTC_SIGN_REQUEST = 13;
export const SSH_AGENT_SIGN_RESPONSE = 14;

/** A sane bound on one agent message (OpenSSH uses 256 KiB). */
const MAX_MESSAGE = 256 * 1024;

export function sshString(b: Buffer | string): Buffer {
  const body = typeof b === "string" ? Buffer.from(b) : b;
  const len = Buffer.alloc(4);
  len.writeUInt32BE(body.length);
  return Buffer.concat([len, body]);
}

export function frame(payload: Buffer): Buffer {
  return sshString(payload);
}

/** Reads SSH wire strings and integers from a buffer; throws on truncation. */
export class WireReader {
  private off = 0;
  constructor(private readonly b: Buffer) {}
  byte(): number {
    if (this.off + 1 > this.b.length) throw new Error("truncated");
    return this.b[this.off++]!;
  }
  uint32(): number {
    if (this.off + 4 > this.b.length) throw new Error("truncated");
    const v = this.b.readUInt32BE(this.off);
    this.off += 4;
    return v;
  }
  string(): Buffer {
    const n = this.uint32();
    if (this.off + n > this.b.length) throw new Error("truncated");
    const v = this.b.subarray(this.off, this.off + n);
    this.off += n;
    return v;
  }
}

/** Splits a byte stream into agent messages (uint32 length + payload). */
export class FrameDecoder {
  private buf: Buffer = Buffer.alloc(0);
  push(chunk: Buffer): Buffer[] {
    this.buf = this.buf.length ? Buffer.concat([this.buf, chunk]) : chunk;
    const out: Buffer[] = [];
    for (;;) {
      if (this.buf.length < 4) break;
      const n = this.buf.readUInt32BE(0);
      if (n === 0 || n > MAX_MESSAGE) throw new Error(`bad agent message length ${n}`);
      if (this.buf.length < 4 + n) break;
      out.push(this.buf.subarray(4, 4 + n));
      this.buf = this.buf.subarray(4 + n);
    }
    return out;
  }
}

/** An in-memory Ed25519 key pair; the private half never leaves this object. */
export class SessionKey {
  private readonly priv: KeyObject;
  /** The public key blob: string "ssh-ed25519" + string(32-byte key). */
  readonly publicBlob: Buffer;

  constructor() {
    const { privateKey, publicKey } = generateKeyPairSync("ed25519");
    this.priv = privateKey;
    const raw = Buffer.from(publicKey.export({ format: "jwk" }).x!, "base64url");
    this.publicBlob = Buffer.concat([sshString("ssh-ed25519"), sshString(raw)]);
  }

  /** authorized_keys line for ssh.cert_request. */
  get authorizedKey(): string {
    return `ssh-ed25519 ${this.publicBlob.toString("base64")}`;
  }

  /** SSH signature blob: string "ssh-ed25519" + string(64-byte signature). */
  sign(data: Buffer): Buffer {
    return Buffer.concat([sshString("ssh-ed25519"), sshString(sign(null, data, this.priv))]);
  }
}

/** Decodes the blob of one OpenSSH public key / certificate line ("type base64 [comment]"). */
export function keyLineBlob(line: string): Buffer | null {
  const parts = line.trim().split(/\s+/);
  if (parts.length < 2 || !/^[A-Za-z0-9+/=]+$/.test(parts[1]!)) return null;
  const blob = Buffer.from(parts[1]!, "base64");
  try {
    if (new WireReader(blob).string().toString() !== parts[0]) return null;
  } catch {
    return null;
  }
  return blob;
}

export interface AgentIdentity {
  /** The certificate blob offered to ssh. */
  certBlob: Buffer;
  comment: string;
}

export interface AgentBackend {
  key: SessionKey;
  /** The current certificate (asking the console for one when needed); null: none (ssh then fails auth). */
  identity(): Promise<AgentIdentity | null>;
}

/** Answers one agent request; never throws (a malformed request gets SSH_AGENT_FAILURE). */
export async function handleAgentMessage(msg: Buffer, backend: AgentBackend): Promise<Buffer> {
  const failure = Buffer.from([SSH_AGENT_FAILURE]);
  try {
    const r = new WireReader(msg);
    const type = r.byte();
    if (type === SSH_AGENTC_REQUEST_IDENTITIES) {
      const id = await backend.identity();
      const count = Buffer.alloc(4);
      count.writeUInt32BE(id ? 1 : 0);
      return Buffer.concat([Buffer.from([SSH_AGENT_IDENTITIES_ANSWER]), count, ...(id ? [sshString(id.certBlob), sshString(id.comment)] : [])]);
    }
    if (type === SSH_AGENTC_SIGN_REQUEST) {
      const blob = r.string();
      const data = r.string();
      const flags = r.uint32();
      // flags ask for RSA SHA-2 variants; for Ed25519 they must be 0.
      if (flags !== 0) return failure;
      const id = await backend.identity();
      const known = blob.equals(backend.key.publicBlob) || (id !== null && blob.equals(id.certBlob));
      if (!known) return failure;
      return Buffer.concat([Buffer.from([SSH_AGENT_SIGN_RESPONSE]), sshString(backend.key.sign(Buffer.from(data)))]);
    }
    return failure;
  } catch {
    return failure;
  }
}

/** Listens on <dir>/agent.sock (0600) for one session; requests on a connection are answered in order. */
export class AgentServer {
  private server: Server | null = null;
  private readonly conns = new Set<Socket>();
  private readonly handle: (msg: Buffer) => Promise<Buffer>;

  /** backend: the session key and certificate, or any handler (the git-ssh wrapper's held key). */
  constructor(readonly socketPath: string, backend: AgentBackend | ((msg: Buffer) => Promise<Buffer>)) {
    this.handle = typeof backend === "function" ? backend : (m) => handleAgentMessage(m, backend);
  }

  async start(): Promise<void> {
    rmSync(this.socketPath, { force: true });
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

  close(): void {
    const s = this.server;
    this.server = null;
    for (const c of this.conns) c.destroy();
    this.conns.clear();
    s?.close();
    rmSync(this.socketPath, { force: true });
  }

  private onConnection(sock: Socket): void {
    this.conns.add(sock);
    const dec = new FrameDecoder();
    let chain = Promise.resolve();
    sock.on("data", (d: Buffer) => {
      let msgs: Buffer[];
      try {
        msgs = dec.push(d);
      } catch {
        return void sock.destroy();
      }
      for (const m of msgs) {
        chain = chain.then(async () => {
          const reply = await this.handle(m);
          if (!sock.destroyed) sock.write(frame(reply));
        });
      }
    });
    sock.on("error", () => sock.destroy());
    sock.on("close", () => this.conns.delete(sock));
  }
}
