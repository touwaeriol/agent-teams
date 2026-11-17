import { cpus, hostname, loadavg, totalmem } from "node:os";
import { existsSync, mkdirSync, statfsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { ApprovalBroker } from "./approvals.js";
import { CredBroker } from "./cred/broker.js";
import { gitCredentialEnv } from "./cred/helper.js";
import { CredServer, credSocketPath } from "./cred/server.js";
import { GitJobs } from "./git/jobs.js";
import { GhInstaller, type GhInstallOptions } from "./git/gh-install.js";
import { installPlatformFiles, type PlatformFiles } from "./git/platform.js";
import { probeIsolation, type Isolation } from "./isolation.js";
import { Link } from "./link/client.js";
import { Dispatcher, type CommandHandler } from "./link/dispatcher.js";
import { Inbox, type Reply } from "./link/inbox.js";
import { Outbox } from "./link/outbox.js";
import type { ApprovalResolve, ClaudeRuntime, CodexRuntime, CredResponse, FileDownload, Frame, GitCleanup, GitPrepare, GitPurge, GitPush, GitResult, GitStatus, Hello, NodeConfig, NodeUpgrade, SessionClose, SessionInterrupt, SessionOpen, SessionSend, SessionSetMode, SessionWatch, ToolResult, Welcome } from "./protocol.js";
import { probeClaude, probeCodex } from "./runtimes.js";
import { ClaudeDriver, type QueryFn } from "./sessions/claude/driver.js";
import { CodexDriver } from "./sessions/codex/driver.js";
import { CodexHub } from "./sessions/codex/hub.js";
import { materializeSkills } from "./sessions/skills.js";
import { codexMcpServers, materializePlugins } from "./sessions/plugins.js";
import { buildEnv, CODEX_PASS_ENV, gitIdentity } from "./sessions/env.js";
import { SessionManager } from "./sessions/manager.js";
import type { ToolPolicy } from "./sessions/policy.js";
import type { Session } from "./sessions/session.js";
import type { Driver, DriverContext } from "./sessions/types.js";
import { LoopbackToolHost } from "./toolhost/http-server.js";
import { ToolBroker } from "./toolhost/broker.js";
import { applyUpgrade } from "./upgrade.js";
import type { Logger } from "./util/log.js";
import { absPath } from "./sessions/claude/options.js";
import { consoleImageFetcher } from "./sessions/images.js";
import { DAEMON_VERSION, PROTOCOLS, CLAUDE_SDK_VERSION, CODEX_CLI_VERSION } from "./version.js";

export interface DaemonOptions {
  /** ws(s)://<console>/nodes/connect */
  url: string;
  token: string;
  root: string;
  stateDir: string;
  configDir: string;
  platformDir: string;
  runtimeDir: string;
  maxActive?: number;
  passEnv: string[];
  gitBaseUrl: string;
  claudeConfigDir: string;
  codexHome: string;
  installDir: string;
  /** How to invoke this daemon for helper subcommands (git-credential, gh). */
  nodeCmd: string[];
  log: Logger;
  probeRuntimes?: boolean;
  isolation?: Isolation;
  queryFn?: QueryFn;
  codexCommand?: string[];
  link?: { pingMs?: number; deadMs?: number; backoff?: { minMs: number; maxMs: number } };
  crashDelaysMs?: number[];
  onExit?: (code: number) => void;
  /** Where the GitHub CLI is downloaded when the node has none (default <data>/tools). */
  toolsDir?: string;
  /** Overrides for the gh download (tests). */
  gh?: Partial<GhInstallOptions>;
  /** Download gh at start instead of on first use (default: when probing runtimes). */
  prefetchGh?: boolean;
}

const FEATURES = ["sessions", "codex_app_server", "git_worktree", "cred_helper", "upgrade", "file_download"];

/** Wires the link, command handlers, sessions, tools, git and credentials together. */
export class Daemon {
  readonly link: Link;
  readonly sessions: SessionManager;
  readonly git: GitJobs;
  private readonly outbox: Outbox;
  private readonly inbox: Inbox;
  private readonly dispatcher: Dispatcher;
  private readonly approvals: ApprovalBroker;
  private readonly tools: ToolBroker;
  private readonly creds: CredBroker;
  private readonly credServer: CredServer;
  private readonly toolhost: LoopbackToolHost;
  private readonly hub: CodexHub;
  private readonly platform: PlatformFiles;
  private readonly gh: GhInstaller;
  private readonly employeeHome: string;
  /** Per-employee skill plugins (sessions/skills.ts), outside every worktree. */
  private readonly skillsRoot: string;
  /** Per-employee plugin copies at exact versions (sessions/plugins.ts), outside every worktree. */
  private readonly pluginsRoot: string;
  private readonly isolation: Isolation;
  private claudeRuntime: ClaudeRuntime = { sdk: CLAUDE_SDK_VERSION, auth: "none" };
  private codexRuntime: CodexRuntime = { cli: CODEX_CLI_VERSION, auth: "none", sandbox_ok: false };
  private statsTimer: NodeJS.Timeout | null = null;

  constructor(private readonly o: DaemonOptions) {
    for (const d of [o.root, o.stateDir]) mkdirSync(d, { recursive: true, mode: 0o700 });
    this.employeeHome = join(o.root, "agent-home");
    mkdirSync(this.employeeHome, { recursive: true, mode: 0o700 });
    this.skillsRoot = join(o.root, "agent-skills");
    this.pluginsRoot = join(o.root, "agent-plugins");
    this.isolation = o.isolation ?? probeIsolation();
    this.outbox = new Outbox(o.stateDir);
    this.inbox = new Inbox(o.stateDir);
    this.link = new Link({ url: o.url, token: o.token, outbox: this.outbox, hello: () => this.hello(), log: o.log, ...o.link });
    const send = (type: string, body: object) => this.link.send(type, body);
    this.approvals = new ApprovalBroker(send);
    this.tools = new ToolBroker(send);
    this.creds = new CredBroker(send);
    this.platform = installPlatformFiles(o.platformDir, o.nodeCmd);
    const dataDir = dirname(o.platformDir);
    this.gh = new GhInstaller({ toolsDir: o.toolsDir ?? join(dataDir, "tools"), path: process.env.PATH ?? "", skip: [this.platform.binDir], ...o.gh });
    this.credServer = new CredServer(credSocketPath(o.runtimeDir), this.creds, o.log, { gh: () => this.gh.resolve(), ghConfigRoot: join(dataDir, "gh-config") });
    this.toolhost = new LoopbackToolHost(o.log);
    this.hub = new CodexHub({ env: this.codexServerEnv(), log: o.log, clientVersion: DAEMON_VERSION, ...(o.codexCommand ? { command: o.codexCommand } : {}) });
    this.git = new GitJobs({ root: o.root, platform: this.platform, gitBaseUrl: o.gitBaseUrl, mirrorDir: join(o.stateDir, "mirrors"), log: o.log, credential: (job, repo, purpose) => this.jobCredential(job, repo, purpose), lookup: (id) => this.recordedResult(id) });
    this.sessions = new SessionManager({
      root: o.root, stateDir: o.stateDir, send, approvals: this.approvals, tools: this.tools, log: o.log, maxActive: o.maxActive,
      waitForJob: (id, ms) => this.git.waitFor(id, ms),
      createDriver: (spec, cwd, sink) => this.createDriver(spec, cwd, sink),
      onSessionGone: (id) => this.forgetSession(id),
      fetchImage: consoleImageFetcher(o.url, o.token),
      ...(o.crashDelaysMs ? { crashDelaysMs: o.crashDelaysMs } : {}),
    });
    this.dispatcher = new Dispatcher(this.link, this.inbox, this.handlers(), o.log);
    this.link.on("frame", (f: Frame) => this.dispatcher.handle(f));
    this.link.on("welcome", (w: Welcome) => this.onWelcome(w));
    this.link.on("stopped", (why: string) => o.onExit?.(why === "replaced" ? 3 : 0));
  }

  async start(): Promise<void> {
    await this.credServer.start();
    await this.toolhost.start();
    if (this.o.probeRuntimes !== false) await this.probe();
    if (this.o.prefetchGh ?? this.o.probeRuntimes !== false) this.prefetchGh();
    this.cancelOrphans();
    this.dispatcher.resumeUnfinished();
    this.sessions.startTimers();
    this.link.start();
  }

  async stop(): Promise<void> {
    if (this.statsTimer) clearInterval(this.statsTimer);
    this.link.stop();
    await this.sessions.shutdown();
    this.hub.stop();
    await this.toolhost.stop();
    await this.credServer.stop();
  }

  /** Fetches gh in the background so the first `gh` in a session does not wait for the download. */
  private prefetchGh(): void {
    this.gh.resolve().then(
      (bin) => this.o.log.info({ gh: bin }, "gh ready"),
      (err: Error) => this.o.log.warn({ err: err.message }, "gh download failed; retried on first use"),
    );
  }

  private async probe(): Promise<void> {
    this.claudeRuntime = await probeClaude(this.claudeEnv({}), this.employeeHome);
    this.codexRuntime = probeCodex(this.o.codexHome, process.env, this.isolation !== "none");
  }

  hello(): Hello {
    return {
      protocols: [...PROTOCOLS],
      daemon: { version: DAEMON_VERSION, node: process.versions.node, os: process.platform, arch: process.arch },
      epoch: this.outbox.epoch,
      last_seq: this.outbox.lastSentSeq,
      acked_cmd_seq: this.inbox.acked,
      host: hostname(),
      root: resolve(this.o.root),
      capacity: { ...(this.o.maxActive ? { max_active_sessions: this.o.maxActive } : {}), cpus: cpus().length, mem_mb: Math.round(totalmem() / 1048576) },
      runtimes: { claude: this.claudeRuntime, codex: this.codexRuntime },
      features: existsSync(join(this.o.configDir, "release.pub")) ? FEATURES : FEATURES.filter((f) => f !== "upgrade"),
      isolated: false,
      isolation: this.isolation,
    };
  }

  /** Requests and tool calls whose Promise died with the previous process become cancels (protocol.md §5.3, §6.2). */
  private cancelOrphans(): void {
    this.outbox.rewritePending((f) => {
      const b = f.body as { approval_id?: string; call_id?: string; session_id: string };
      if (f.type === "approval.request") return [{ type: "approval.cancel", body: { approval_id: b.approval_id, session_id: b.session_id, reason: "process_restarted" } }];
      if (f.type === "tool.call") return [{ type: "tool.cancel", body: { call_id: b.call_id, session_id: b.session_id, reason: "process_restarted" } }];
      return null;
    });
  }

  private onWelcome(w: Welcome): void {
    this.sessions.configure(w.config ?? {});
    if (w.config?.log_level) this.o.log.level = w.config.log_level;
    this.link.send("sessions.report", this.sessions.report());
    this.statsTimer ??= setInterval(() => this.sendStats(), 30_000);
  }

  private sendStats(): void {
    const s = this.sessions.stats;
    const fs = statfsSync(this.o.root);
    this.link.send("node.stats", {
      active_sessions: s.active, parked_sessions: s.parked, queued: s.queued,
      mem_mb: Math.round(process.memoryUsage().rss / 1048576), load1: loadavg()[0] ?? 0, disk_free_mb: Math.round((fs.bavail * fs.bsize) / 1048576),
    });
  }

  private handlers(): Partial<Record<string, CommandHandler>> {
    const session = (b: Record<string, unknown>) => `s:${b.session_id}`;
    const s = this.sessions;
    return {
      "session.open": { lane: session, run: (b) => s.open(b as unknown as SessionOpen) },
      "session.release": { lane: session, run: async (b) => s.release(b as { session_id: string; open_gen: number }) },
      "session.send": { lane: session, run: (b) => s.sendMessage(b as unknown as SessionSend) },
      "session.interrupt": { run: (b) => s.interrupt(b as unknown as SessionInterrupt) },
      "session.set_mode": { lane: session, run: (b) => s.setMode(b as unknown as SessionSetMode) },
      "session.close": { lane: session, run: (b) => s.close(b as unknown as SessionClose) },
      "session.watch": { run: async (b) => s.watch(b as unknown as SessionWatch) },
      "approval.resolve": { run: async (b) => [{ type: "approval.ack", body: this.approvals.resolve(b as unknown as ApprovalResolve) }] },
      "tool.result": { run: async (b) => (this.tools.resolve(b as unknown as ToolResult), []) },
      "file.download": { run: async (b) => (this.tools.chunk(b as unknown as FileDownload), []) },
      "cred.response": { run: async (b) => (this.creds.resolve(b as unknown as CredResponse), []) },
      "git.prepare": { run: (b) => this.gitReply(this.git.prepare(b as unknown as GitPrepare)) },
      "git.status": { run: (b) => this.gitReply(this.git.status(b as unknown as GitStatus)) },
      "git.push": { run: (b) => this.gitReply(this.git.push(b as unknown as GitPush)) },
      "git.cleanup": { run: (b) => this.gitReply(this.git.cleanup(b as unknown as GitCleanup)) },
      "git.purge": { run: (b) => this.gitReply(this.git.purge(b as unknown as GitPurge, (abs) => this.sessions.busyUnder(abs))) },
      "node.config": { run: async (b) => this.nodeConfig(b as NodeConfig) },
      "node.drain": { run: async () => (this.sessions.drain(), []) },
      "node.upgrade": { run: (b) => this.upgrade(b as unknown as NodeUpgrade) },
    };
  }

  private async gitReply(p: Promise<GitResult>): Promise<Reply[]> {
    return [{ type: "git.result", body: await p }];
  }

  private nodeConfig(c: NodeConfig): Reply[] {
    this.sessions.configure(c);
    if (c.log_level) this.o.log.level = c.log_level;
    return [];
  }

  private async upgrade(msg: NodeUpgrade): Promise<Reply[]> {
    const consoleHttpBase = this.o.url.replace(/^ws/, "http").replace(/\/nodes(\/v2)?\/connect$/, "");
    try {
      await applyUpgrade(msg, { consoleHttpBase, installDir: this.o.installDir, releaseKeyPath: join(this.o.configDir, "release.pub") });
    } catch (err) {
      return [{ type: "node.upgrade_result", body: { ok: false, version: msg.version, error: (err as Error).message } }];
    }
    setTimeout(() => this.o.onExit?.(75), 500);
    return [];
  }

  private recordedResult(jobId: string): GitResult | undefined {
    const replies = this.inbox.repliesFor(`git.prepare:${jobId}`);
    return replies?.find((r) => r.type === "git.result")?.body as GitResult | undefined;
  }

  private async jobCredential(jobId: string, repo: string, purpose: "read" | "push") {
    const cred = await this.creds.get({ job_id: jobId }, "github.com", `${repo}.git`, purpose);
    return { username: cred.username, password: cred.password };
  }

  // ---- session runtime construction ----

  private createDriver(spec: SessionOpen, absCwd: string, sink: Session): { driver: Driver; ctx: DriverContext } {
    const skills = spec.runtime === "claude" ? materializeSkills(this.skillsRoot, spec) : null;
    const plugins = materializePlugins(this.pluginsRoot, spec);
    const readable = [...(skills ? [skills.pluginDir] : []), ...plugins.map((p) => p.dir)];
    const ctx: DriverContext = { spec, absCwd, env: this.sessionEnv(spec), policy: this.policy(spec, absCwd, readable), sink, log: this.o.log.child({ session: spec.session_id }) };
    const driver = spec.runtime === "codex"
      ? new CodexDriver(ctx, { hub: this.hub, toolhost: this.toolhost, pluginServers: codexMcpServers(plugins) })
      : new ClaudeDriver(ctx, { root: this.o.root, claudeConfigDir: this.o.claudeConfigDir, secretPaths: this.secretPaths(), skills, plugins, ...(this.o.queryFn ? { queryFn: this.o.queryFn } : {}) });
    return { driver, ctx };
  }

  private policy(spec: SessionOpen, absCwd: string, readable: string[]): ToolPolicy {
    const c = spec.claude;
    const extra = (c?.sandbox?.extra_write_paths ?? []).map((p) => absPath(p, this.o.root, absCwd)).filter(Boolean);
    return {
      denyNonPlatform: c?.deny_non_platform_tools === true || spec.employee.tier === "coordinator",
      readOnly: c?.read_only === true,
      bash: c?.bash_policy ?? null,
      readRoots: [absCwd, ...extra, ...readable],
      secretPaths: this.secretPaths(),
    };
  }

  secretPaths(): string[] {
    return [this.o.configDir, this.o.stateDir, this.o.runtimeDir, join(this.o.claudeConfigDir, ".credentials.json"), this.o.codexHome].map((p) => resolve(p));
  }

  private sessionEnv(spec: SessionOpen): Record<string, string> {
    const cred = this.credEnv(spec);
    const fixed = spec.runtime === "claude" ? this.claudeFixed() : {};
    return buildEnv({
      base: process.env, home: this.employeeHome, extra: { ...gitIdentity(spec.employee.name), ...(spec.env ?? {}) },
      passList: spec.runtime === "codex" ? [...this.o.passEnv, ...CODEX_PASS_ENV] : this.o.passEnv,
      fixed: { ...fixed, ...cred }, pathPrefix: cred.AT_CRED_SOCK ? [this.platform.binDir] : [],
    });
  }

  private claudeEnv(extra: Record<string, string>): Record<string, string> {
    return buildEnv({ base: process.env, home: this.employeeHome, passList: this.o.passEnv, fixed: { ...this.claudeFixed(), ...extra } });
  }

  private claudeFixed(): Record<string, string> {
    return { MCP_TOOL_TIMEOUT: "86400000", CLAUDE_AGENT_SDK_CLIENT_APP: `agent-teams-node/${DAEMON_VERSION}`, CLAUDE_CONFIG_DIR: this.o.claudeConfigDir };
  }

  private codexServerEnv(): Record<string, string> {
    return buildEnv({ base: process.env, home: this.employeeHome, passList: [...this.o.passEnv, ...CODEX_PASS_ENV], fixed: { CODEX_HOME: this.o.codexHome } });
  }

  private credEnv(spec: SessionOpen): Record<string, string> {
    if (!spec.git_credentials || spec.employee.tier === "coordinator") return {};
    const secret = this.credServer.register(spec.session_id, spec.open_gen);
    return { ...gitCredentialEnv(this.o.nodeCmd, this.credServer.socketPath, spec.session_id, secret), AT_GH_WRAPPER_DIR: this.platform.binDir };
  }

  private forgetSession(id: string): void {
    this.credServer.unregister(id);
    this.toolhost.remove(id);
  }
}
