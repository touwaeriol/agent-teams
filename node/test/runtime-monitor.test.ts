import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import type { NodeRuntimes } from "../src/protocol.js";
import { FileWatch, RuntimeMonitor } from "../src/runtime-monitor.js";
import { ClaudeAdapter, type DetectOptions, type RuntimeDeps, type RuntimeDetector, type RuntimeState } from "../src/sessions/registry.js";
import { fileStamp } from "../src/runtimes.js";
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
  it("re-runs the SDK probe only when forced or when the credentials file or key env changed", async () => {
    const root = tmp();
    const log: FakeQueryLog = { options: [], prompts: [], closed: 0, interrupts: 0, modes: [] };
    const deps = { root, employeeHome: root, claudeConfigDir: join(root, "claude"), codexHome: join(root, "codex"), claudeEnv: () => ({}), queryFn: fakeQueryFactory(log) } as unknown as RuntimeDeps;
    const a = new ClaudeAdapter(deps);
    expect(await a.detect({ force: false })).toEqual({ installed: true, version: "2.1.325-fake", auth: "none" });
    expect(log.options).toHaveLength(1);
    await a.detect({ force: false });
    expect(log.options).toHaveLength(1);
    mkdirSync(join(root, "claude"));
    writeFileSync(join(root, "claude", ".credentials.json"), "{}");
    await a.detect({ force: false });
    expect(log.options).toHaveLength(2);
    await a.detect({ force: true });
    expect(log.options).toHaveLength(3);
    expect(a.watchPaths()).toEqual([join(root, "claude", ".credentials.json")]);
    expect(a.info()).toMatchObject({ cli: "2.1.325-fake", auth: "none" });
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
