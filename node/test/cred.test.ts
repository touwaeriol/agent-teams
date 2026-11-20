import { generateKeyPairSync, sign, createHash } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CredBroker } from "../src/cred/broker.js";
import { CredServer, credSocketPath } from "../src/cred/server.js";
import { gitCredentialEnv, gitCredentialMain, githubPath, parseCredentialInput } from "../src/cred/helper.js";
import { verifyRelease } from "../src/upgrade.js";
import { silentLogger } from "../src/util/log.js";

let dir: string;
beforeEach(() => (dir = mkdtempSync(join(tmpdir(), "at-cred-"))));
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe("credSocketPath", () => {
  it("keeps <runtimeDir>/cred.sock when it fits and falls back to a short temp path otherwise", async () => {
    expect(credSocketPath("/run/user/1000/agent-teams")).toBe("/run/user/1000/agent-teams/cred.sock");
    const deep = join(dir, "x".repeat(120), "agent-teams");
    const short = credSocketPath(deep, dir);
    expect(Buffer.byteLength(short)).toBeLessThanOrEqual(100);
    expect(short).toBe(credSocketPath(deep, dir));
    expect(credSocketPath(deep + "2", dir)).not.toBe(short);
    const server = new CredServer(short, new CredBroker(() => {}), silentLogger);
    await server.start();
    await server.stop();
  });
});

describe("credentials", () => {
  it("serves read tokens to the git credential helper of a registered session only", async () => {
    const sent: Array<Record<string, any>> = [];
    let broker: CredBroker;
    broker = new CredBroker((type, body) => {
      sent.push({ type, ...body });
      const b = body as { req_id: string; path: string };
      setTimeout(() => broker.resolve({ req_id: b.req_id, ok: true, username: "x-access-token", password: "ghs_test123", expires_at: new Date(Date.now() + 3600_000).toISOString() }), 5);
    });
    const server = new CredServer(join(dir, "run", "cred.sock"), broker, silentLogger);
    await server.start();
    try {
      const secret = server.register("s_1", 3);
      const out = await gitCredentialMain({ socket: server.socketPath, session: "s_1", secret }, "get", "protocol=https\nhost=github.com\npath=acme/app.git\n\n");
      expect(out.out).toBe("username=x-access-token\npassword=ghs_test123\n");
      expect(sent).toEqual([expect.objectContaining({ type: "cred.request", session_id: "s_1", open_gen: 3, host: "github.com", path: "acme/app.git", purpose: "read" })]);
      await gitCredentialMain({ socket: server.socketPath, session: "s_1", secret }, "get", "protocol=https\nhost=github.com\npath=acme/app.git\n");
      expect(sent.length).toBe(1);
      expect((await gitCredentialMain({ socket: server.socketPath, session: "s_1", secret: "wrong" }, "get", "protocol=https\nhost=github.com\n")).out).toBe("quit=1\n");
      expect((await gitCredentialMain({ socket: server.socketPath, session: "s_1", secret }, "store", "")).out).toBe("");
    } finally {
      await server.stop();
    }
  });

  it("drops the cached token on git erase (this session and repository only) and when the session ends", async () => {
    const sent: Array<Record<string, any>> = [];
    let broker: CredBroker;
    broker = new CredBroker((type, body) => {
      sent.push({ type, ...body });
      const b = body as { req_id: string };
      setTimeout(() => broker.resolve({ req_id: b.req_id, ok: true, username: "x-access-token", password: `ghs_${sent.length}`, expires_at: new Date(Date.now() + 3600_000).toISOString() }), 5);
    });
    const server = new CredServer(join(dir, "run", "cred.sock"), broker, silentLogger);
    await server.start();
    try {
      const a = server.register("s_1", 1);
      const b = server.register("s_2", 1);
      const get = (session: string, secret: string, path: string) => gitCredentialMain({ socket: server.socketPath, session, secret }, "get", `protocol=https\nhost=github.com\npath=${path}\n`);
      const erase = (session: string, secret: string, path: string) => gitCredentialMain({ socket: server.socketPath, session, secret }, "erase", `protocol=https\nhost=github.com\npath=${path}\nusername=x-access-token\npassword=ghs_1\n`);
      expect((await get("s_1", a, "acme/app.git")).out).toContain("password=ghs_1");
      await get("s_1", a, "acme/lib.git");
      await get("s_2", b, "acme/app.git");
      expect(sent.length).toBe(3);
      // A wrong secret cannot drop anything.
      expect((await erase("s_1", "wrong", "acme/app.git")).out).toBe("");
      await get("s_1", a, "acme/app.git");
      expect(sent.length).toBe(3);
      expect(sent.every((m) => m.force_refresh === undefined)).toBe(true);
      // erase drops s_1's token for acme/app only: the next get asks the console again, telling
      // it to skip its own token cache (force_refresh), once.
      expect((await erase("s_1", a, "acme/app.git")).out).toBe("");
      expect((await get("s_1", a, "acme/app.git")).out).toContain("password=ghs_4");
      expect(sent[3]).toMatchObject({ session_id: "s_1", path: "acme/app.git", force_refresh: true });
      await get("s_1", a, "acme/lib.git");
      await get("s_2", b, "acme/app.git");
      expect(sent.length).toBe(4);
      // erase of a token no longer cached still makes the next request force a refresh.
      await erase("s_2", b, "acme/lib.git");
      await get("s_2", b, "acme/lib.git");
      expect(sent[4]).toMatchObject({ session_id: "s_2", path: "acme/lib.git", force_refresh: true });
      await erase("s_2", b, "acme/lib.git");
      sent.splice(4, 1);
      server.unregister("s_2");
      // The session ends: its tokens leave the cache; the other session keeps its own.
      server.unregister("s_1");
      const again = server.register("s_1", 2);
      const b2 = server.register("s_2", 1);
      await get("s_1", again, "acme/lib.git");
      expect(sent.length).toBe(5);
      // The session's pending force_refresh ended with it.
      await get("s_2", b2, "acme/lib.git");
      expect(sent[5]!.force_refresh).toBeUndefined();
    } finally {
      await server.stop();
    }
  });

  it("caches a token only until 5 minutes before the expiry the console reported", async () => {
    let n = 0;
    let left = 4 * 60_000;
    let broker: CredBroker;
    broker = new CredBroker((_t, body) => {
      n++;
      const b = body as { req_id: string };
      setTimeout(() => broker.resolve({ req_id: b.req_id, ok: true, password: `ghs_${n}`, expires_at: new Date(Date.now() + left).toISOString() }), 1);
    });
    const scope = { session_id: "s_1", open_gen: 1 };
    await broker.get(scope, "github.com", "a/b.git", "read");
    await broker.get(scope, "github.com", "a/b.git", "read");
    expect(n).toBe(2); // 4 minutes left (a cached console token): never reused
    left = 37 * 60_000;
    const c = await broker.get(scope, "github.com", "a/b.git", "read");
    expect((await broker.get(scope, "github.com", "a/b.git", "read")).password).toBe(c.password);
    expect(n).toBe(3);
  });

  it("retries an unanswered cred.request with the same req_id and then gives up", async () => {
    const sent: Array<{ req_id: string }> = [];
    const broker = new CredBroker((_t, body) => sent.push(body as { req_id: string }), 20);
    await expect(broker.get({ job_id: "g_1" }, "github.com", "a/b.git", "push")).rejects.toThrow(/did not answer/);
    expect(sent.length).toBe(3);
    expect(new Set(sent.map((s) => s.req_id)).size).toBe(1);
  });

  it("builds git config env for the helper without writing tokens anywhere", () => {
    const env = gitCredentialEnv(["/usr/bin/node", "/opt/at/node.mjs"], "/run/at/cred.sock", "s_1", "sec");
    expect(env.GIT_CONFIG_VALUE_0).toBe("");
    expect(env.GIT_CONFIG_VALUE_1).toBe("!/usr/bin/node /opt/at/node.mjs git-credential --socket /run/at/cred.sock --session s_1 --secret sec");
    expect(env.GIT_TERMINAL_PROMPT).toBe("0");
    expect(parseCredentialInput("protocol=https\nhost=github.com\n")).toEqual({ protocol: "https", host: "github.com" });
    expect(githubPath("https://github.com/acme/app.git")).toBe("acme/app.git");
    expect(githubPath("git@github.com:acme/app")).toBe("acme/app.git");
    expect(githubPath("https://gitlab.com/a/b")).toBeNull();
  });
});

describe("upgrade verification", () => {
  it("accepts only tarballs with the right sha256 and Ed25519 signature", () => {
    const { publicKey, privateKey } = generateKeyPairSync("ed25519");
    const pem = publicKey.export({ type: "spki", format: "pem" }).toString();
    const tgz = Buffer.from("release bytes");
    const msg = { version: "2.0.1", sha256: createHash("sha256").update(tgz).digest("hex"), signature: sign(null, tgz, privateKey).toString("base64") };
    expect(() => verifyRelease(tgz, msg, pem)).not.toThrow();
    expect(() => verifyRelease(Buffer.from("tampered"), msg, pem)).toThrow(/sha256/);
    expect(() => verifyRelease(tgz, { ...msg, signature: sign(null, Buffer.from("x"), privateKey).toString("base64") }, pem)).toThrow(/signature/);
    const der = publicKey.export({ type: "spki", format: "der" }).toString("base64");
    expect(() => verifyRelease(tgz, msg, der)).not.toThrow();
  });
});
