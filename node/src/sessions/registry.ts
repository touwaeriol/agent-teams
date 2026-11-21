// The runtime registry: every agent runtime the daemon can run (Claude Agent SDK, Codex
// app-server…) is one RuntimeAdapter. The daemon looks a session's runtime up here for its login
// probe, its environment, its skill handling and its Driver; nothing else branches on the id.
// Adding a runtime = a Driver (sessions/<id>/driver.ts), an adapter below (with its detect() and
// watchPaths(): RuntimeMonitor then reports its install/login state in node.runtimes on its own), its id
// in the protocol Runtime enum plus its session.open options block, and its entry in the control
// plane's registry (server/internal/runtimes).
import { existsSync } from "node:fs";
import type { ClaudeRuntime, CodexRuntime, Runtime, Runtimes, RuntimeStatus } from "../protocol.js";
import { claudeCredentialsFile, claudeKeyEnv, codexAuthFile, fileStamp, probeClaude, probeCodex } from "../runtimes.js";
import { codexCommand } from "./codex/appserver.js";
import type { LoopbackToolHost } from "../toolhost/http-server.js";
import { CLAUDE_SDK_VERSION, CODEX_CLI_VERSION } from "../version.js";
import { ClaudeDriver, type QueryFn } from "./claude/driver.js";
import { CodexDriver } from "./codex/driver.js";
import type { CodexHub } from "./codex/hub.js";
import { CODEX_PASS_ENV } from "./env.js";
import { codexMcpServers, type MaterializedPlugin } from "./plugins.js";
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

  constructor(private readonly d: RuntimeDeps, private readonly now: () => number = Date.now) {}

  info(): ClaudeRuntime { return this.current; }

  watchPaths(): string[] { return [claudeCredentialsFile(this.d.claudeConfigDir)]; }

  /**
   * The SDK probe spawns the CLI, so it runs only when forced, when the credentials file or the key env
   * changed since the last probe, or (after a failed probe) every 5 minutes.
   */
  async detect(o: DetectOptions): Promise<RuntimeState> {
    const env = this.d.claudeEnv();
    const credentialsFile = claudeCredentialsFile(this.d.claudeConfigDir);
    const stamp = `${fileStamp(credentialsFile) ?? "-"}|${claudeKeyEnv(env) ? "key" : "-"}`;
    if (!o.force && this.last && stamp === this.stamp && (!this.last.error || this.now() < this.retryAt)) return this.last;
    const p = await probeClaude(env, this.d.employeeHome, { credentialsFile, timeoutMs: o.timeoutMs ?? 12_000, ...(this.d.queryFn ? { queryFn: this.d.queryFn } : {}) });
    this.stamp = stamp;
    this.retryAt = this.now() + 5 * 60_000;
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

/** The runtimes of this daemon, by id. */
export class RuntimeRegistry {
  private readonly byId: Map<Runtime, RuntimeAdapter>;

  constructor(adapters: RuntimeAdapter[]) {
    this.byId = new Map(adapters.map((a) => [a.id, a]));
  }

  static builtin(d: RuntimeDeps): RuntimeRegistry {
    return new RuntimeRegistry([new ClaudeAdapter(d), new CodexAdapter(d)]);
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
