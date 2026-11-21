import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { EventEmitter } from "node:events";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ChildProcess } from "node:child_process";
import type { NodeUpgradeResult } from "../src/protocol.js";
import { beforeStart, launch, releaseIdOf, rollback, seedRelease } from "../src/launcher.js";
import { buildOf, MAX_UNCONFIRMED_BOOTS, ReleaseLayout } from "../src/release.js";
import { Updater } from "../src/upgrade.js";
import { silentLogger } from "../src/util/log.js";

let dir: string;
beforeEach(() => (dir = mkdtempSync(join(tmpdir(), "at-upgrade-"))));
afterEach(() => rmSync(dir, { recursive: true, force: true }));

/** A release directory with package.json, node.mjs (content decides the build) and a lock file. */
function makeRelease(at: string, code: string, lock = "{\"lock\":1}"): string {
  mkdirSync(at, { recursive: true });
  writeFileSync(join(at, "package.json"), JSON.stringify({ name: "agent-teams-node", version: "2.0.0" }));
  writeFileSync(join(at, "node.mjs"), code);
  writeFileSync(join(at, "npm-shrinkwrap.json"), lock);
  return releaseIdOf(at);
}

/** tgz of a release, as npm pack lays it out (package/…). */
function makeTgz(code: string, lock?: string): { id: string; tgz: Buffer; sha256: string } {
  const src = join(dir, `src-${Math.random().toString(16).slice(2)}`);
  const id = makeRelease(join(src, "package"), code, lock);
  const file = join(src, "r.tgz");
  execFileSync("tar", ["-czf", file, "-C", src, "package"]);
  const tgz = readFileSync(file);
  return { id, tgz, sha256: createHash("sha256").update(tgz).digest("hex") };
}

function fakeSessions(turns = 0) {
  return { turns, drained: 0, undrained: 0, drain() { this.drained++; }, undrain() { this.undrained++; }, busyTurns() { return this.turns; } };
}

function setup(opts: { turns?: number; tgz?: Buffer } = {}) {
  const data = join(dir, "data");
  const layout = new ReleaseLayout(data);
  const self = makeRelease(join(data, "releases", "old"), "old();");
  // the running release lives at releases/<its id>
  execFileSync("mv", [join(data, "releases", "old"), join(data, "releases", self)]);
  layout.point("current", join("releases", self));
  mkdirSync(join(layout.dir(self), "node_modules", "dep"), { recursive: true });
  writeFileSync(join(layout.dir(self), "node_modules", "dep", "index.js"), "x");
  const sent: NodeUpgradeResult[] = [];
  const exits: number[] = [];
  const urls: string[] = [];
  const npmRuns: string[] = [];
  const sessions = fakeSessions(opts.turns ?? 0);
  const u = new Updater({
    data, self, consoleHttpBase: () => "http://console", releaseKeyPath: join(dir, "release.pub"), sessions, log: silentLogger,
    send: (b) => sent.push(b), exit: (c) => exits.push(c), pollMs: 5, confirmAfterMs: 10,
    fetchFn: (async (url: string) => (urls.push(url), new Response(opts.tgz ?? Buffer.from("")))) as unknown as typeof fetch,
    npm: async (cwd) => (npmRuns.push(cwd), mkdirSync(join(cwd, "node_modules"), { recursive: true }), { code: 0, stderr: "" }),
  });
  return { data, layout, self, sent, exits, urls, npmRuns, sessions, u };
}

const until = async (cond: () => boolean, ms = 3000) => {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 5));
  }
};

describe("Updater", () => {
  it("stages next to the running release, drains, switches current and exits 75", async () => {
    const rel = makeTgz("new();");
    const t = setup({ tgz: rel.tgz });
    t.u.handle({ version: rel.id, sha256: rel.sha256 });
    await until(() => t.exits.length > 0);
    expect(t.urls).toEqual([`http://console/node/dist/agent-teams-node-${encodeURIComponent(rel.id)}.tgz`]);
    expect(t.sent.map((s) => s.phase)).toEqual(["staging", "draining", "restarting"]);
    expect(t.exits).toEqual([75]);
    expect(t.sessions.drained).toBe(1);
    expect(t.layout.target("current")).toBe(rel.id);
    expect(t.layout.target("previous")).toBe(t.self);
    expect(existsSync(join(t.layout.dir(t.self), "node.mjs"))).toBe(true);
    // same lock file: node_modules hard-linked from the running release, no npm ci
    expect(t.npmRuns).toEqual([]);
    expect(readFileSync(join(t.layout.dir(rel.id), "node_modules", "dep", "index.js"), "utf8")).toBe("x");
    expect(t.layout.readState()).toMatchObject({ state: "switched", from: t.self, to: rel.id, boots: 0 });
  });

  it("runs npm ci when the lock file changed", async () => {
    const rel = makeTgz("new();", "{\"lock\":2}");
    const t = setup({ tgz: rel.tgz });
    t.u.handle({ version: rel.id, sha256: rel.sha256 });
    await until(() => t.exits.length > 0);
    expect(t.npmRuns).toEqual([`${t.layout.dir(rel.id)}.staging`]);
  });

  it("waits for running turns, reports drain_timeout, and switches at once on mode now", async () => {
    const rel = makeTgz("new();");
    const t = setup({ tgz: rel.tgz, turns: 2 });
    t.u.handle({ version: rel.id, sha256: rel.sha256, deadline_s: 1 });
    await until(() => t.sent.some((s) => s.phase === "draining"));
    expect(t.sent.find((s) => s.phase === "draining")?.active_turns).toBe(2);
    await until(() => t.sent.some((s) => s.phase === "drain_timeout"), 3000);
    expect(t.exits).toEqual([]);
    t.u.handle({ version: rel.id, sha256: rel.sha256, mode: "now" });
    await until(() => t.exits.length > 0);
    expect(t.layout.target("current")).toBe(rel.id);
  });

  it("cancel during the drain undrains and keeps the running release", async () => {
    const rel = makeTgz("new();");
    const t = setup({ tgz: rel.tgz, turns: 1 });
    t.u.handle({ version: rel.id, sha256: rel.sha256 });
    await until(() => t.sent.some((s) => s.phase === "draining"));
    t.u.handle({ version: rel.id, sha256: rel.sha256, mode: "cancel" });
    await until(() => t.sent.some((s) => s.phase === "cancelled"));
    expect(t.sessions.undrained).toBe(1);
    expect(t.layout.target("current")).toBe(t.self);
    expect(t.u.busy).toBe(false);
  });

  it("fails on a sha256 mismatch without touching anything", async () => {
    const rel = makeTgz("new();");
    const t = setup({ tgz: rel.tgz });
    t.u.handle({ version: rel.id, sha256: "0".repeat(64) });
    await until(() => t.sent.some((s) => s.phase === "failed"));
    expect(t.sent.at(-1)?.error).toMatch(/sha256/);
    expect(t.layout.target("current")).toBe(t.self);
    expect(t.sessions.drained).toBe(0);
  });

  it("answers done for the running release and refuses a second target while busy", async () => {
    const rel = makeTgz("new();");
    const t = setup({ tgz: rel.tgz, turns: 1 });
    t.u.handle({ version: t.self, sha256: "a".repeat(64) });
    expect(t.sent).toEqual([{ ok: true, phase: "done", version: t.self }]);
    t.u.handle({ version: rel.id, sha256: rel.sha256 });
    t.u.handle({ version: "2.0.0+deadbeef", sha256: rel.sha256 });
    expect(t.sent.at(-1)).toMatchObject({ ok: false, phase: "failed", version: "2.0.0+deadbeef" });
    t.u.handle({ version: rel.id, sha256: rel.sha256, mode: "cancel" });
    await until(() => !t.u.busy);
  });

  it("refuses a release that needs a newer Node.js", async () => {
    const t = setup();
    t.u.handle({ version: "2.0.0+deadbeef", sha256: "a".repeat(64), min_node: 999 });
    await until(() => t.sent.some((s) => s.phase === "failed"));
    expect(t.sent.at(-1)?.error).toMatch(/Node\.js 999/);
  });

  it("the new release confirms itself after staying connected, and prunes old releases", async () => {
    const t = setup();
    t.layout.writeState({ state: "switched", from: "2.0.0+00000000", to: t.self, boots: 1, switched_at: Date.now() });
    mkdirSync(t.layout.dir("2.0.0+11111111"), { recursive: true });
    t.u.onConnected();
    await until(() => t.sent.length > 0);
    expect(t.sent).toEqual([{ ok: true, phase: "done", version: t.self }]);
    expect(t.layout.readState()?.state).toBe("confirmed");
    expect(existsSync(t.layout.dir("2.0.0+11111111"))).toBe(false);
    expect(existsSync(t.layout.dir(t.self))).toBe(true);
  });

  it("a disconnect before the confirm delay postpones the confirmation", async () => {
    const t = setup();
    t.layout.writeState({ state: "switched", from: "2.0.0+00000000", to: t.self, boots: 1, switched_at: Date.now() });
    t.u.onConnected();
    t.u.onDisconnected();
    await new Promise((r) => setTimeout(r, 30));
    expect(t.layout.readState()?.state).toBe("switched");
  });

  it("the old release reports a rollback once", () => {
    const t = setup();
    t.layout.writeState({ state: "rolled_back", from: t.self, to: "2.0.0+deadbeef", boots: 4, switched_at: 1, error: "boom", reported: false });
    t.u.onConnected();
    t.u.onConnected();
    expect(t.sent).toEqual([{ ok: false, phase: "rolled_back", version: "2.0.0+deadbeef", error: "boom" }]);
  });
});

describe("launcher", () => {
  function layoutWith(): { layout: ReleaseLayout; a: string; b: string } {
    const layout = new ReleaseLayout(join(dir, "data"));
    makeRelease(layout.dir("a"), "a();");
    makeRelease(layout.dir("b"), "b();");
    layout.point("current", "releases/a");
    layout.switchTo("b");
    return { layout, a: "a", b: "b" };
  }

  it("counts starts of an unconfirmed release and rolls back after the limit", () => {
    const { layout } = layoutWith();
    layout.writeState({ state: "switched", from: "a", to: "b", boots: 0, switched_at: 1000 });
    for (let i = 0; i < MAX_UNCONFIRMED_BOOTS; i++) beforeStart(layout, 2000, () => {});
    expect(layout.readState()).toMatchObject({ state: "switched", boots: MAX_UNCONFIRMED_BOOTS });
    beforeStart(layout, 2000, () => {});
    expect(layout.readState()).toMatchObject({ state: "rolled_back", reported: false });
    expect(layout.target("current")).toBe("a");
    expect(layout.target("previous")).toBe("b");
  });

  it("rolls back a release that did not confirm within the deadline", () => {
    const { layout } = layoutWith();
    layout.writeState({ state: "switched", from: "a", to: "b", boots: 1, switched_at: 0 });
    beforeStart(layout, 10 * 60_000, () => {});
    expect(layout.readState()?.error).toMatch(/5 分钟/);
    expect(layout.target("current")).toBe("a");
  });

  it("leaves confirmed releases alone, and records a failed rollback when there is no previous", () => {
    const layout = new ReleaseLayout(join(dir, "data"));
    makeRelease(layout.dir("a"), "a();");
    layout.point("current", "releases/a");
    layout.writeState({ state: "confirmed", from: "z", to: "a", boots: 1, switched_at: 0 });
    beforeStart(layout, 10 * 60_000, () => {});
    expect(layout.readState()?.state).toBe("confirmed");
    layout.writeState({ state: "switched", from: "z", to: "a", boots: 1, switched_at: 0 });
    rollback(layout, "boom", 1, () => {});
    expect(layout.readState()).toMatchObject({ state: "rolled_back", error: "boom; no previous release to go back to" });
    expect(layout.target("current")).toBe("a");
  });

  it("seeds the image release into an empty data dir, and again when the image changes", () => {
    const layout = new ReleaseLayout(join(dir, "data"));
    const seed = join(dir, "seed");
    const id1 = makeRelease(seed, "v1();");
    seedRelease(layout, seed, () => {});
    expect(layout.target("current")).toBe(id1);
    expect(readFileSync(join(layout.dir(id1), "node.mjs"), "utf8")).toBe("v1();");
    // an update moved on; the same image does not reseed
    makeRelease(layout.dir("2.0.0+aaaaaaaa"), "upd();");
    layout.switchTo("2.0.0+aaaaaaaa");
    seedRelease(layout, seed, () => {});
    expect(layout.target("current")).toBe("2.0.0+aaaaaaaa");
    // a new image wins
    writeFileSync(join(seed, "node.mjs"), "v2();");
    seedRelease(layout, seed, () => {});
    expect(layout.target("current")).toBe(`2.0.0+${buildOf(join(seed, "node.mjs"))}`);
    expect(layout.target("previous")).toBe("2.0.0+aaaaaaaa");
  });

  it("restarts the daemon at once on exit 75 and with backoff after a crash", async () => {
    const { layout } = layoutWith();
    const codes = [75, 1, 0];
    const started: string[] = [];
    const sleeps: number[] = [];
    let stopAfter = 3;
    const signals = new EventEmitter();
    const spawnFn = (_cmd: string, args: string[], env: NodeJS.ProcessEnv) => {
      started.push(`${readlinkSync(join(layout.data, "current"))}:${env.AGENT_TEAMS_LAUNCHER}:${args.slice(1).join(" ")}`);
      const child = new EventEmitter() as ChildProcess;
      child.kill = () => true;
      const code = codes.shift() ?? 0;
      setTimeout(() => {
        if (--stopAfter === 0) signals.emit("SIGTERM");
        child.emit("exit", code, null);
      }, 1);
      return child;
    };
    const code = await launch({ data: layout.data, args: ["run"], spawnFn, sleep: async (ms) => void sleeps.push(ms), log: () => {}, checkEveryMs: 1000, signals });
    expect(code).toBe(0);
    expect(started).toEqual(["releases/b:1:run", "releases/b:1:run", "releases/b:1:run"]);
    expect(sleeps).toEqual([1000]);
  });

  it("refuses to start without an installed release", async () => {
    const data = join(dir, "empty");
    mkdirSync(data);
    symlinkSync("releases/none", join(data, "current"));
    expect(await launch({ data, args: ["run"], log: () => {}, signals: new EventEmitter() })).toBe(1);
  });
});
