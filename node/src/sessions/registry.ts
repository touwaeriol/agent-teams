// The runtime registry: every agent runtime the daemon can run (Claude Agent SDK, Codex
// app-server…) is one RuntimeAdapter. The daemon looks a session's runtime up here for its login
// probe, its environment, its skill handling and its Driver; nothing else branches on the id.
// Adding a runtime = a Driver (sessions/<id>/driver.ts), an adapter below (with its detect() and
// watchPaths(): RuntimeMonitor then reports its install/login state in node.runtimes on its own), its id
// in the protocol Runtime enum plus its session.open options block, and its entry in the control
// plane's registry (server/internal/runtimes).
import { existsSync } from "node:fs";
import { join } from "node:path";
import type { AntigravityRuntime, ClaudeRuntime, CodexRuntime, Runtime, Runtimes, RuntimeStatus } from "../protocol.js";
import { claudeCredentialsFile, claudeKeyEnv, codexAuthFile, fileStamp, probeClaude, probeCodex } from "../runtimes.js";
import { codexCommand } from "./codex/appserver.js";
import type { LoopbackToolHost } from "../toolhost/http-server.js";
import { silentLogger, type Logger } from "../util/log.js";
import { CLAUDE_SDK_VERSION, CODEX_CLI_VERSION } from "../version.js";
import { ClaudeDriver, type QueryFn } from "./claude/driver.js";
import { CodexDriver } from "./codex/driver.js";
import type { CodexHub } from "./codex/hub.js";
import { AntigravityDriver } from "./antigravity/driver.js";
import { AGY_PASS_ENV, AGY_TOKEN_FILES, detectInstalled, detectLogin, probeAntigravity, writeAgySettings } from "./antigravity/detect.js";
import { CODEX_PASS_ENV } from "./env.js";
import { acpMcpServers, codexMcpServers, type MaterializedPlugin } from "./plugins.js";
import type { MaterializedSkills } from "./skills.js";
import type { Driver, DriverContext } from "./types.js";

/** What a session brings beyond its spec: skill bundle and plugin copies, already on disk. */
export interface SessionAssets {
  skills: MaterializedSkills | null;
  plugins: MaterializedPlugin[];
}

/** A runtime's install/login state without the check time (node.runtimes, protocol.md §9.2). */
export type RuntimeState = Omit<RuntimeStatus, "checked_at">;

export interface DetectOptions {
  /** Re-run the expensive part of the check even when the watched files look unchanged (start, node.runtimes_refresh). */
  force: boolean;
  /** Budget of a check that has to spawn something (the Claude SDK probe). */
  timeoutMs?: number;
  /**
   * A detector that put off part of an unforced check (e.g. a probe too soon after the last one) asks
   * for another unforced check in `ms`, so the change it saw is not lost.
   */
  recheckIn?: (ms: number) => void;
}

/**
 * Detects one runtime's CLI and login. Implementations only stat credential files or read non-secret
 * fields; nothing they return may contain a credential. Errors go into `error` (a thrown error is
 * turned into auth "unknown" by RuntimeMonitor).
 */
export interface RuntimeDetector {
  readonly id: string;
  /** Checks now and updates what info() and the sessions see. Cheap when not forced. */
  detect(o: DetectOptions): Promise<RuntimeState>;
  /** Files whose change (create, rewrite, delete) should trigger a check: credential files, CLI executables. */
  watchPaths(): string[];
}

/** One agent runtime as the daemon sees it. */
export interface RuntimeAdapter extends RuntimeDetector {
  readonly id: Runtime;
  /** Loads the employee's skills itself (as a local plugin); otherwise the control plane inlines them into the instructions. */
  readonly nativeSkills: boolean;
  /** The login/version info last detected, reported in hello.runtimes and used by node.usage. */
  info(): NonNullable<Runtimes[Runtime]>;
  /** Environment names passed through from the daemon to this runtime's sessions, beyond the node's own pass list. */
  readonly passEnv: readonly string[];
  /** Fixed environment of this runtime's sessions. */
  fixedEnv(): Record<string, string>;
  /** The runtime behind one logical session. */
  createDriver(ctx: DriverContext, assets: SessionAssets): Driver;
}

/** What the built-in adapters need from the daemon. */
export interface RuntimeDeps {
  root: string;
  employeeHome: string;
  claudeConfigDir: string;
  codexHome: string;
  /** Whether the node isolates sessions (Codex's sandbox is usable). */
  sandboxOk: () => boolean;
  claudeEnv: () => Record<string, string>;
  claudeFixed: () => Record<string, string>;
  secretPaths: () => string[];
  hub: CodexHub;
  /** The codex command when not the pinned package (tests); its last element is the script/executable checked. */
  codexCommand?: string[];
  toolhost: LoopbackToolHost;
  queryFn?: QueryFn;
  /** HOME of every agy process on this node (its login, settings and conversations live there). */
  agyHome?: string;
  /** The agy command (tests: a fake ACP agent); default: detectInstalled (AGENT_TEAMS_AGY_BIN, PATH, ~/.local/bin). */
  agyCommand?: string[];
  /** Environment agy itself runs with for the probe (the node's pass list plus AGY_PASS_ENV). */
  agyEnv?: () => Record<string, string>;
  log?: Logger;
  /** Asks RuntimeMonitor for a check now (a session saw a runtime's state change). */
  recheck?: () => void;
}

export class ClaudeAdapter implements RuntimeAdapter {
  readonly id = "claude" as const;
  readonly nativeSkills = true;
  readonly passEnv: readonly string[] = [];
  private current: ClaudeRuntime = { sdk: CLAUDE_SDK_VERSION, auth: "none" };
  private last: RuntimeState | null = null;
  /** Credentials file + key env as of the last SDK probe; the probe re-runs only when this changes. */
  private stamp = "";
  private retryAt = 0;
  /** When the last SDK probe ended (0: never). */
  private probedAt = 0;
  private readonly now: () => number;
  private readonly minProbeIntervalMs: number;

  constructor(private readonly d: RuntimeDeps, o: { now?: () => number; minProbeIntervalMs?: number } = {}) {
    this.now = o.now ?? Date.now;
    this.minProbeIntervalMs = o.minProbeIntervalMs ?? 30_000;
  }

  info(): ClaudeRuntime { return this.current; }

  watchPaths(): string[] { return [claudeCredentialsFile(this.d.claudeConfigDir)]; }

  /**
   * The SDK probe spawns the CLI, so it runs only when forced, when the credentials file or the key env
   * changed since the last probe, or (after a failed probe) every 5 minutes. An unforced probe also waits
   * at least minProbeIntervalMs after the previous one (the CLI may rewrite its credentials file while
   * starting, which must not turn into a probe loop): until then the last result stands and a recheck is
   * asked for. The baseline is the stamp after the probe, so the probe's own writes do not count.
   */
  async detect(o: DetectOptions): Promise<RuntimeState> {
    const env = this.d.claudeEnv();
    const credentialsFile = claudeCredentialsFile(this.d.claudeConfigDir);
    const stampNow = () => `${fileStamp(credentialsFile) ?? "-"}|${claudeKeyEnv(env) ? "key" : "-"}`;
    if (!o.force && this.last) {
      const due = stampNow() !== this.stamp || (this.last.error !== undefined && this.now() >= this.retryAt);
      if (!due) return this.last;
      const wait = this.probedAt + this.minProbeIntervalMs - this.now();
      if (wait > 0) {
        o.recheckIn?.(wait);
        return this.last;
      }
    }
    const p = await probeClaude(env, this.d.employeeHome, { credentialsFile, timeoutMs: o.timeoutMs ?? 12_000, ...(this.d.queryFn ? { queryFn: this.d.queryFn } : {}) });
    this.stamp = stampNow();
    this.probedAt = this.now();
    this.retryAt = this.probedAt + 5 * 60_000;
    const cli = p.cli ?? this.current.cli;
    this.current = { sdk: CLAUDE_SDK_VERSION, ...(cli ? { cli } : {}), auth: p.auth };
    this.last = { installed: true, ...(cli ? { version: cli } : {}), auth: p.auth, ...(p.error ? { error: p.error } : {}) };
    return this.last;
  }
  fixedEnv(): Record<string, string> { return this.d.claudeFixed(); }

  createDriver(ctx: DriverContext, a: SessionAssets): Driver {
    return new ClaudeDriver(ctx, { root: this.d.root, claudeConfigDir: this.d.claudeConfigDir, secretPaths: this.d.secretPaths(), skills: a.skills, plugins: a.plugins, ...(this.d.queryFn ? { queryFn: this.d.queryFn } : {}) });
  }
}

export class CodexAdapter implements RuntimeAdapter {
  readonly id = "codex" as const;
  readonly nativeSkills = false;
  readonly passEnv: readonly string[] = CODEX_PASS_ENV;
  private current: CodexRuntime = { cli: CODEX_CLI_VERSION, auth: "none", sandbox_ok: false };

  constructor(private readonly d: RuntimeDeps) {}

  info(): CodexRuntime { return this.current; }

  watchPaths(): string[] { return [codexAuthFile(this.d.codexHome)]; }

  /** Stats auth.json and the pinned CLI; no process is spawned. */
  async detect(): Promise<RuntimeState> {
    this.current = probeCodex(this.d.codexHome, process.env, this.d.sandboxOk());
    let bin: string | undefined;
    try {
      bin = (this.d.codexCommand ?? codexCommand()).at(-1);
    } catch {
      bin = undefined;
    }
    if (!bin || !existsSync(bin)) return { installed: false, auth: "unknown", error: "codex CLI not found" };
    return { installed: true, version: this.current.cli ?? CODEX_CLI_VERSION, path: bin, auth: this.current.auth ?? "none" };
  }
  fixedEnv(): Record<string, string> { return {}; }

  createDriver(ctx: DriverContext, a: SessionAssets): Driver {
    return new CodexDriver(ctx, { hub: this.d.hub, toolhost: this.d.toolhost, pluginServers: codexMcpServers(a.plugins) });
  }
}

/**
 * Google Antigravity CLI (agy) over ACP, one process per session (sessions/antigravity). agy has
 * no CODEX_HOME-like variable, so all its processes get one node-level HOME (agyHome) where an
 * admin logs in once (`HOME=<agyHome> agy acp --login`); auto update is turned off. No usage report.
 * Detection (detectAntigravity): agy is looked up and `agy --version` / the login check run again
 * only when forced or when the binary, a token file under agyHome or the login env changed.
 */
export class AntigravityAdapter implements RuntimeAdapter {
  readonly id = "antigravity" as const;
  readonly nativeSkills = false;
  readonly passEnv: readonly string[] = AGY_PASS_ENV;
  private current: AntigravityRuntime = { cli: "", auth: "none", installed: false };
  private command: string[] | null = null;
  private last: RuntimeState | null = null;
  /** Binary + token files + login env as of the last probe; an unforced check re-probes only when this changes. */
  private stamp = "";
  /**
   * When an unforced check probes again although nothing it watches changed: the login is not seen in
   * the env or a token file (logged out, or logged in where we do not look, e.g. a token path not yet
   * verified or the keyring), so only the ACP check can tell; Infinity otherwise.
   */
  private retryAt = Infinity;
  private readonly now: () => number;
  private readonly retryMs: number;
  private readonly maxProbeMs: number;

  constructor(private readonly d: RuntimeDeps, o: { now?: () => number; retryMs?: number; maxProbeMs?: number } = {}) {
    this.now = o.now ?? Date.now;
    this.retryMs = o.retryMs ?? 5 * 60_000;
    this.maxProbeMs = o.maxProbeMs ?? AGY_PROBE_MAX_MS;
  }

  get home(): string { return this.d.agyHome ?? join(this.d.root, "agy-home"); }

  info(): AntigravityRuntime { return this.current; }

  /** The agy command, looked up again when the last probe did not find it or the binary went away. */
  resolveCommand(): string[] | null {
    if (this.d.agyCommand) return this.d.agyCommand;
    if (this.command && !existsSync(this.command[0]!)) this.command = null;
    if (this.command) return this.command;
    const bin = detectInstalled({ explicit: process.env.AGENT_TEAMS_AGY_BIN, homes: [this.home, ...(process.env.HOME ? [process.env.HOME] : [])] });
    this.command = bin ? [bin] : null;
    return this.command;
  }

  private env(): Record<string, string> { return { ...(this.d.agyEnv?.() ?? {}), ...this.fixedEnv() }; }

  private stampOf(command: string[] | null, env: Record<string, string>): string {
    const bin = command?.at(-1) ?? "";
    return [bin, bin ? fileStamp(bin) ?? "-" : "-", ...this.watchPaths().map((f) => fileStamp(f) ?? "-"), env.GEMINI_API_KEY ? "key" : "-", env.AGY_ADC_AUTH ?? "-"].join("|");
  }

  /** RuntimeDetector: installed/version/login of agy (detectInstalled, detectVersion, detectLogin with the ACP fallback). */
  async detect(o: DetectOptions): Promise<RuntimeState> {
    const env = this.env();
    const command = this.resolveCommand();
    if (!o.force && this.last && this.stampOf(command, env) === this.stamp && this.now() < this.retryAt) return this.last;
    // At most AGY_PROBE_MAX_MS even when the caller allows more (the daemon's start check), so agy never holds up hello.
    const timeoutMs = Math.min(o.timeoutMs ?? this.maxProbeMs, this.maxProbeMs);
    this.current = await probeAntigravity({ home: this.home, env, command, log: this.d.log ?? silentLogger, timeoutMs });
    if (this.current.installed) writeAgySettings(this.home, env, this.d.log);
    this.stamp = this.stampOf(command, env);
    this.retryAt = this.current.installed !== false && (this.current.auth === "none" || !detectLogin(this.home, env)) ? this.now() + this.retryMs : Infinity;
    this.last = antigravityState(this.current, command);
    return this.last;
  }

  /** Token files whose change means a login or logout (RuntimeMonitor watches them). */
  watchPaths(): string[] { return AGY_TOKEN_FILES.map((f) => join(this.home, f)); }

  fixedEnv(): Record<string, string> {
    return { HOME: this.home, AGY_CLI_DISABLE_AUTO_UPDATE: "true" };
  }

  createDriver(ctx: DriverContext, a: SessionAssets): Driver {
    return new AntigravityDriver(ctx, { toolhost: this.d.toolhost, command: () => this.resolveCommand(), pluginServers: acpMcpServers(a.plugins), onStatus: (s) => this.noteStatus(s) });
  }

  /**
   * What a session start saw: a session that opened means agy is installed and logged in, -32000
   * means logged out, a missing binary means not installed. Taken as the current state and handed
   * to RuntimeMonitor at once (d.recheck), without spawning agy again.
   */
  noteStatus(s: "opened" | "not_logged_in" | "not_installed"): void {
    const before = JSON.stringify(this.current);
    if (s === "not_installed") {
      this.command = null;
      this.current = { cli: "", auth: "none", installed: false };
    } else if (s === "not_logged_in") {
      this.current = { ...this.current, installed: true, auth: "none" };
    } else if (this.current.auth === "none" || this.current.installed === false) {
      this.current = { ...this.current, installed: true, auth: detectLogin(this.home, this.env()) ?? "oauth" };
    }
    if (JSON.stringify(this.current) === before) return;
    this.last = antigravityState(this.current, this.command);
    this.d.recheck?.();
  }
}

/** Budget of one agy check (version + ACP login check), whatever the caller allows. */
export const AGY_PROBE_MAX_MS = 15_000;

/**
 * node.runtimes' view of hello.runtimes.antigravity. The protocol's auth has no Google-specific
 * kinds: an OAuth (Google account) login is a provider account login (subscription), ADC a
 * configured credential like an API key.
 */
export function antigravityState(r: AntigravityRuntime, command: string[] | null): RuntimeState {
  if (r.installed === false) return { installed: false, auth: "unknown", error: "agy not found" };
  const auth = r.auth === "oauth" ? "subscription" : r.auth === "api_key" || r.auth === "adc" ? "api_key" : "none";
  const path = command?.at(-1);
  return { installed: true, ...(r.cli ? { version: r.cli } : {}), ...(path && path.startsWith("/") ? { path } : {}), auth };
}

/** The runtimes of this daemon, by id. */
export class RuntimeRegistry {
  private readonly byId: Map<Runtime, RuntimeAdapter>;

  constructor(adapters: RuntimeAdapter[]) {
    this.byId = new Map(adapters.map((a) => [a.id, a]));
  }

  static builtin(d: RuntimeDeps): RuntimeRegistry {
    return new RuntimeRegistry([new ClaudeAdapter(d), new CodexAdapter(d), new AntigravityAdapter(d)]);
  }

  /** The adapter of a session's runtime; an id this daemon does not know is an error the session reports. */
  get(id: Runtime): RuntimeAdapter {
    const a = this.byId.get(id);
    if (!a) throw new Error(`runtime ${id} is not supported by this node`);
    return a;
  }

  has(id: string): id is Runtime { return this.byId.has(id as Runtime); }

  all(): RuntimeAdapter[] { return [...this.byId.values()]; }

  /** Checks every runtime once (start); RuntimeMonitor does the later checks. */
  async detectAll(o: DetectOptions): Promise<void> { await Promise.all(this.all().map((a) => a.detect(o).catch(() => undefined))); }

  /** hello.runtimes. */
  hello(): Runtimes {
    return Object.fromEntries(this.all().map((a) => [a.id, a.info()])) as Runtimes;
  }
}
