import { rmSync } from "node:fs";
import { join } from "node:path";
import type { Duplex } from "node:stream";
import { WebSocket, createWebSocketStream } from "ws";
import type { SshCertRequest, SshCertResponse, SshServers } from "../protocol.js";
import { deferred, type Deferred } from "../util/async.js";
import { newId } from "../util/ids.js";
import type { Logger } from "../util/log.js";
import { AgentServer, keyLineBlob, SessionKey, type AgentIdentity } from "./agent.js";
import { clearSshDirs, ensurePrivateDir, renderSshConfig, sessionSshDir, writePrivateFileAtomic } from "./config.js";
import { ProxyServer, type ProxyRequest } from "./proxy.js";

/** Renew the certificate when less than this is left (remote-servers.md §4.4). */
const RENEW_MARGIN_MS = 3 * 60_000;
const CERT_RETRY_MS = 8_000;
const CERT_TRIES = 2;
const GATEWAY_TIMEOUT_MS = 20_000;

export interface SshHubOptions {
  /** Parent of every session directory (sshBaseDir). */
  baseDir: string;
  /** Shared ssh.sock the ProxyCommand connects to. */
  proxySocket: string;
  nodeCmd: string[];
  /** ws(s)://<console>/nodes/ssh */
  gatewayUrl: string;
  token: string;
  send: (type: string, body: object) => void;
  /** Checks a session's secret (CredServer.verify): its open_gen, or null. */
  verify: (sessionId: string, secret: string) => number | null;
  log: Logger;
  certRetryMs?: number;
  tmpDir?: string;
}

interface Cert {
  blob: Buffer;
  validBefore: number;
}

/** One driver lifetime of one session: its directory, in-memory key, certificate and pipes. */
class SshSession implements AgentIdentityProvider {
  readonly key = new SessionKey();
  readonly agent: AgentServer;
  cert: Cert | null = null;
  servers: string[];
  readonly pipes = new Set<Duplex>();
  private pending: Promise<AgentIdentity | null> | null = null;
  closed = false;
  /** Why the last certificate request failed (shown by ssh-proxy). */
  lastError = "";

  constructor(readonly id: string, readonly openGen: number, readonly dir: string, servers: string[], private readonly hub: SshHub) {
    this.servers = servers;
    this.agent = new AgentServer(join(dir, "agent.sock"), { key: this.key, identity: () => this.identity() });
  }

  get configPath(): string {
    return join(this.dir, "ssh_config");
  }

  get knownHostsPath(): string {
    return join(this.dir, "known_hosts");
  }

  /** The certificate, renewed (same key) when less than 3 minutes are left; concurrent asks share one request. */
  identity(): Promise<AgentIdentity | null> {
    if (this.closed) return Promise.resolve(null);
    const c = this.cert;
    if (c && c.validBefore - RENEW_MARGIN_MS > Date.now()) return Promise.resolve({ certBlob: c.blob, comment: `agent-teams ${this.id}` });
    this.pending ??= this.hub.requestCert(this).finally(() => (this.pending = null));
    return this.pending;
  }
}

interface AgentIdentityProvider {
  identity(): Promise<AgentIdentity | null>;
}

interface Waiting {
  wait: Deferred<SshCertResponse>;
  timer: NodeJS.Timeout;
  body: SshCertRequest;
  tries: number;
}

/**
 * Remote servers on the node side (remote-servers.md §4.2, §7): per session a 0700 directory
 * with ssh_config / known_hosts / agent.sock, an ephemeral Ed25519 key in memory, a
 * certificate asked from the console on first use, and the ssh.sock ProxyCommand bridge to
 * the console's /nodes/ssh. Nothing secret is written to disk or put into the session env.
 */
export class SshHub {
  private readonly sessions = new Map<string, SshSession>();
  private readonly waiting = new Map<string, Waiting>();
  /** Last applied ssh.servers_update version per session and open_gen. */
  private readonly versions = new Map<string, { gen: number; version: number }>();
  readonly proxy: ProxyServer;

  constructor(private readonly o: SshHubOptions) {
    this.proxy = new ProxyServer(o.proxySocket, (req) => this.openProxy(req));
  }

  /** Clears directories left by a previous process and listens on ssh.sock. */
  async start(): Promise<void> {
    clearSshDirs(this.o.baseDir, this.o.tmpDir);
    await this.proxy.start();
  }

  async stop(): Promise<void> {
    for (const id of [...this.sessions.keys()]) this.drop(id);
    await this.proxy.stop();
    for (const w of this.waiting.values()) clearTimeout(w.timer);
    this.waiting.clear();
  }

  /**
   * A driver is being created for the session: replaces any previous state (new key, no
   * certificate), writes ssh_config and starts the agent. Returns the ssh_config path.
   */
  open(sessionId: string, openGen: number, servers: SshServers): string {
    this.drop(sessionId);
    const dir = sessionSshDir(this.o.baseDir, sessionId, this.o.tmpDir);
    ensurePrivateDir(dir);
    const s = new SshSession(sessionId, openGen, dir, servers.map((x) => x.name), this);
    this.sessions.set(sessionId, s);
    writePrivateFileAtomic(s.knownHostsPath, "");
    this.writeConfig(s);
    s.agent.start().catch((err: Error) => this.o.log.warn({ session: sessionId, err: err.message }, "ssh agent failed to listen"));
    return s.configPath;
  }

  has(sessionId: string): boolean {
    return this.sessions.has(sessionId);
  }

  /** Driver gone (closed, parked, crashed, replaced): agent closed, key and certificate dropped, pipes cut, directory removed. */
  drop(sessionId: string): void {
    const s = this.sessions.get(sessionId);
    if (!s) return;
    this.sessions.delete(sessionId);
    s.closed = true;
    s.cert = null;
    s.agent.close();
    for (const p of s.pipes) p.destroy();
    s.pipes.clear();
    rmSync(s.dir, { recursive: true, force: true });
  }

  /** The session ended for good: forget its servers_update version too. */
  forget(sessionId: string): void {
    this.drop(sessionId);
    this.versions.delete(sessionId);
  }

  /**
   * ssh.servers_update: true when applied (newer version for this open_gen). A live session's
   * ssh_config is rewritten atomically and its certificate dropped (the next use asks again).
   */
  updateServers(sessionId: string, openGen: number, version: number, servers: SshServers): boolean {
    const last = this.versions.get(sessionId);
    if (last && last.gen === openGen && last.version >= version) return false;
    if (last && last.gen > openGen) return false;
    this.versions.set(sessionId, { gen: openGen, version });
    const s = this.sessions.get(sessionId);
    if (s && s.openGen === openGen) {
      s.servers = servers.map((x) => x.name);
      s.cert = null;
      this.writeConfig(s);
    }
    return true;
  }

  private writeConfig(s: SshSession): void {
    if (s.closed) return;
    if (s.servers.length === 0) {
      rmSync(s.configPath, { force: true });
      return;
    }
    writePrivateFileAtomic(s.configPath, renderSshConfig({ servers: s.servers, nodeCmd: this.o.nodeCmd, proxySocket: this.o.proxySocket, agentSocket: s.agent.socketPath, knownHosts: s.knownHostsPath }));
  }

  // ---- certificates ----

  /** Asks the console for a certificate for the session's key (ssh.cert_request); null when refused. */
  async requestCert(s: SshSession): Promise<AgentIdentity | null> {
    const res = await this.ask({ req_id: newId("sc_"), session_id: s.id, open_gen: s.openGen, public_key: s.key.authorizedKey });
    if (s.closed || this.sessions.get(s.id) !== s) return null;
    if (!res.ok || !res.certificate) {
      s.lastError = res.error ?? "refused";
      this.o.log.info({ session: s.id, err: res.error }, "ssh certificate refused");
      return null;
    }
    const blob = keyLineBlob(res.certificate);
    if (!blob) {
      s.lastError = "unreadable certificate";
      this.o.log.warn({ session: s.id }, "ssh certificate unreadable");
      return null;
    }
    s.lastError = "";
    const validBefore = res.valid_before ? Date.parse(res.valid_before) : Date.now() + 10 * 60_000;
    s.cert = { blob, validBefore };
    writePrivateFileAtomic(s.knownHostsPath, (res.known_hosts ?? []).filter((l) => !/[\r\n]/.test(l)).join("\n") + "\n");
    if (res.servers) {
      s.servers = res.servers.map((x) => x.name);
      this.writeConfig(s);
    }
    return { certBlob: blob, comment: `agent-teams ${s.id}` };
  }

  /** ssh.cert_response. */
  resolve(res: SshCertResponse): void {
    const w = this.waiting.get(res.req_id);
    if (!w) return;
    clearTimeout(w.timer);
    this.waiting.delete(res.req_id);
    w.wait.resolve(res);
  }

  private ask(body: SshCertRequest): Promise<SshCertResponse> {
    const retryMs = this.o.certRetryMs ?? CERT_RETRY_MS;
    const w: Waiting = { wait: deferred<SshCertResponse>(), timer: setTimeout(() => this.retry(body.req_id, retryMs), retryMs), body, tries: 1 };
    this.waiting.set(body.req_id, w);
    this.o.send("ssh.cert_request", body);
    return w.wait.promise;
  }

  private retry(reqId: string, retryMs: number): void {
    const w = this.waiting.get(reqId);
    if (!w) return;
    if (w.tries >= CERT_TRIES) {
      this.waiting.delete(reqId);
      return w.wait.resolve({ req_id: reqId, ok: false, error: "console did not answer" });
    }
    w.tries++;
    w.timer = setTimeout(() => this.retry(reqId, retryMs), retryMs);
    this.o.send("ssh.cert_request", w.body);
  }

  // ---- ProxyCommand bridge ----

  private async openProxy(req: ProxyRequest): Promise<{ stream: Duplex; done: () => void } | { error: string }> {
    const gen = this.o.verify(req.session, req.secret);
    const s = this.sessions.get(req.session);
    if (gen === null || !s) return { error: "unknown session (the session ended or has no remote servers)" };
    if (s.openGen !== gen) return { error: "stale session" };
    if (!s.servers.includes(req.server)) return { error: notAvailable(req.server, s.servers) };
    // ssh checks the gateway's host key before it asks the agent for an identity: get the
    // certificate (and with it known_hosts) now, and explain a refusal here rather than as
    // "Permission denied (publickey)".
    if (!(await s.identity())) return { error: `no session certificate: ${s.lastError || "the session ended"}` };
    if (!s.servers.includes(req.server)) return { error: notAvailable(req.server, s.servers) };
    const ws = await this.dialGateway(s, req.server);
    if ("error" in ws) return ws;
    if (s.closed) {
      ws.close();
      return { error: "session ended" };
    }
    const stream = createWebSocketStream(ws);
    s.pipes.add(stream);
    return { stream, done: () => s.pipes.delete(stream) };
  }

  private dialGateway(s: SshSession, server: string): Promise<WebSocket | { error: string }> {
    return new Promise((resolve) => {
      const ws = new WebSocket(this.o.gatewayUrl, "agent-teams-ssh.v1", {
        headers: { Authorization: `Bearer ${this.o.token}`, "X-AT-Session": s.id, "X-AT-Open-Gen": String(s.openGen), "X-AT-Server": server },
        perMessageDeflate: false, handshakeTimeout: GATEWAY_TIMEOUT_MS,
      });
      ws.once("open", () => resolve(ws));
      ws.once("unexpected-response", (_req, res) => {
        let body = "";
        res.setEncoding("utf8");
        res.on("data", (d: string) => (body += d.slice(0, 500)));
        res.on("end", () => resolve({ error: `the console refused the connection (${res.statusCode}${body ? `: ${body.trim().slice(0, 300)}` : ""})` }));
        res.on("error", () => resolve({ error: `the console refused the connection (${res.statusCode})` }));
      });
      ws.once("error", (err) => resolve({ error: `cannot reach the console SSH gateway: ${err.message}` }));
    });
  }
}

function notAvailable(server: string, servers: string[]): string {
  return `server ${server} is not available to this session; use one of: ${servers.join(", ") || "(none)"}`;
}

/** ws(s)://<console>/nodes/ssh from the daemon's link URL (…/nodes/connect or …/nodes/v2/connect). */
export function gatewayUrl(linkUrl: string): string {
  return linkUrl.replace(/\/nodes(\/v2)?\/connect(\?.*)?$/, "/nodes/ssh");
}
