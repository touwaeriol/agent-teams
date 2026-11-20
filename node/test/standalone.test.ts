import { execFileSync, spawn, spawnSync } from "node:child_process";
import { createPublicKey, generateKeyPairSync, verify } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type Server } from "node:https";
import { connect } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { startHarness, type Harness } from "./helpers/daemon.js";
import { CredBroker } from "../src/cred/broker.js";
import { HeldKey, keyAgentMessage, keyFor, parseSshArgs, sshOptions, upstreamOf } from "../src/cred/gitssh.js";
import { gitCredentialEnv } from "../src/cred/helper.js";
import { CredServer } from "../src/cred/server.js";
import { AgentServer, FrameDecoder, frame, sshString, WireReader, SSH_AGENTC_REQUEST_IDENTITIES, SSH_AGENTC_SIGN_REQUEST } from "../src/ssh/agent.js";
import { silentLogger } from "../src/util/log.js";

const hasGit = spawnSync("git", ["--version"]).error === undefined;
const hasOpenssl = spawnSync("openssl", ["version"]).error === undefined;
const TOKEN = "glpat-upstream";

const git = (cwd: string, ...args: string[]) =>
  execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8", env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" } }).trim();

/** An HTTPS Git host (git http-backend over root) that wants Basic auth with TOKEN. */
function upstream(root: string, tls: { key: string; cert: string }): Promise<{ server: Server; base: string }> {
  const server = createServer(tls, (req, res) => {
    const pass = Buffer.from((req.headers.authorization ?? "").replace(/^Basic /, ""), "base64").toString().split(":").slice(1).join(":");
    if (pass !== TOKEN) {
      res.writeHead(401, { "WWW-Authenticate": 'Basic realm="t"' }).end();
      return;
    }
    const url = new URL(req.url ?? "/", "https://x");
    const cgi = spawn("git", ["http-backend"], {
      env: { ...process.env, GIT_PROJECT_ROOT: root, GIT_HTTP_EXPORT_ALL: "1", REMOTE_USER: "t", PATH_INFO: url.pathname,
        QUERY_STRING: url.search.slice(1), REQUEST_METHOD: req.method ?? "GET", CONTENT_TYPE: req.headers["content-type"] ?? "",
        ...(req.headers["content-encoding"] ? { HTTP_CONTENT_ENCODING: String(req.headers["content-encoding"]) } : {}) },
    });
    req.pipe(cgi.stdin);
    let head: Buffer | null = Buffer.alloc(0);
    cgi.stdout.on("data", (d: Buffer) => {
      if (head === null) return void res.write(d);
      head = Buffer.concat([head, d]);
      const end = head.indexOf("\r\n\r\n");
      if (end < 0) return;
      let status = 200;
      for (const l of head.subarray(0, end).toString().split("\r\n")) {
        const [k, ...v] = l.split(":");
        if (k!.toLowerCase() === "status") status = Number(v.join(":").trim().split(" ")[0]);
        else res.setHeader(k!, v.join(":").trim());
      }
      res.writeHead(status);
      res.write(head.subarray(end + 4));
      head = null;
    });
    cgi.on("close", () => res.end());
  });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve({ server, base: `https://127.0.0.1:${(server.address() as { port: number }).port}` })));
}

describe.skipIf(!hasGit || !hasOpenssl)("standalone repositories over HTTPS", () => {
  let h: Harness;
  let srv: Server | undefined;
  const savedCA = process.env.GIT_SSL_CAINFO;
  afterEach(async () => {
    srv?.close();
    await h?.close();
    if (savedCA === undefined) delete process.env.GIT_SSL_CAINFO;
    else process.env.GIT_SSL_CAINFO = savedCA;
  });

  it("clones and pushes the upstream itself with the credential the console hands the job", async () => {
    h = await startHarness();
    const certDir = join(h.dir, "tls");
    mkdirSync(certDir);
    execFileSync("openssl", ["req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "1", "-subj", "/CN=127.0.0.1",
      "-addext", "subjectAltName=IP:127.0.0.1", "-keyout", join(certDir, "key.pem"), "-out", join(certDir, "cert.pem")], { stdio: "ignore" });
    process.env.GIT_SSL_CAINFO = join(certDir, "cert.pem");
    const root = join(h.dir, "up");
    const bare = join(root, "acme", "web.git");
    mkdirSync(join(root, "acme"), { recursive: true });
    execFileSync("git", ["init", "--bare", "-b", "main", bare]);
    git(bare, "config", "http.receivepack", "true");
    const seed = join(h.dir, "seed");
    execFileSync("git", ["clone", bare, seed], { stdio: "ignore" });
    writeFileSync(join(seed, "README.md"), "hi\n");
    git(seed, "add", ".");
    git(seed, "commit", "-m", "init");
    git(seed, "push", "origin", "HEAD:main");
    const r = await upstream(root, { key: readFileSync(join(certDir, "key.pem"), "utf8"), cert: readFileSync(join(certDir, "cert.pem"), "utf8") });
    srv = r.server;
    const remote = `${r.base}/acme/web.git`;
    const answered = new Set<string>();
    const timer = setInterval(() => {
      for (const f of h.console.ofType("cred.request")) {
        if (answered.has(f.body.req_id)) continue;
        answered.add(f.body.req_id);
        h.console.unreliable("cred.response", { req_id: f.body.req_id, ok: true, username: "oauth2", password: TOKEN, expires_at: new Date(Date.now() + 1800_000).toISOString() });
      }
    }, 10);
    try {
      h.console.command("git.prepare", { job_id: "g_1", project_id: "p1", repo: "git/gr_abc", remote_url: remote, mode: "clone",
        cwd: "projects/p1/threads/1/dev", branch: "thread/1/dev", base: "main", fetch: true });
      expect((await h.console.waitType("git.result", (b) => b.job_id === "g_1")).body).toMatchObject({ ok: true, created: true });
      expect(h.console.ofType("cred.request")[0]!.body).toMatchObject({ job_id: "g_1", host: r.base.replace("https://", ""), path: "acme/web.git", purpose: "read" });
      const wt = join(h.opts.root, "projects/p1/threads/1/dev");
      expect(git(wt, "remote", "get-url", "origin")).toBe(remote);
      writeFileSync(join(wt, "b.txt"), "b\n");
      git(wt, "add", ".");
      git(wt, "commit", "-m", "work");
      h.console.command("git.push", { job_id: "g_2", project_id: "p1", repo: "git/gr_abc", remote_url: remote, cwd: "projects/p1/threads/1/dev",
        refspec: "HEAD:refs/heads/thread/1/dev", employee: "dev", thread_no: 1 });
      expect((await h.console.waitType("git.result", (b) => b.job_id === "g_2")).body).toMatchObject({ ok: true });
      expect(git(bare, "rev-parse", "refs/heads/thread/1/dev")).toBe(git(wt, "rev-parse", "HEAD"));
      expect(readFileSync(join(wt, ".git", "config"), "utf8")).not.toContain(TOKEN);
    } finally {
      clearInterval(timer);
    }
  });
});

describe("session environment for standalone repositories", () => {
  it("points the credential helper at their HTTPS hosts and git's ssh at the wrapper", () => {
    const env = gitCredentialEnv(["node", "cli.mjs"], "/s", "s_1", "x", [{ transport: "https", host: "gitlab.com" }, { transport: "ssh", host: "git.example.com:2222" }]);
    expect(env.GIT_CONFIG_COUNT).toBe("6");
    expect(env.GIT_CONFIG_KEY_3).toBe("credential.https://gitlab.com.helper");
    expect(env.GIT_CONFIG_KEY_5).toBe("credential.https://gitlab.com.useHttpPath");
    expect(env.GIT_SSH_COMMAND).toBe("node cli.mjs git-ssh");
    expect(gitCredentialEnv(["node"], "/s", "s", "x").GIT_SSH_COMMAND).toBeUndefined();
  });

  it("parses where git connects and a remote URL's host and path", () => {
    expect(parseSshArgs(["-o", "SendEnv=GIT_PROTOCOL", "git@gitlab.com", "git-upload-pack 'acme/web.git'"])).toEqual({ host: "gitlab.com", path: "acme/web.git" });
    expect(parseSshArgs(["-p", "2222", "git@Git.Example.com", "git-receive-pack '/srv/web.git'"])).toEqual({ host: "git.example.com:2222", path: "srv/web.git" });
    expect(parseSshArgs(["-p22", "host", "git-upload-pack '~/x/y.git'"])).toEqual({ host: "host", path: "x/y.git" });
    expect(parseSshArgs(["server", "uptime"])).toBeNull();
    expect(upstreamOf("git@gitlab.com:acme/web.git")).toEqual({ transport: "ssh", host: "gitlab.com", path: "acme/web.git" });
    expect(upstreamOf("ssh://git@h:2222/a/b.git")).toEqual({ transport: "ssh", host: "h:2222", path: "a/b.git" });
    expect(upstreamOf("https://H:443/a/b.git")).toEqual({ transport: "https", host: "h", path: "a/b.git" });
  });

  it("pins the host key and offers only the held key", () => {
    const o = sshOptions("/d").join(" ");
    expect(o).toContain("IdentityAgent=/d/agent.sock");
    expect(o).toContain("IdentitiesOnly=yes");
    expect(o).toContain("UserKnownHostsFile=/d/known_hosts");
    expect(o).toContain("StrictHostKeyChecking=yes");
    expect(o).toContain("BatchMode=yes");
  });
});

/** authorized_keys line for a Node public key (ed25519, rsa, ecdsa P-256). */
function authorizedKey(pub: ReturnType<typeof createPublicKey>): string {
  const jwk = pub.export({ format: "jwk" });
  const b = (s: string) => Buffer.from(s, "base64url");
  const mp = (x: Buffer) => (x[0]! & 0x80 ? Buffer.concat([Buffer.from([0]), x]) : x);
  if (jwk.kty === "OKP") return `ssh-ed25519 ${Buffer.concat([sshString("ssh-ed25519"), sshString(b(jwk.x!))]).toString("base64")}`;
  if (jwk.kty === "RSA") return `ssh-rsa ${Buffer.concat([sshString("ssh-rsa"), sshString(mp(b(jwk.e!))), sshString(mp(b(jwk.n!)))]).toString("base64")}`;
  const point = Buffer.concat([Buffer.from([4]), b(jwk.x!), b(jwk.y!)]);
  return `ecdsa-sha2-nistp256 ${Buffer.concat([sshString("ecdsa-sha2-nistp256"), sshString("nistp256"), sshString(point)]).toString("base64")}`;
}

function heldKey(type: "ed25519" | "rsa" | "ec") {
  const pair = type === "rsa" ? generateKeyPairSync("rsa", { modulusLength: 2048 }) : type === "ec" ? generateKeyPairSync("ec", { namedCurve: "prime256v1" }) : generateKeyPairSync("ed25519");
  const raw = { private_key: pair.privateKey.export({ type: "pkcs8", format: "pem" }).toString(), public_key: authorizedKey(pair.publicKey), known_hosts: "h ssh-ed25519 AAAA" };
  return { raw, key: new HeldKey(raw), pub: pair.publicKey };
}

describe("git-ssh held key", () => {
  it("signs the way ssh expects for Ed25519, RSA (SHA-2) and ECDSA", () => {
    const data = Buffer.from("session data");
    for (const [type, flags, alg, hash] of [["ed25519", 0, "ssh-ed25519", null], ["rsa", 2, "rsa-sha2-256", "sha256"], ["rsa", 4, "rsa-sha2-512", "sha512"], ["ec", 0, "ecdsa-sha2-nistp256", "sha256"]] as const) {
      const { key, pub } = heldKey(type);
      const r = new WireReader(key.sign(data, flags));
      expect(r.string().toString()).toBe(alg);
      let sig = r.string();
      if (type === "ec") {
        const inner = new WireReader(sig);
        const pad = (x: Buffer) => Buffer.concat([Buffer.alloc(32), x]).subarray(-32);
        sig = Buffer.concat([pad(inner.string()), pad(inner.string())]);
        expect(verify(hash, data, { key: pub, dsaEncoding: "ieee-p1363" }, sig)).toBe(true);
      } else {
        expect(verify(hash, data, pub, sig)).toBe(true);
      }
    }
  });

  it("serves its identity over an agent socket and refuses other keys", async () => {
    const { key } = heldKey("ed25519");
    const other = heldKey("ed25519").key;
    const dir = mkdtempSync(join(tmpdir(), "at-gitssh-"));
    const agent = new AgentServer(join(dir, "agent.sock"), (m) => keyAgentMessage(m, key));
    await agent.start();
    try {
      const ask = (msg: Buffer) => new Promise<Buffer>((resolve, reject) => {
        const s = connect(join(dir, "agent.sock"));
        const dec = new FrameDecoder();
        s.on("data", (d: Buffer) => {
          const [m] = dec.push(d);
          if (m) {
            s.end();
            resolve(m);
          }
        });
        s.on("error", reject);
        s.write(frame(msg));
      });
      const ids = new WireReader(await ask(Buffer.from([SSH_AGENTC_REQUEST_IDENTITIES])));
      expect(ids.byte()).toBe(12);
      expect(ids.uint32()).toBe(1);
      expect(ids.string().equals(key.publicBlob)).toBe(true);
      const signReq = (blob: Buffer) => Buffer.concat([Buffer.from([SSH_AGENTC_SIGN_REQUEST]), sshString(blob), sshString("d"), Buffer.alloc(4)]);
      expect((await ask(signReq(key.publicBlob)))[0]).toBe(14);
      expect((await ask(signReq(other.publicBlob)))[0]).toBe(5);
    } finally {
      agent.close();
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it("gets the key from the daemon with the session secret only", async () => {
    const { raw } = heldKey("ed25519");
    const dir = mkdtempSync(join(tmpdir(), "at-gitssh-"));
    let broker: CredBroker;
    const sent: Array<Record<string, unknown>> = [];
    broker = new CredBroker((_t, body) => {
      sent.push(body as Record<string, unknown>);
      const b = body as { req_id: string; host: string };
      setTimeout(() => broker.resolve(b.host === "gitlab.com" ? { req_id: b.req_id, ok: true, username: "git", ssh_key: raw } : { req_id: b.req_id, ok: false, error: "repo_not_in_project" }), 2);
    });
    const server = new CredServer(join(dir, "run", "cred.sock"), broker, silentLogger);
    await server.start();
    try {
      const secret = server.register("s_1", 1);
      const env = { AT_CRED_SOCK: server.socketPath, AT_CRED_SESSION: "s_1", AT_CRED_SECRET: secret };
      expect(await keyFor({ host: "gitlab.com", path: "acme/web.git" }, env)).toEqual(raw);
      expect(sent[0]).toMatchObject({ session_id: "s_1", host: "gitlab.com", path: "acme/web.git", purpose: "read" });
      expect(await keyFor({ host: "gitlab.com", path: "acme/web.git" }, { ...env, AT_CRED_SECRET: "wrong" })).toBeNull();
      expect(await keyFor({ host: "other.example", path: "a/b.git" }, env)).toBeNull();
      expect(await keyFor({ host: "gitlab.com", path: "acme/web.git" }, { AT_GIT_SSH_KEY: "k", AT_GIT_SSH_PUB: "p", AT_GIT_SSH_KNOWN_HOSTS: "h" })).toEqual({ private_key: "k", public_key: "p", known_hosts: "h" });
    } finally {
      await server.stop();
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
