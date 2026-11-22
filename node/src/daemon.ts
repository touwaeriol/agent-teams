import { cpus, hostname, loadavg, totalmem } from "node:os";
import { mkdirSync, statfsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { ApprovalBroker } from "./approvals.js";
import { CredBroker } from "./cred/broker.js";
import { gitCredentialEnv } from "./cred/helper.js";
import { gitSshCommand } from "./cred/gitssh.js";
import { CredServer, credSocketPath } from "./cred/server.js";
import { GitJobs } from "./git/jobs.js";
import { GhInstaller, type GhInstallOptions } from "./git/gh-install.js";
import { installPlatformFiles, installSshWrappers, sshClientVersion, type PlatformFiles } from "./git/platform.js";
import { sshBaseDir, sshSocketPath } from "./ssh/config.js";
import { gatewayUrl, SshHub } from "./ssh/hub.js";
import { sshPrompt } from "./ssh/prompt.js";
import { probeIsolation, type Isolation } from "./isolation.js";
import { Link } from "./link/client.js";
import { Dispatcher, type CommandHandler } from "./link/dispatcher.js";
import { Inbox, type Reply } from "./link/inbox.js";
import { Outbox } from "./link/outbox.js";
import { Mover } from "./move/mover.js";
import { consoleTransfer, type MoveTransfer } from "./move/transfer.js";
import type { ApprovalResolve, ClaudeRuntime, CodexRuntime, CredResponse, FileDownload, Frame, GitCleanup, GitPrepare, GitPurge, GitPush, GitResult, GitStatus, Hello, NodeConfig, NodeUpgrade, NodeRuntimesRefresh, NodeUsageRefresh, SessionClose, SshCertResponse, SshServersUpdate, SessionCompact, SessionExport, SessionInterrupt, SessionOpen, SessionSend, SessionSetMode, SessionWatch, ToolResult, Welcome } from "./protocol.js";
import { readClaudeUsage, UsageReporter, type UsageSources } from "./usage.js";
import type { QueryFn } from "./sessions/claude/driver.js";
import { RuntimeRegistry } from "./sessions/registry.js";
import { RuntimeMonitor, type RuntimeStatuses } from "./runtime-monitor.js";
import { CodexHub } from "./sessions/codex/hub.js";
import { materializeSkills } from "./sessions/skills.js";
import { materializePlugins } from "./sessions/plugins.js";
import { buildEnv, CODEX_PASS_ENV, gitIdentity } from "./sessions/env.js";
import { AGY_PASS_ENV } from "./sessions/antigravity/detect.js";
import { SessionManager } from "./sessions/manager.js";
import type { ToolPolicy } from "./sessions/policy.js";
import type { Session } from "./sessions/session.js";
import type { Driver, DriverContext } from "./sessions/types.js";
import { LoopbackToolHost } from "./toolhost/http-server.js";
import { ToolBroker } from "./toolhost/broker.js";
import { Updater, type UpdaterOptions } from "./upgrade.js";
import type { Logger } from "./util/log.js";
import { absPath } from "./sessions/claude/options.js";
import { consoleImageFetcher } from "./sessions/images.js";
import { DAEMON_BUILD, DAEMON_RELEASE, DAEMON_VERSION, PROTOCOLS } from "./version.js";

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
  /** HOME of the agy processes (default <data>/agy-home): log in there once with `HOME=<agyHome> agy acp --login`. */
  agyHome?: string;
  installDir: string;
  /** How to invoke this daemon for helper subcommands (git-credential, gh). */
  nodeCmd: string[];
  log: Logger;
  probeRuntimes?: boolean;
  isolation?: Isolation;
  queryFn?: QueryFn;
  codexCommand?: string[];
  /** The agy command (tests: a fake ACP agent); default: found on PATH / ~/.local/bin. */
  agyCommand?: string[];
  link?: { pingMs?: number; deadMs?: number; backoff?: { minMs: number; maxMs: number } };
  crashDelaysMs?: number[];
  /** session.compact: how long to wait for an idle session, and for the compaction (tests). */
  compactWaitMs?: number;
  compactRunMs?: number;
  /** Review sync with expect_sha: the wait step between fetch retries (tests; default 2 s). */
  reviewRetryMs?: number;
  onExit?: (code: number) => void;
  /** Where the GitHub CLI is downloaded when the node has none (default <data>/tools). */
  toolsDir?: string;
  /** Overrides for the gh download (tests). */
  gh?: Partial<GhInstallOptions>;
  /** node.usage period (default 5 min; 0 disables) and per-runtime read budget (default 12 s). */
  usageIntervalMs?: number;
  usageTimeoutMs?: number;
  /** Replaces the Claude / Codex quota reads (tests). */
  usageSources?: Partial<UsageSources>;
  /** node.runtimes checks: period (default 60 s; 0 disables the timer), file-event debounce (default 2 s), probe budget (default 12 s). */
  runtimeMonitor?: { intervalMs?: number; debounceMs?: number; probeTimeoutMs?: number };
  /** Download gh at start instead of on first use (default: when probing runtimes). */
  prefetchGh?: boolean;
  /** Parent of the per-session SSH directories (default $XDG_RUNTIME_DIR/agent-teams-ssh or <data>/run-ssh). */
  sshBaseDir?: string;
  /** PATH searched for the real ssh / scp / sftp (default the daemon's PATH). */
  sshPath?: string;
  /** Temp directory for over-long session directory paths (tests; default os.tmpdir()). */
  sshTmpDir?: string;
  /** This process's release id (tests; default <version>+<build of node.mjs>). */
  release?: string;
  /** Advertise self_update (tests; default: started by the launcher, AGENT_TEAMS_LAUNCHER=1). */
  selfUpdate?: boolean;
  /** Where session-move archives are uploaded and downloaded (tests; default PUT / GET /nodes/moves/{id}). */
  moveTransfer?: MoveTransfer;
  /** Updater overrides (tests): poll and confirm delays, fetch, npm. */
  updater?: Partial<Pick<UpdaterOptions, "pollMs" | "confirmAfterMs" | "fetchFn" | "npm" | "now">>;
}

const FEATURES = ["sessions", "codex_app_server", "git_worktree", "git_sync", "git_review_sha", "cred_helper", "file_download", "shared_files", "context_usage", "session_compact", "usage", "ssh_gateway", "runtime_status", "session_move"];

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
  /** HOME of the agy processes (Antigravity login and settings); its .gemini and .config are secret. */
  private readonly agyHome: string;
  /** Per-employee skill plugins (sessions/skills.ts), outside every worktree. */
  private readonly skillsRoot: string;
  /** Per-employee plugin copies at exact versions (sessions/plugins.ts), outside every worktree. */
  private readonly pluginsRoot: string;
  private readonly isolation: Isolation;
  /** The agent runtimes this node runs (sessions/registry.ts). */
  readonly runtimes: RuntimeRegistry;
  private statsTimer: NodeJS.Timeout | null = null;
  private readonly usage: UsageReporter;
  /** Re-detects the runtimes' install/login state and reports changes in node.runtimes (protocol.md §9.2). */
  readonly runtimeMonitor: RuntimeMonitor;
  private readonly updater: Updater;
  /** Session moves between nodes (protocol.md §4.11). */
  private readonly mover: Mover;
  private readonly ssh: SshHub;
  /** Real OpenSSH client found at start (null: remote servers cannot be used on this node). */
  private readonly sshClient: string | null;

  constructor(private readonly o: DaemonOptions) {
    for (const d of [o.root, o.stateDir]) mkdirSync(d, { recursive: true, mode: 0o700 });
    this.employeeHome = join(o.root, "agent-home");
    mkdirSync(this.employeeHome, { recursive: true, mode: 0o700 });
    this.agyHome = resolve(o.agyHome ?? join(dirname(o.platformDir), "agy-home"));
    mkdirSync(this.agyHome, { recursive: true, mode: 0o700 });
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
    const realSsh = installSshWrappers(this.platform.binDir, o.sshPath ?? process.env.PATH ?? "");
    this.sshClient = sshClientVersion(realSsh.ssh);
    this.ssh = new SshHub({
      baseDir: o.sshBaseDir ?? sshBaseDir(dataDir), proxySocket: sshSocketPath(o.runtimeDir), nodeCmd: o.nodeCmd,
      gatewayUrl: gatewayUrl(o.url), token: o.token, send, log: o.log,
      verify: (id, secret) => this.credServer.verify(id, secret), ...(o.sshTmpDir ? { tmpDir: o.sshTmpDir } : {}),
    });
    this.credServer = new CredServer(credSocketPath(o.runtimeDir), this.creds, o.log, { gh: () => this.gh.resolve(), ghConfigRoot: join(dataDir, "gh-config") });
    this.toolhost = new LoopbackToolHost(o.log);
    this.hub = new CodexHub({ env: this.codexServerEnv(), log: o.log, clientVersion: DAEMON_VERSION, ...(o.codexCommand ? { command: o.codexCommand } : {}) });
    this.runtimes = RuntimeRegistry.builtin({
      root: o.root, employeeHome: this.employeeHome, claudeConfigDir: o.claudeConfigDir, codexHome: o.codexHome,
      sandboxOk: () => this.isolation !== "none", claudeEnv: () => this.claudeEnv({}), claudeFixed: () => this.claudeFixed(),
      secretPaths: () => this.secretPaths(), hub: this.hub, toolhost: this.toolhost, ...(o.queryFn ? { queryFn: o.queryFn } : {}),
      ...(o.codexCommand ? { codexCommand: o.codexCommand } : {}),
      agyHome: this.agyHome, ...(o.agyCommand ? { agyCommand: o.agyCommand } : {}), log: o.log,
      agyEnv: () => buildEnv({ base: process.env, home: this.agyHome, passList: [...this.o.passEnv, ...AGY_PASS_ENV] }),
      // A session saw a runtime's state change (agy logged out, binary gone): report it without waiting for the timer.
      recheck: () => { if (this.monitorOn) void this.runtimeMonitor.poll("periodic"); },
    });
    this.runtimeMonitor = new RuntimeMonitor({
      detectors: () => this.runtimes.all(), log: o.log, ...o.runtimeMonitor,
      send: (body) => this.link.send("node.runtimes", body),
      onChange: (prev, next) => this.runtimesChanged(prev, next),
    });
    this.git = new GitJobs({ root: o.root, platform: this.platform, gitBaseUrl: o.gitBaseUrl, mirrorDir: join(o.stateDir, "mirrors"), log: o.log, credential: (job, repo, purpose, host, path) => this.jobCredential(job, repo, purpose, host, path), gitSsh: gitSshCommand(o.nodeCmd), lookup: (id) => this.recordedResult(id), ...(o.reviewRetryMs !== undefined ? { reviewRetryMs: o.reviewRetryMs } : {}) });
    this.mover = new Mover({
      root: o.root, claudeConfigDir: o.claudeConfigDir, codexHome: o.codexHome, tmpDir: o.stateDir, hooksDir: this.platform.hooksDir,
      transfer: o.moveTransfer ?? consoleTransfer(o.url, o.token), log: o.log,
    });
    this.sessions = new SessionManager({
      root: o.root, stateDir: o.stateDir, send, approvals: this.approvals, tools: this.tools, log: o.log, maxActive: o.maxActive,
      waitForJob: (id, ms) => this.git.waitFor(id, ms),
      importMove: (spec, absCwd) => this.mover.import(spec, absCwd),
      createDriver: (spec, cwd, sink) => this.createDriver(spec, cwd, sink),
      onSessionGone: (id) => this.forgetSession(id),
      onDriverGone: (id) => this.ssh.drop(id),
      fetchImage: consoleImageFetcher(o.url, o.token),
      ...(o.crashDelaysMs ? { crashDelaysMs: o.crashDelaysMs } : {}),
      ...(o.compactWaitMs ? { compactWaitMs: o.compactWaitMs } : {}),
      ...(o.compactRunMs ? { compactRunMs: o.compactRunMs } : {}),
    });
    this.usage = new UsageReporter({
      sources: { ...this.usageSources(), ...o.usageSources }, log: o.log,
      send: (body) => this.link.send("node.usage", body),
      ...(o.usageIntervalMs !== undefined ? { intervalMs: o.usageIntervalMs } : {}),
      ...(o.usageTimeoutMs !== undefined ? { timeoutMs: o.usageTimeoutMs } : {}),
    });
    this.updater = new Updater({
      data: o.installDir, self: o.release ?? DAEMON_RELEASE, releaseKeyPath: join(o.configDir, "release.pub"), sessions: this.sessions, log: o.log,
      consoleHttpBase: () => o.url.replace(/^ws/, "http").replace(/\/nodes(\/v2)?\/connect$/, ""),
      send: (body) => this.link.send("node.upgrade_result", body), exit: (code) => o.onExit?.(code),
      ...(o.updater ?? {}),
    });
    this.dispatcher = new Dispatcher(this.link, this.inbox, this.handlers(), o.log);
    this.link.on("frame", (f: Frame) => this.dispatcher.handle(f));
    this.link.on("welcome", (w: Welcome) => this.onWelcome(w));
    this.link.on("closed", () => this.updater.onDisconnected());
    this.link.on("stopped", (why: string) => o.onExit?.(why === "replaced" ? 3 : 0));
  }

  async start(): Promise<void> {
    await this.credServer.start();
    await this.ssh.start();
    await this.toolhost.start();
    if (this.o.probeRuntimes !== false) await this.probe();
    if (this.o.prefetchGh ?? this.o.probeRuntimes !== false) this.prefetchGh();
    this.cancelOrphans();
    this.dispatcher.resumeUnfinished();
    this.sessions.startTimers();
    if (this.monitorOn) this.runtimeMonitor.start();
    this.link.start();
  }

  async stop(): Promise<void> {
    if (this.statsTimer) clearInterval(this.statsTimer);
    this.usage.stop();
    this.runtimeMonitor.stop();
    this.link.stop();
    await this.sessions.shutdown();
    this.hub.stop();
    await this.toolhost.stop();
    await this.ssh.stop();
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
    await this.runtimes.detectAll({ force: true, timeoutMs: 60_000 });
  }

  hello(): Hello {
    return {
      protocols: [...PROTOCOLS],
      daemon: { version: DAEMON_VERSION, ...(DAEMON_BUILD ? { build: DAEMON_BUILD } : {}), node: process.versions.node, os: process.platform, arch: process.arch },
      epoch: this.outbox.epoch,
      last_seq: this.outbox.lastSentSeq,
      acked_cmd_seq: this.inbox.acked,
      host: hostname(),
      root: resolve(this.o.root),
      capacity: { ...(this.o.maxActive ? { max_active_sessions: this.o.maxActive } : {}), cpus: cpus().length, mem_mb: Math.round(totalmem() / 1048576) },
      runtimes: this.runtimes.hello(),
      features: this.o.selfUpdate ?? process.env.AGENT_TEAMS_LAUNCHER === "1" ? [...FEATURES, "self_update"] : FEATURES,
      ...(this.sshClient ? { ssh_client: { version: this.sshClient } } : {}),
      isolated: false,
      isolation: this.isolation,
    };
  }

  /** Requests and tool calls whose Promise died with the previous process become cancels (protocol.md §5.3, §6.2). */
  /** A platform tool's result; one that ended the caller's task also replaces its anchor (protocol.md §4.10). */
  private toolResult(b: ToolResult): void {
    this.tools.resolve(b);
    if (b.anchor !== undefined) this.sessions.setTaskAnchor(b.session_id, b.anchor);
  }

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
    if (this.monitorOn) void this.runtimeMonitor.hello();
    this.usage.kick();
    this.updater.onConnected();
  }

  /** Periodic, file-event and hello checks (off in tests that skip runtime probes, unless asked for); refresh always works. */
  private get monitorOn(): boolean {
    return this.o.runtimeMonitor !== undefined || this.o.probeRuntimes !== false;
  }

  /** A login that appeared, changed or went away: the subscription quota on the node page follows at once. */
  private runtimesChanged(prev: RuntimeStatuses, next: RuntimeStatuses): void {
    const auth = (r: RuntimeStatuses) => JSON.stringify(Object.keys(r).sort().map((id) => [id, r[id]!.auth, r[id]!.expired ?? false]));
    if (Object.keys(prev).length > 0 && auth(prev) !== auth(next)) void this.usage.report();
  }

  /** Where node.usage reads from: the login kinds last detected (RuntimeMonitor), the Claude CLI and the shared codex app-server. */
  private usageSources(): UsageSources {
    return {
      claudeAuth: () => (this.runtimes.get("claude").info() as ClaudeRuntime).auth,
      codexAuth: () => (this.runtimes.get("codex").info() as CodexRuntime).auth,
      claudeUsage: (signal) => readClaudeUsage(this.claudeEnv({}), this.employeeHome, signal, this.o.queryFn),
      codexAccount: () => this.hub.request("account/read", { refreshToken: false }),
      codexRateLimits: () => this.hub.request("account/rateLimits/read", { excludeResetCreditDetails: true }),
      lastCodexEvent: () => this.hub.lastRateLimits,
    };
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
      "session.compact": { lane: session, run: async (b) => s.compact(b as unknown as SessionCompact) },
      "session.close": { lane: session, run: (b) => s.close(b as unknown as SessionClose) },
      "session.export": { lane: session, run: async (b) => [{ type: "session.exported", body: await this.mover.export(b as unknown as SessionExport) }] },
      "session.watch": { run: async (b) => s.watch(b as unknown as SessionWatch) },
      "approval.resolve": { run: async (b) => [{ type: "approval.ack", body: this.approvals.resolve(b as unknown as ApprovalResolve) }] },
      "tool.result": { run: async (b) => (this.toolResult(b as unknown as ToolResult), []) },
      "file.download": { run: async (b) => (this.tools.chunk(b as unknown as FileDownload), []) },
      "cred.response": { run: async (b) => (this.creds.resolve(b as unknown as CredResponse), []) },
      "ssh.cert_response": { run: async (b) => (this.ssh.resolve(b as unknown as SshCertResponse), []) },
      "ssh.servers_update": { lane: session, run: async (b) => (this.sshServersUpdate(b as unknown as SshServersUpdate), []) },
      "git.prepare": { run: (b) => this.gitReply(this.git.prepare(b as unknown as GitPrepare)) },
      "git.status": { run: (b) => this.gitReply(this.git.status(b as unknown as GitStatus)) },
      "git.push": { run: (b) => this.gitReply(this.git.push(b as unknown as GitPush)) },
      "git.cleanup": { run: (b) => this.gitReply(this.git.cleanup(b as unknown as GitCleanup)) },
      "git.purge": { run: (b) => this.gitReply(this.git.purge(b as unknown as GitPurge, (abs) => this.sessions.busyUnder(abs))) },
      "node.config": { run: async (b) => this.nodeConfig(b as NodeConfig) },
      "node.drain": { run: async () => (this.sessions.drain(), []) },
      "node.upgrade": { run: async (b) => (this.updater.handle(b as unknown as NodeUpgrade), []) },
      "node.runtimes_refresh": { run: async (b, f) => [{ type: "node.runtimes", body: await this.runtimeMonitor.refresh((b as NodeRuntimesRefresh).runtimes, f.id) }] },
      "node.usage_refresh": { run: async (b) => [{ type: "node.usage", body: await this.usage.refresh((b as NodeUsageRefresh).runtimes) }] },
    };
  }

  private async gitReply(p: Promise<GitResult>): Promise<Reply[]> {
    return [{ type: "git.result", body: await p }];
  }

  /** ssh.servers_update: the spec keeps the list for later drivers; a live driver's ssh_config is rewritten now. */
  private sshServersUpdate(u: SshServersUpdate): void {
    if (!this.ssh.updateServers(u.session_id, u.open_gen, u.version, u.servers)) return;
    this.sessions.setSshServers(u.session_id, u.open_gen, u.servers);
  }

  private nodeConfig(c: NodeConfig): Reply[] {
    this.sessions.configure(c);
    if (c.log_level) this.o.log.level = c.log_level;
    return [];
  }

  private recordedResult(jobId: string): GitResult | undefined {
    const replies = this.inbox.repliesFor(`git.prepare:${jobId}`);
    return replies?.find((r) => r.type === "git.result")?.body as GitResult | undefined;
  }

  private async jobCredential(jobId: string, _repo: string, purpose: "read" | "push", host: string, path: string) {
    const cred = await this.creds.get({ job_id: jobId }, host, path, purpose);
    return { username: cred.username, password: cred.password, ...(cred.sshKey ? { sshKey: cred.sshKey } : {}) };
  }

  // ---- session runtime construction ----

  private createDriver(spec: SessionOpen, absCwd: string, sink: Session): { driver: Driver; ctx: DriverContext } {
    const runtime = this.runtimes.get(spec.runtime);
    const skills = runtime.nativeSkills ? materializeSkills(this.skillsRoot, spec) : null;
    const plugins = materializePlugins(this.pluginsRoot, spec);
    const readable = [...(skills ? [skills.pluginDir] : []), ...plugins.map((p) => p.dir)];
    const env = this.sessionEnv(spec);
    const ctx: DriverContext = { spec, absCwd, env, ...(env.AT_SSH_CONFIG ? { systemExtra: sshPrompt((spec.ssh_servers ?? []).map((s) => s.name)) } : {}), policy: this.policy(spec, absCwd, readable), sink, log: this.o.log.child({ session: spec.session_id }) };
    return { driver: runtime.createDriver(ctx, { skills, plugins }), ctx };
  }

  private policy(spec: SessionOpen, absCwd: string, readable: string[]): ToolPolicy {
    const c = spec.claude;
    const a = spec.antigravity;
    const extra = (c?.sandbox?.extra_write_paths ?? []).map((p) => absPath(p, this.o.root, absCwd)).filter(Boolean);
    return {
      denyNonPlatform: c?.deny_non_platform_tools === true || spec.employee.tier === "coordinator",
      readOnly: c?.read_only === true || a?.read_only === true,
      bash: c?.bash_policy ?? a?.bash_policy ?? null,
      readRoots: [absCwd, ...extra, ...readable],
      secretPaths: this.secretPaths(),
    };
  }

  secretPaths(): string[] {
    return [this.o.configDir, this.o.stateDir, this.o.runtimeDir, join(this.o.claudeConfigDir, ".credentials.json"), this.o.codexHome,
      join(this.agyHome, ".gemini"), join(this.agyHome, ".config")].map((p) => resolve(p));
  }

  private sessionEnv(spec: SessionOpen): Record<string, string> {
    const ssh = this.sshEnabled(spec);
    const cred = this.credEnv(spec, ssh);
    if (!ssh) this.ssh.drop(spec.session_id);
    const sshEnv: Record<string, string> = ssh ? { AT_SSH_CONFIG: this.ssh.open(spec.session_id, spec.open_gen, spec.ssh_servers ?? []) } : {};
    const runtime = this.runtimes.get(spec.runtime);
    const extra: Record<string, string> = { ...gitIdentity(spec.employee.name), ...(spec.env ?? {}) };
    // The session's own SSH plumbing is the platform's: spec.env cannot point ssh elsewhere.
    for (const k of ["AT_SSH_CONFIG", "SSH_AUTH_SOCK"]) delete extra[k];
    return buildEnv({
      base: process.env, home: this.employeeHome, extra,
      passList: [...this.o.passEnv, ...runtime.passEnv],
      fixed: { ...runtime.fixedEnv(), ...cred, ...sshEnv }, pathPrefix: cred.AT_CRED_SOCK || sshEnv.AT_SSH_CONFIG ? [this.platform.binDir] : [],
    });
  }

  /**
   * Remote servers for this session (remote-servers.md §7.4): the console granted some, the
   * employee is not the coordinator, the node has an OpenSSH client, and the session does not
   * run in the Claude Bash sandbox (its network isolation and denyRead keep ssh from reaching
   * the daemon's sockets; not offered there, §8.3).
   */
  private sshEnabled(spec: SessionOpen): boolean {
    if (!spec.ssh_servers?.length || spec.employee.tier === "coordinator") return false;
    if (!this.sshClient) {
      this.o.log.warn({ session: spec.session_id }, "remote servers granted but no OpenSSH client on this node");
      return false;
    }
    if (spec.runtime === "claude" && spec.claude?.sandbox?.enabled) {
      this.o.log.warn({ session: spec.session_id }, "remote servers are not offered to sandboxed Claude sessions");
      return false;
    }
    return true;
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

  /** The per-session secret (new on every driver) for git/gh and, with remote servers, ssh-proxy. */
  private credEnv(spec: SessionOpen, ssh: boolean): Record<string, string> {
    const git = spec.git_credentials === true && spec.employee.tier !== "coordinator";
    if (!git && !ssh) return {};
    const secret = this.credServer.register(spec.session_id, spec.open_gen);
    if (!git) return { AT_CRED_SESSION: spec.session_id, AT_CRED_SECRET: secret };
    return { ...gitCredentialEnv(this.o.nodeCmd, this.credServer.socketPath, spec.session_id, secret, spec.git_hosts ?? []), AT_GH_WRAPPER_DIR: this.platform.binDir };
  }

  private forgetSession(id: string): void {
    this.ssh.forget(id);
    this.credServer.unregister(id);
    this.toolhost.remove(id);
  }
}
