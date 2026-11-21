// The runtime registry: every agent runtime the daemon can run (Claude Agent SDK, Codex
// app-server…) is one RuntimeAdapter. The daemon looks a session's runtime up here for its login
// probe, its environment, its skill handling and its Driver; nothing else branches on the id.
// Adding a runtime = a Driver (sessions/<id>/driver.ts), an adapter below, its id in the protocol
// Runtime enum plus its session.open options block, and its entry in the control plane's registry
// (server/internal/runtimes).
import type { ClaudeRuntime, CodexRuntime, Runtime, Runtimes } from "../protocol.js";
import { probeClaude, probeCodex } from "../runtimes.js";
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

/** One agent runtime as the daemon sees it. */
export interface RuntimeAdapter {
  readonly id: Runtime;
  /** Loads the employee's skills itself (as a local plugin); otherwise the control plane inlines them into the instructions. */
  readonly nativeSkills: boolean;
  /** The login/version info last probed, reported in hello.runtimes. */
  info(): NonNullable<Runtimes[Runtime]>;
  /** Probes the login once at start. */
  probe(): Promise<void>;
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
  toolhost: LoopbackToolHost;
  queryFn?: QueryFn;
}

export class ClaudeAdapter implements RuntimeAdapter {
  readonly id = "claude" as const;
  readonly nativeSkills = true;
  readonly passEnv: readonly string[] = [];
  private current: ClaudeRuntime = { sdk: CLAUDE_SDK_VERSION, auth: "none" };

  constructor(private readonly d: RuntimeDeps) {}

  info(): ClaudeRuntime { return this.current; }
  async probe(): Promise<void> { this.current = await probeClaude(this.d.claudeEnv(), this.d.employeeHome); }
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
  async probe(): Promise<void> { this.current = probeCodex(this.d.codexHome, process.env, this.d.sandboxOk()); }
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

  async probeAll(): Promise<void> { await Promise.all(this.all().map((a) => a.probe())); }

  /** hello.runtimes. */
  hello(): Runtimes {
    return Object.fromEntries(this.all().map((a) => [a.id, a.info()])) as Runtimes;
  }
}
