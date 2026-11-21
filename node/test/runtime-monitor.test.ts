import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import type { NodeRuntimes } from "../src/protocol.js";
import { FileWatch, RuntimeMonitor } from "../src/runtime-monitor.js";
import { ClaudeAdapter, type DetectOptions, type RuntimeDeps, type RuntimeDetector, type RuntimeState } from "../src/sessions/registry.js";
import { codexFileAuth, fileStamp } from "../src/runtimes.js";
import { silentLogger } from "../src/util/log.js";
import { startHarness, type Harness } from "./helpers/daemon.js";
import { fakeQueryFactory, type FakeQueryLog } from "./helpers/fake-query.js";

class Fake implements RuntimeDetector {
  calls: DetectOptions[] = [];
  paths: string[] = [];
  delayMs = 0;
  fail: Error | null = null;
  constructor(readonly id: string, public state: RuntimeState) {}
  async detect(o: DetectOptions): Promise<RuntimeState> {
    this.calls.push(o);
    if (this.delayMs) await new Promise((r) => setTimeout(r, this.delayMs));
    if (this.fail) throw this.fail;
    return this.state;
  }
  watchPaths(): string[] { return this.paths; }
}

function monitor(detectors: RuntimeDetector[], extra: Partial<ConstructorParameters<typeof RuntimeMonitor>[0]> = {}) {
  const sent: NodeRuntimes[] = [];
  let t = Date.parse("2026-11-21T00:00:00Z");
  const m = new RuntimeMonitor({ detectors: () => detectors, send: (b) => sent.push(b), log: silentLogger, intervalMs: 0, now: () => new Date((t += 1000)), ...extra });
  return { m, sent };
}

const until = async (cond: () => boolean, ms = 5000) => {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) throw new Error("condition not met in time");
    await new Promise((r) => setTimeout(r, 20));
  }
};

const dirs: string[] = [];
const tmp = () => {
  const d = mkdtempSync(join(tmpdir(), "at-rtmon-"));
  dirs.push(d);
  return d;
};
let mon: RuntimeMonitor | null = null;
let h: Harness | null = null;

afterEach(async () => {
  mon?.stop();
  mon = null;
  await h?.close();
  h = null;
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

describe("RuntimeMonitor", () => {
  it("reports hello always, then only changes (checked_at ignored), and always answers refresh", async () => {
    const claude = new Fake("claude", { installed: true, version: "2.1", auth: "none" });
    const codex = new Fake("codex", { installed: true, auth: "chatgpt" });
    const { m, sent } = monitor([claude, codex]);
    await m.hello();
    expect(sent).toHaveLength(1);
    expect(sent[0]!.reason).toBe("hello");
    expect(Object.keys(sent[0]!.runtimes)).toEqual(["claude", "codex"]);
    expect(sent[0]!.runtimes.claude).toMatchObject({ installed: true, version: "2.1", auth: "none" });

    await m.poll("periodic");
    await m.poll("periodic");
    expect(sent).toHaveLength(1);

    claude.state = { installed: true, version: "2.1", auth: "subscription" };
    await m.poll("periodic");
    expect(sent).toHaveLength(2);
    expect(sent[1]).toMatchObject({ reason: "periodic", runtimes: { claude: { auth: "subscription" }, codex: { auth: "chatgpt" } } });
    await m.poll("fs_event");
    expect(sent).toHaveLength(2);

    const r = await m.refresh(undefined, "c_1");
    expect(r).toMatchObject({ reason: "refresh", in_reply_to: "c_1", runtimes: { claude: { auth: "subscription" } } });
    expect(sent).toHaveLength(2);
    expect(claude.calls.at(-1)).toMatchObject({ force: true });

    await m.refresh(["codex"], "c_2");
    expect(claude.calls.at(-1)!.force).toBe(false);
    expect(codex.calls.at(-1)!.force).toBe(true);

    // a reconnect starts again with a hello report, changed or not
    await m.hello();
    expect(sent.map((b) => b.reason)).toEqual(["hello", "periodic", "hello"]);
  });

  it("reports a newly registered runtime", async () => {
    const list: RuntimeDetector[] = [new Fake("claude", { installed: true, auth: "subscription" })];
    const { m, sent } = monitor(list);
    await m.hello();
    list.push(new Fake("gemini", { installed: false, auth: "unknown" }));
    await m.poll("periodic");
    expect(sent).toHaveLength(2);
    expect(sent[1]!.runtimes.gemini).toMatchObject({ installed: false, auth: "unknown" });
  });

  it("turns a failing or hanging detector into auth unknown with an error, and scrubs errors", async () => {
    const bad = new Fake("claude", { installed: true, auth: "subscription" });
    const slow = new Fake("codex", { installed: true, auth: "chatgpt" });
    const leaky = new Fake("gemini", { installed: true, auth: "none", error: "login failed: Bearer abc.def.ghi for me@example.com" });
    const { m, sent } = monitor([bad, slow, leaky], { probeTimeoutMs: 10 });
    await m.hello();
    bad.fail = new Error("boom sk-ant-0123456789abcdef");
    slow.delayMs = 3000;
    await m.poll("periodic");
    const r = sent[1]!.runtimes;
    expect(r.claude).toMatchObject({ installed: true, auth: "unknown" });
    expect(r.claude!.error).toMatch(/^detect_failed: boom \[redacted\]$/);
    expect(r.codex).toMatchObject({ auth: "unknown", error: "detect_failed: timeout" });
    expect(JSON.stringify(sent)).not.toMatch(/abc\.def|example\.com|0123456789abcdef/);
  }, 10_000);

  it("merges concurrent checks: one running, one queued after it", async () => {
    const d = new Fake("claude", { installed: true, auth: "none" });
    d.delayMs = 50;
    const { m } = monitor([d]);
    await Promise.all([m.poll("periodic"), m.poll("fs_event"), m.poll("periodic"), m.refresh(undefined, "c_1"), m.poll("periodic")]);
    expect(d.calls).toHaveLength(2);
    expect(d.calls[0]!.force).toBe(false);
    expect(d.calls[1]!.force).toBe(true);
  });

  it("checks soon after a watched file appears, also when its directory is created later", async () => {
    const root = tmp();
    const file = join(root, "home", ".claude", ".credentials.json");
    const d = new Fake("claude", { installed: true, auth: "none" });
    d.paths = [file];
    // the detector looks at the file like the real ones do: metadata only
    d.detect = async function (this: Fake, o: DetectOptions) {
      this.calls.push(o);
      return { installed: true, auth: fileStamp(file) ? "subscription" : "none" };
    };
    const { m, sent } = monitor([d], { debounceMs: 30 });
    mon = m;
    m.start();
    await m.hello();
    mkdirSync(join(root, "home"));
    await new Promise((r) => setTimeout(r, 100));
    mkdirSync(join(root, "home", ".claude"));
    await new Promise((r) => setTimeout(r, 100));
    writeFileSync(file, "{}");
    await until(() => sent.length === 2);
    expect(sent[1]).toMatchObject({ reason: "fs_event", runtimes: { claude: { auth: "subscription" } } });
    rmSync(file);
    await until(() => sent.length === 3);
    expect(sent[2]).toMatchObject({ reason: "fs_event", runtimes: { claude: { auth: "none" } } });
  });

  it("FileWatch ignores other names and falls back to an ancestor directory", async () => {
    const root = tmp();
    let hits = 0;
    const w = new FileWatch(join(root, "a", "auth.json"), () => hits++);
    w.arm();
    expect(w.watching).toBe(root);
    writeFileSync(join(root, "other"), "x");
    await new Promise((r) => setTimeout(r, 100));
    expect(hits).toBe(0);
    mkdirSync(join(root, "a"));
    await until(() => hits > 0);
    expect(w.watching).toBe(join(root, "a"));
    w.close();
  });
});

describe("ClaudeAdapter.detect", () => {
  function setup(queryFn?: unknown) {
    const root = tmp();
    const log: FakeQueryLog = { options: [], prompts: [], closed: 0, interrupts: 0, modes: [] };
    const env: Record<string, string> = {};
    let t = 1_000_000;
    const deps = { root, employeeHome: root, claudeConfigDir: join(root, "claude"), codexHome: join(root, "codex"), claudeEnv: () => ({ ...env }), queryFn: queryFn ?? fakeQueryFactory(log) } as unknown as RuntimeDeps;
    const a = new ClaudeAdapter(deps, { now: () => t });
    const cred = join(root, "claude", ".credentials.json");
    const writeCred = (body: string) => (mkdirSync(join(root, "claude"), { recursive: true }), writeFileSync(cred, body));
    return { a, log, env, writeCred, tick: (ms: number) => (t += ms) };
  }

  it("re-runs the SDK probe only when forced or when the credentials file changed", async () => {
    const { a, log, writeCred, tick } = setup();
    expect(await a.detect({ force: false })).toEqual({ installed: true, version: "2.1.325-fake", auth: "none" });
    expect(log.options).toHaveLength(1);
    await a.detect({ force: false });
    expect(log.options).toHaveLength(1);
    tick(31_000);
    writeCred("{}");
    await a.detect({ force: false });
    expect(log.options).toHaveLength(2);
    await a.detect({ force: true });
    expect(log.options).toHaveLength(3);
    expect(a.watchPaths()).toEqual([join(a["d"].claudeConfigDir, ".credentials.json")]);
    expect(a.info()).toMatchObject({ cli: "2.1.325-fake", auth: "none" });
  });

  it("re-probes when ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN appears or goes away", async () => {
    const { a, log, env, tick } = setup();
    await a.detect({ force: false });
    tick(31_000);
    env.ANTHROPIC_API_KEY = "sk-test";
    expect(await a.detect({ force: false })).toMatchObject({ auth: "api_key" });
    expect(log.options).toHaveLength(2);
    tick(31_000);
    delete env.ANTHROPIC_API_KEY;
    env.ANTHROPIC_AUTH_TOKEN = "tok";
    await a.detect({ force: false });
    expect(log.options).toHaveLength(2); // still "a key is set": same stamp
    tick(31_000);
    delete env.ANTHROPIC_AUTH_TOKEN;
    expect(await a.detect({ force: false })).toMatchObject({ auth: "none" });
    expect(log.options).toHaveLength(3);
    expect(JSON.stringify(a.info())).not.toContain("sk-test");
  });

  it("guesses from the files when the probe fails and retries after 5 minutes", async () => {
    let probes = 0;
    let fail = true;
    const ok = fakeQueryFactory({ options: [], prompts: [], closed: 0, interrupts: 0, modes: [] });
    const queryFn = (p: Parameters<typeof ok>[0]) => {
      probes++;
      if (!fail) return ok(p);
      return { initializationResult: () => Promise.reject(new Error("spawn failed")), close: () => {} } as unknown as ReturnType<typeof ok>;
    };
    const { a, writeCred, tick } = setup(queryFn);
    writeCred("{}");
    expect(await a.detect({ force: false })).toMatchObject({ installed: true, auth: "subscription", error: "probe_failed: spawn failed" });
    tick(60_000);
    await a.detect({ force: false });
    expect(probes).toBe(1);
    fail = false;
    tick(4 * 60_000);
    expect(await a.detect({ force: false })).toEqual({ installed: true, version: "2.1.325-fake", auth: "none" });
    expect(probes).toBe(2);
  });

  it("waits 30 s between unforced probes and asks for a recheck instead of dropping the change", async () => {
    const { a, log, writeCred, tick } = setup();
    await a.detect({ force: false });
    tick(10_000);
    writeCred("{}");
    const asked: number[] = [];
    await a.detect({ force: false, recheckIn: (ms) => asked.push(ms) });
    expect(log.options).toHaveLength(1);
    expect(asked).toEqual([20_000]);
    await a.detect({ force: true });
    expect(log.options).toHaveLength(2); // forced probes are not held back
    tick(5_000);
    writeCred('{"x":1}');
    await a.detect({ force: false, recheckIn: (ms) => asked.push(ms) });
    expect(asked).toEqual([20_000, 25_000]);
    tick(25_000);
    await a.detect({ force: false });
    expect(log.options).toHaveLength(3);
    await a.detect({ force: false });
    expect(log.options).toHaveLength(3);
  });
});

describe("codexFileAuth", () => {
  it("tells an API key login from a ChatGPT login by the file's shape only", () => {
    const d = tmp();
    const f = join(d, "auth.json");
    const at = (body: string) => (writeFileSync(f, body), codexFileAuth(f));
    expect(codexFileAuth(f)).toBeNull();
    expect(at("")).toBeNull();
    expect(at(JSON.stringify({ OPENAI_API_KEY: "sk-x", tokens: null }))).toBe("api_key");
    expect(at(JSON.stringify({ OPENAI_API_KEY: "sk-x", tokens: { id_token: "a" } }))).toBe("chatgpt");
    expect(at(JSON.stringify({ OPENAI_API_KEY: null, tokens: { id_token: "a" }, last_refresh: "x" }))).toBe("chatgpt");
    expect(at(JSON.stringify({ auth_mode: "apikey", OPENAI_API_KEY: "sk-x" }))).toBe("api_key");
    expect(at(JSON.stringify({ auth_mode: "chatgpt", OPENAI_API_KEY: "sk-x" }))).toBe("chatgpt");
    expect(at("{}")).toBe("chatgpt");
    expect(at("not json")).toBe("chatgpt");
  });
});

describe("RuntimeMonitor recheck", () => {
  it("runs the check a detector put off", async () => {
    const d = new Fake("claude", { installed: true, auth: "none" });
    let asked = false;
    d.detect = async function (this: Fake, o: DetectOptions) {
      this.calls.push(o);
      if (!asked && this.calls.length === 2) (asked = true, o.recheckIn?.(50));
      return this.state;
    };
    const { m, sent } = monitor([d]);
    mon = m;
    await m.hello();
    await m.poll("fs_event");
    d.state = { installed: true, auth: "subscription" };
    await until(() => sent.length === 2);
    expect(sent[1]).toMatchObject({ reason: "fs_event", runtimes: { claude: { auth: "subscription" } } });
    expect(d.calls).toHaveLength(3);
  });
});

describe("daemon node.runtimes", () => {
  it("advertises runtime_status, reports after welcome and answers node.runtimes_refresh with in_reply_to", async () => {
    const hh = (h = await startHarness({ runtimeMonitor: { intervalMs: 0 } }));
    const hello = hh.console.ofType("hello")[0]!;
    expect(hello.body.features).toContain("runtime_status");
    const first = await hh.console.waitType("node.runtimes");
    expect(first.seq).toBeUndefined();
    expect(first.body.reason).toBe("hello");
    expect(first.body.runtimes.claude).toMatchObject({ installed: true, auth: "none" });
    expect(first.body.runtimes.codex).toMatchObject({ installed: true, auth: "none" });

    mkdirSync(hh.opts.codexHome, { recursive: true });
    writeFileSync(join(hh.opts.codexHome, "auth.json"), "{}");
    const cmd = hh.console.unreliable("node.runtimes_refresh", { runtimes: ["codex"] });
    const reply = await hh.console.waitType("node.runtimes", (b) => b.in_reply_to === cmd.id);
    expect(reply.body.reason).toBe("refresh");
    expect(reply.body.runtimes.codex.auth).toBe("chatgpt");
    expect(hh.daemon.runtimes.get("codex").info()).toMatchObject({ auth: "chatgpt" });
    expect(hh.daemon.hello().runtimes?.codex).toMatchObject({ auth: "chatgpt" });
    expect(hh.console.invalid).toEqual([]);
    expect(hh.console.ofType("error")).toEqual([]);
  });
});
