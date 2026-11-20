import { spawn, spawnSync } from "node:child_process";
import { createPublicKey, verify } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { connect } from "node:net";
import { tmpdir, userInfo } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { WebSocket } from "ws";
import {
  AgentServer, FrameDecoder, frame, handleAgentMessage, keyLineBlob, SessionKey, sshString, WireReader,
  SSH_AGENT_FAILURE, SSH_AGENT_IDENTITIES_ANSWER, SSH_AGENT_SIGN_RESPONSE, SSH_AGENTC_REQUEST_IDENTITIES, SSH_AGENTC_SIGN_REQUEST,
} from "../src/ssh/agent.js";
import { clearSshDirs, renderSshConfig, sessionSshDir, shortBasePrefix, sshSocketPath } from "../src/ssh/config.js";
import { gatewayUrl } from "../src/ssh/hub.js";
import { sshProxyMain } from "../src/ssh/proxy.js";
import { sshWrapper } from "../src/git/platform.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

const hasSshKeygen = spawnSync("ssh-keygen", ["-?"], { encoding: "utf8" }).error === undefined;
const hasSsh = spawnSync("ssh", ["-V"], { encoding: "utf8" }).error === undefined;
const SSHD = "/usr/sbin/sshd";
const USER = userInfo().username;

let dir: string;
beforeEach(() => (dir = mkdtempSync(join(tmpdir(), "at-ssh-"))));
afterEach(() => rmSync(dir, { recursive: true, force: true }));

/** A CA and a certificate for `pub` (authorized_keys line) signed by it with ssh-keygen. */
function signCert(work: string, pub: string, principals: string[]): string {
  if (!existsSync(join(work, "ca"))) spawnSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-f", join(work, "ca")]);
  writeFileSync(join(work, "sess.pub"), pub + "\n");
  const r = spawnSync("ssh-keygen", ["-q", "-s", join(work, "ca"), "-I", "s_test", "-n", principals.join(","), "-V", "-1m:+10m", join(work, "sess.pub")], { encoding: "utf8" });
  if (r.status !== 0) throw new Error(r.stderr);
  return readFileSync(join(work, "sess-cert.pub"), "utf8").trim();
}

function agentRequest(socket: string, payload: Buffer): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const sock = connect(socket);
    const dec = new FrameDecoder();
    sock.on("connect", () => sock.write(frame(payload)));
    sock.on("data", (d: Buffer) => {
      const m = dec.push(d);
      if (m.length) {
        sock.destroy();
        resolve(m[0]!);
      }
    });
    sock.on("error", reject);
  });
}

describe("ssh-agent protocol", () => {
  it("lists only the certificate and signs for it or its public key; everything else fails", async () => {
    const key = new SessionKey();
    const certBlob = Buffer.concat([sshString("ssh-ed25519-cert-v01@openssh.com"), sshString("fake")]);
    const backend = { key, identity: async () => ({ certBlob, comment: "c" }) };

    const ids = new WireReader(await handleAgentMessage(Buffer.from([SSH_AGENTC_REQUEST_IDENTITIES]), backend));
    expect(ids.byte()).toBe(SSH_AGENT_IDENTITIES_ANSWER);
    expect(ids.uint32()).toBe(1);
    expect(ids.string().equals(certBlob)).toBe(true);

    const data = Buffer.from("session-hash");
    for (const blob of [certBlob, key.publicBlob]) {
      const reply = new WireReader(await handleAgentMessage(Buffer.concat([Buffer.from([SSH_AGENTC_SIGN_REQUEST]), sshString(blob), sshString(data), Buffer.alloc(4)]), backend));
      expect(reply.byte()).toBe(SSH_AGENT_SIGN_RESPONSE);
      const sig = new WireReader(reply.string());
      expect(sig.string().toString()).toBe("ssh-ed25519");
      const raw = new WireReader(key.publicBlob);
      raw.string();
      const pub = createPublicKey({ key: { kty: "OKP", crv: "Ed25519", x: raw.string().toString("base64url") }, format: "jwk" });
      expect(verify(null, data, pub, sig.string())).toBe(true);
    }

    const other = new SessionKey();
    const fail = Buffer.from([SSH_AGENT_FAILURE]);
    expect(await handleAgentMessage(Buffer.concat([Buffer.from([SSH_AGENTC_SIGN_REQUEST]), sshString(other.publicBlob), sshString(data), Buffer.alloc(4)]), backend)).toEqual(fail);
    const flags = Buffer.alloc(4);
    flags.writeUInt32BE(2);
    expect(await handleAgentMessage(Buffer.concat([Buffer.from([SSH_AGENTC_SIGN_REQUEST]), sshString(key.publicBlob), sshString(data), flags]), backend)).toEqual(fail);
    for (const t of [17, 18, 19, 22, 25, 27]) expect(await handleAgentMessage(Buffer.from([t, 0, 0, 0, 0]), backend)).toEqual(fail); // add/remove/lock/extension
    expect(await handleAgentMessage(Buffer.from([SSH_AGENTC_SIGN_REQUEST, 0, 0]), backend)).toEqual(fail); // truncated

    const none = new WireReader(await handleAgentMessage(Buffer.from([SSH_AGENTC_REQUEST_IDENTITIES]), { key, identity: async () => null }));
    expect(none.byte()).toBe(SSH_AGENT_IDENTITIES_ANSWER);
    expect(none.uint32()).toBe(0);
  });

  it("splits frames across chunks and rejects absurd lengths", () => {
    const dec = new FrameDecoder();
    const both = Buffer.concat([frame(Buffer.from([11])), frame(Buffer.from([13, 1, 2]))]);
    expect(dec.push(both.subarray(0, 3))).toEqual([]);
    expect(dec.push(both.subarray(3, 7)).map((b) => [...b])).toEqual([[11]]);
    expect(dec.push(both.subarray(7)).map((b) => [...b])).toEqual([[13, 1, 2]]);
    expect(() => new FrameDecoder().push(Buffer.from([0xff, 0, 0, 0]))).toThrow();
  });

  it.skipIf(!hasSshKeygen)("serves a real certificate to ssh-add over the unix socket (0600)", async () => {
    const key = new SessionKey();
    const cert = signCert(dir, key.authorizedKey, ["prod"]);
    const blob = keyLineBlob(cert)!;
    expect(blob).not.toBeNull();
    const agent = new AgentServer(join(dir, "agent.sock"), { key, identity: async () => ({ certBlob: blob, comment: "agent-teams s_1" }) });
    await agent.start();
    try {
      expect(statSync(agent.socketPath).mode & 0o777).toBe(0o600);
      // Async: the agent answers from this very process.
      const agentEnv = { PATH: process.env.PATH ?? "/usr/bin:/bin", SSH_AUTH_SOCK: agent.socketPath };
      const r = await run("ssh-add", ["-L"], agentEnv);
      expect(r.out.trim().split(" ").slice(0, 2).join(" ")).toBe(cert.split(" ").slice(0, 2).join(" "));
      expect((await run("ssh-add", ["-D"], agentEnv)).code).not.toBe(0);
      const answer = new WireReader(await agentRequest(agent.socketPath, Buffer.from([SSH_AGENTC_REQUEST_IDENTITIES])));
      expect(answer.byte()).toBe(SSH_AGENT_IDENTITIES_ANSWER);
    } finally {
      agent.close();
    }
    expect(existsSync(agent.socketPath)).toBe(false);
  });
});

describe("ssh_config and directories", () => {
  it("renders one Host block per server, pinned to the gateway, with no secret", () => {
    const cfg = renderSshConfig({ servers: ["prod", "staging", "Bad Name"], nodeCmd: ["/usr/bin/node", "/opt/at 1/node.mjs"], proxySocket: "/run/x/ssh.sock", agentSocket: "/run/u/agent-teams-ssh/s_1/agent.sock", knownHosts: "/run/u/agent-teams-ssh/s_1/known_hosts" });
    expect(cfg).toContain("Host prod\n  User prod\n  HostKeyAlias agent-teams-gateway\n  ProxyCommand /usr/bin/node '/opt/at 1/node.mjs' ssh-proxy --socket /run/x/ssh.sock --server prod\n");
    expect(cfg).toContain("Host staging\n  User staging\n");
    expect(cfg).not.toContain("Bad Name");
    expect(cfg).toContain("Host prod staging\n  IdentityAgent /run/u/agent-teams-ssh/s_1/agent.sock\n");
    for (const line of ["StrictHostKeyChecking yes", "ForwardAgent no", "ForwardX11 no", "ControlMaster no", "GlobalKnownHostsFile /dev/null", "UserKnownHostsFile /run/u/agent-teams-ssh/s_1/known_hosts"]) expect(cfg).toContain(line);
    expect(cfg).not.toMatch(/IdentitiesOnly|IdentityFile|secret/i);
  });

  it.skipIf(!hasSsh)("is understood by ssh -G (paths with % and spaces quoted)", () => {
    const d = join(dir, "a b%c");
    const cfg = renderSshConfig({ servers: ["prod"], nodeCmd: ["node", "cli"], proxySocket: join(d, "ssh.sock"), agentSocket: join(d, "agent.sock"), knownHosts: join(d, "known_hosts") });
    writeFileSync(join(dir, "cfg"), cfg);
    const r = spawnSync("ssh", ["-G", "-F", join(dir, "cfg"), "prod"], { encoding: "utf8" });
    expect(r.status).toBe(0);
    const out = r.stdout.toLowerCase();
    expect(out).toContain("user prod\n");
    expect(out).toContain("hostkeyalias agent-teams-gateway\n");
    expect(out).toContain("stricthostkeychecking true\n");
    expect(out).toContain(`identityagent ${join(d, "agent.sock").toLowerCase()}`);
    expect(out).toContain("forwardagent no\n");
  });

  it("uses a short temp directory when agent.sock would not fit sun_path, and clears all at start", () => {
    expect(sessionSshDir("/run/user/1000/agent-teams-ssh", "s_01JD0000000000000000000000")).toBe("/run/user/1000/agent-teams-ssh/s_01JD0000000000000000000000");
    const deep = join(dir, "x".repeat(90), "agent-teams-ssh");
    const short = sessionSshDir(deep, "s_01JD0000000000000000000000", dir);
    expect(Buffer.byteLength(join(short, "agent.sock"))).toBeLessThanOrEqual(100);
    expect(short.startsWith(join(dir, shortBasePrefix()))).toBe(true);
    expect(Buffer.byteLength(sshSocketPath(join(dir, "y".repeat(100)), dir))).toBeLessThanOrEqual(100);
    mkdirSync(short, { recursive: true });
    mkdirSync(join(deep, "s_x"), { recursive: true });
    mkdirSync(join(dir, "unrelated"));
    clearSshDirs(deep, dir);
    expect(existsSync(short)).toBe(false);
    expect(existsSync(deep)).toBe(false);
    expect(existsSync(join(dir, "unrelated"))).toBe(true);
  });

  it("derives the gateway URL and writes wrappers that only add -F with AT_SSH_CONFIG", () => {
    expect(gatewayUrl("wss://c.example/nodes/connect")).toBe("wss://c.example/nodes/ssh");
    expect(gatewayUrl("ws://127.0.0.1:1/nodes/v2/connect")).toBe("ws://127.0.0.1:1/nodes/ssh");
    const fake = join(dir, "real-ssh");
    writeFileSync(fake, '#!/bin/sh\necho "$@"\n', { mode: 0o755 });
    writeFileSync(join(dir, "ssh"), sshWrapper("ssh", fake), { mode: 0o755 });
    writeFileSync(join(dir, "cfg"), "");
    const run = (env: Record<string, string>) => spawnSync(join(dir, "ssh"), ["prod", "uptime"], { encoding: "utf8", env: { PATH: "/usr/bin:/bin", ...env } }).stdout.trim();
    expect(run({})).toBe("prod uptime");
    expect(run({ AT_SSH_CONFIG: join(dir, "cfg") })).toBe(`-F ${join(dir, "cfg")} prod uptime`);
    expect(run({ AT_SSH_CONFIG: join(dir, "missing") })).toBe("prod uptime");
    expect(sshWrapper("scp", null)).toContain("exit 127");
  });
});

const SECRET_PATTERNS = [/PRIVATE KEY/, /-cert-v01@openssh\.com/, /AAAA[A-Za-z0-9+/]{40,}/, /test-token/];

describe("daemon integration", () => {
  let h: Harness;
  afterEach(async () => h?.close());

  const sshOpen = (id: string, extra: Record<string, unknown> = {}) => claudeOpen(id, 1, { git_credentials: true, ssh_servers: [{ name: "prod" }, { name: "staging" }], ...extra });

  it("injects AT_SSH_CONFIG and the platform bin, keeps secrets out of env and files, cleans up on close", async () => {
    h = await startHarness();
    const hello = h.console.ofType("hello")[0]!;
    expect(hello.body.features).toContain("ssh_gateway");
    if (hasSsh) expect(hello.body.ssh_client.version).toMatch(/^OpenSSH_/);
    h.console.command("session.open", sshOpen("s_ssh1"));
    await h.console.waitType("session.opened");
    const opts = h.log.options[0]! as { env: Record<string, string>; systemPrompt: { append: string } };
    const env = opts.env;
    if (!hasSsh) return expect(env.AT_SSH_CONFIG).toBeUndefined();
    expect(env.AT_SSH_CONFIG).toMatch(/s_ssh1\/ssh_config$/);
    expect(env.PATH.split(":")[0]).toBe(join(h.dir, "platform", "bin"));
    expect(env.SSH_AUTH_SOCK).toBeUndefined();
    expect(env.AT_CRED_SESSION).toBe("s_ssh1");
    for (const v of Object.values(env)) for (const p of SECRET_PATTERNS) expect(v).not.toMatch(p);
    expect(opts.systemPrompt.append).toContain("ssh <name>");
    expect(opts.systemPrompt.append).toContain("prod, staging");
    const sdir = join(env.AT_SSH_CONFIG, "..");
    expect(statSync(sdir).mode & 0o777).toBe(0o700);
    expect(statSync(env.AT_SSH_CONFIG).mode & 0o777).toBe(0o600);
    expect(readFileSync(env.AT_SSH_CONFIG, "utf8")).toContain("Host prod\n");
    for (const name of ["ssh", "scp", "sftp"]) expect(readFileSync(join(h.dir, "platform", "bin", name), "utf8")).toContain('-F "$AT_SSH_CONFIG"');

    // ssh.servers_update: rewritten atomically, older versions ignored, empty list removes ssh_config.
    h.console.command("ssh.servers_update", { session_id: "s_ssh1", open_gen: 1, version: 5, servers: [{ name: "db" }] });
    await waitUntil(() => readFileSync(env.AT_SSH_CONFIG, "utf8").includes("Host db\n"));
    h.console.command("ssh.servers_update", { session_id: "s_ssh1", open_gen: 1, version: 4, servers: [{ name: "old" }] });
    h.console.command("ssh.servers_update", { session_id: "s_ssh1", open_gen: 1, version: 6, servers: [] });
    await waitUntil(() => !existsSync(env.AT_SSH_CONFIG));
    h.console.command("ssh.servers_update", { session_id: "s_ssh1", open_gen: 1, version: 7, servers: [{ name: "prod" }] });
    await waitUntil(() => existsSync(env.AT_SSH_CONFIG));
    expect(readFileSync(env.AT_SSH_CONFIG, "utf8")).not.toContain("Host old");

    h.console.command("session.close", { session_id: "s_ssh1", open_gen: 1 });
    await h.console.waitType("session.closed");
    expect(existsSync(sdir)).toBe(false);
    expect(h.console.invalid).toEqual([]);
  });

  it("gives nothing to the coordinator, sessions without servers, or sandboxed Claude sessions", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_c", 1, { ssh_servers: [{ name: "prod" }], employee: { name: "coordinator", role: "coordinator", tier: "coordinator", project_id: "p1", thread_no: 1 } }));
    h.console.command("session.open", claudeOpen("s_none", 1, { git_credentials: true }));
    h.console.command("session.open", sshOpen("s_box", { claude: { ...claudeOpen("x").claude, sandbox: { enabled: true } } }));
    await h.console.waitFor(() => h.console.ofType("session.opened").length === 3, 10_000, "3 opened").catch((e) => {
      throw new Error(`${e.message} ${JSON.stringify(h.console.ofType("error").map((f) => f.body))}`);
    });
    for (const o of h.log.options as Array<{ env: Record<string, string> }>) expect(o.env.AT_SSH_CONFIG).toBeUndefined();
    expect((h.log.options as Array<{ env: Record<string, string> }>).find((o) => o.env.AT_CRED_SESSION === "s_c")).toBeUndefined();
  });

  it.skipIf(!hasSsh)("drops the session directory when the driver is parked, and clears leftovers at start", async () => {
    h = await startHarness();
    h.console.command("session.open", sshOpen("s_park"));
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_park", open_gen: 1 });
    h.console.command("session.send", { session_id: "s_park", msg_id: "m_p0", text: "one" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    const cfg = (h.log.options[0]! as { env: Record<string, string> }).env.AT_SSH_CONFIG;
    expect(existsSync(cfg)).toBe(true);
    await (h.daemon.sessions as unknown as { parkIdle(all: boolean): Promise<void> }).parkIdle(true);
    await waitUntil(() => !existsSync(join(cfg, "..")));
    // An update while parked is kept in the spec: the next driver gets the new list and a new directory.
    h.console.command("ssh.servers_update", { session_id: "s_park", open_gen: 1, version: 2, servers: [{ name: "db" }] });
    h.console.command("session.send", { session_id: "s_park", msg_id: "m_p1", text: "hi" });
    await h.console.waitType("session.delivered", (b) => b.msg_id === "m_p1").catch((e) => {
      throw new Error(`${e.message}\n${h.console.frames.map((f) => `${f.type} ${JSON.stringify(f.body).slice(0, 200)}`).join("\n")}`);
    });
    const again = h.log.options.at(-1)! as { env: Record<string, string>; systemPrompt: { append: string } };
    expect(readFileSync(again.env.AT_SSH_CONFIG, "utf8")).toContain("Host db\n");
    expect(again.systemPrompt.append).toContain(": db.");
    expect(again.env.AT_CRED_SECRET).not.toBe((h.log.options[0]! as { env: Record<string, string> }).env.AT_CRED_SECRET);
    // Leftovers from a crashed daemon (its keys died with it) are removed when the next one starts.
    mkdirSync(join(h.dir, "ssh", "s_stale"), { recursive: true });
    mkdirSync(join(h.dir, "tmp", `${shortBasePrefix()}abc`, "def"), { recursive: true });
    await h.restart();
    expect(existsSync(join(h.dir, "ssh", "s_stale"))).toBe(false);
    expect(existsSync(join(h.dir, "tmp", `${shortBasePrefix()}abc`))).toBe(false);
  });

  it.skipIf(!hasSsh)("ssh-proxy: wrong secret and ungranted server are refused; a good request is piped to /nodes/ssh", async () => {
    h = await startHarness();
    h.console.command("session.open", sshOpen("s_px"));
    await h.console.waitType("session.opened");
    const env = (h.log.options[0]! as { env: Record<string, string> }).env;
    const socket = /--socket (\S+)/.exec(readFileSync(env.AT_SSH_CONFIG, "utf8"))![1]!;

    const bad = await runProxy(["--socket", socket, "--server", "prod"], { AT_CRED_SESSION: "s_px", AT_CRED_SECRET: "wrong" });
    expect(bad.code).toBe(1);
    expect(bad.err).toContain("unknown session");
    const none = await runProxy(["--socket", socket, "--server", "prod"], {});
    expect(none.code).toBe(1);
    const ungranted = await runProxy(["--socket", socket, "--server", "other"], env);
    expect(ungranted.code).toBe(1);
    expect(ungranted.err).toContain("not available");
    expect(h.console.sshUpgrades).toEqual([]);

    const stop = autoCert(h, join(h.dir, "ca"), ["prod", "staging"]);
    h.console.sshRefuse = 403;
    const refused = await runProxy(["--socket", socket, "--server", "prod"], env);
    expect(refused.code).toBe(1);
    expect(refused.err).toContain("403");
    h.console.sshRefuse = null;

    h.console.onSsh = (ws: WebSocket) => ws.on("message", (d: Buffer) => ws.send(Buffer.concat([Buffer.from("echo:"), d])));
    const ok = await runProxy(["--socket", socket, "--server", "prod"], env, "SSH-2.0-test\r\n");
    expect(ok.out).toBe("echo:SSH-2.0-test\r\n");
    const up = h.console.sshUpgrades.at(-1)!;
    expect(up).toMatchObject({ authorization: "Bearer test-token", "x-at-session": "s_px", "x-at-open-gen": "1", "x-at-server": "prod", "sec-websocket-protocol": "agent-teams-ssh.v1" });

    // A pipe still open when the session closes is cut (the gateway side sees the WebSocket close).
    let closed = false;
    h.console.onSsh = (ws: WebSocket) => ws.on("close", () => (closed = true));
    const hold = runProxy(["--socket", socket, "--server", "prod"], env, "SSH-2.0-hold\r\n");
    await waitUntil(() => h.console.sshUpgrades.length === 3);
    await new Promise((r) => setTimeout(r, 100));
    h.console.command("session.close", { session_id: "s_px", open_gen: 1 });
    await waitUntil(() => closed);
    expect((await hold).code).toBe(0);
    stop();
  });

  it.skipIf(!hasSsh || !hasSshKeygen || !existsSync(SSHD) || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(USER))(
    "smoke: real ssh -F through the wrapper, agent certificate and ssh-proxy to a fake gateway (sshd -i)",
    async () => {
      // ProxyCommand runs the real CLI (through tsx) so ssh-proxy is exercised as ssh starts it.
      h = await startHarness({ nodeCmd: [join(__dirname, "..", "node_modules", ".bin", "tsx"), join(__dirname, "..", "src", "cli.ts")] });
      const work = join(h.dir, "gw");
      mkdirSync(work);
      spawnSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-f", join(work, "hostkey")]);
      spawnSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-f", join(work, "ca")]);
      writeFileSync(join(work, "sshd_config"), [`HostKey ${work}/hostkey`, `TrustedUserCAKeys ${work}/ca.pub`, "AuthorizedKeysFile none", "PasswordAuthentication no", "KbdInteractiveAuthentication no", "UsePAM no", "StrictModes no", "PidFile none", "LogLevel ERROR", "Subsystem sftp internal-sftp"].join("\n") + "\n");
      const hostPub = readFileSync(join(work, "hostkey.pub"), "utf8").trim().split(" ").slice(0, 2).join(" ");
      const stop = autoCert(h, work, [USER], [`agent-teams-gateway ${hostPub}`]);
      h.console.onSsh = (ws: WebSocket) => {
        const sshd = spawn(SSHD, ["-i", "-f", join(work, "sshd_config")], { stdio: ["pipe", "pipe", "inherit"] });
        ws.on("message", (d: Buffer) => sshd.stdin.write(d));
        ws.on("close", () => sshd.kill());
        sshd.stdout.on("data", (d: Buffer) => ws.send(d));
        sshd.on("close", () => ws.close());
      };
      try {
        h.console.command("session.open", claudeOpen("s_smoke", 1, { git_credentials: true, ssh_servers: [{ name: USER }] }));
        await h.console.waitType("session.opened");
        const env = (h.log.options[0]! as { env: Record<string, string> }).env;
        const cfgPath = env.AT_SSH_CONFIG;
        const r = await run("ssh", [USER, "echo hello-$((40+2))"], env);
        expect(r.out + r.err).toBe("hello-42\n");
        expect(r.code).toBe(0);
        expect(h.console.ofType("ssh.cert_request")).toHaveLength(1);
        expect(readFileSync(join(cfgPath, "..", "known_hosts"), "utf8")).toContain("agent-teams-gateway ssh-ed25519");
        // A second connection reuses the in-memory certificate.
        const again = await run("ssh", [USER, "true"], env);
        expect(again.err).toBe("");
        expect(again.code).toBe(0);
        expect(h.console.ofType("ssh.cert_request")).toHaveLength(1);
        // scp (sftp subsystem) goes through the scp wrapper's -F as well.
        writeFileSync(join(h.dir, "payload.txt"), "payload-1\n");
        const scp = await run("scp", [join(h.dir, "payload.txt"), `${USER}:${join(h.dir, "copied.txt")}`], env);
        expect(scp.err).toBe("");
        expect(readFileSync(join(h.dir, "copied.txt"), "utf8")).toBe("payload-1\n");
        // user@ cannot change who the gateway logs in as: the cert's principal is the server name.
        expect((await run("ssh", [`root@${USER}`, "true"], env)).code).not.toBe(0);
      } finally {
        stop();
      }
    },
    30_000,
  );
});

/** Console side: answers every ssh.cert_request with a certificate signed by a test CA. Returns a stop function. */
function autoCert(h: Harness, work: string, principals: string[], knownHosts: string[] = ["agent-teams-gateway ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"]): () => void {
  mkdirSync(work, { recursive: true });
  const done = new Set<string>();
  const t = setInterval(() => {
    for (const f of h.console.ofType("ssh.cert_request")) {
      if (done.has(f.body.req_id)) continue;
      done.add(f.body.req_id);
      const cert = signCert(work, f.body.public_key, principals);
      h.console.unreliable("ssh.cert_response", { req_id: f.body.req_id, ok: true, certificate: cert, valid_before: new Date(Date.now() + 600_000).toISOString(), known_hosts: knownHosts, servers: principals.map((name) => ({ name })) });
    }
  }, 20);
  return () => clearInterval(t);
}

async function waitUntil(pred: () => boolean, ms = 5000): Promise<void> {
  const until = Date.now() + ms;
  while (!pred()) {
    if (Date.now() > until) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 20));
  }
}

async function runProxy(argv: string[], env: Record<string, string>, input?: string): Promise<{ code: number; out: string; err: string }> {
  const stdin = new PassThrough();
  const stdout = new PassThrough();
  const stderr = new PassThrough();
  let out = "";
  let err = "";
  stdout.on("data", (d) => {
    out += d;
    if (input && out.length >= input.length + 5) stdin.end();
  });
  stderr.on("data", (d) => (err += d));
  if (input) stdin.write(input);
  else stdin.end();
  const code = await sshProxyMain(argv, env, { stdin, stdout, stderr });
  return { code, out, err };
}

function run(cmd: string, args: string[], env: Record<string, string>): Promise<{ code: number; out: string; err: string }> {
  return new Promise((resolve) => {
    const c = spawn(cmd, args, { env, stdio: ["ignore", "pipe", "pipe"] });
    let out = "";
    let err = "";
    c.stdout.on("data", (d) => (out += d));
    c.stderr.on("data", (d) => (err += d));
    c.on("close", (code) => resolve({ code: code ?? 1, out, err }));
  });
}
