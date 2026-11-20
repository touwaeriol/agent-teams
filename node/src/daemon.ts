import { cpus, hostname, loadavg, totalmem } from "node:os";
import { existsSync, mkdirSync, statfsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { ApprovalBroker } from "./approvals.js";
import { CredBroker } from "./cred/broker.js";
import { gitCredentialEnv } from "./cred/helper.js";
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
import type { ApprovalResolve, ClaudeRuntime, CodexRuntime, CredResponse, FileDownload, Frame, GitCleanup, GitPrepare, GitPurge, GitPush, GitResult, GitStatus, Hello, NodeConfig, NodeUpgrade, NodeUsageRefresh, SessionClose, SshCertResponse, SshServersUpdate, SessionCompact, SessionInterrupt, SessionOpen, SessionSend, SessionSetMode, SessionWatch, ToolResult, Welcome } from "./protocol.js";
import { probeClaude, probeCodex } from "./runtimes.js";
import { readClaudeUsage, UsageReporter, type UsageSources } from "./usage.js";
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
  /** Download gh at start instead of on first use (default: when probing runtimes). */
  prefetchGh?: boolean;
  /** Parent of the per-session SSH directories (default $XDG_RUNTIME_DIR/agent-teams-ssh or <data>/run-ssh). */
  sshBaseDir?: string;
  /** PATH searched for the real ssh / scp / sftp (default the daemon's PATH). */
  sshPath?: string;
  /** Temp directory for over-long session directory paths (tests; default os.tmpdir()). */
  sshTmpDir?: string;
}

const FEATURES = ["sessions", "codex_app_server", "git_worktree", "git_sync", "git_review_sha", "cred_helper", "upgrade", "file_download", "shared_files", "context_usage", "session_compact", "usage", "ssh_gateway"];

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
  private readonly usage: UsageReporter;
  private readonly ssh: SshHub;
  /** Real OpenSSH client found at start (null: remote servers cannot be used on this node). */
  private readonly sshClient: string | null;

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
    this.git = new GitJobs({ root: o.root, platform: this.platform, gitBaseUrl: o.gitBaseUrl, mirrorDir: join(o.stateDir, "mirrors"), log: o.log, credential: (job, repo, purpose) => this.jobCredential(job, repo, purpose), lookup: (id) => this.recordedResult(id), ...(o.reviewRetryMs !== undefined ? { reviewRetryMs: o.reviewRetryMs } : {}) });
    this.sessions = new SessionManager({
      root: o.root, stateDir: o.stateDir, send, approvals: this.approvals, tools: this.tools, log: o.log, maxActive: o.maxActive,
      waitForJob: (id, ms) => this.git.waitFor(id, ms),
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
    this.dispatcher = new Dispatcher(this.link, this.inbox, this.handlers(), o.log);
    this.link.on("frame", (f: Frame) => this.dispatcher.handle(f));
    this.link.on("welcome", (w: Welcome) => this.onWelcome(w));
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
    this.link.start();
  }

  async stop(): Promise<void> {
    if (this.statsTimer) clearInterval(this.statsTimer);
    this.usage.stop();
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
    this.usage.kick();
  }

  /** Where node.usage reads from: the login kinds probed at start, the Claude CLI and the shared codex app-server. */
  private usageSources(): UsageSources {
    return {
      claudeAuth: () => this.claudeRuntime.auth,
      codexAuth: () => this.codexRuntime.auth,
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
      "node.upgrade": { run: (b) => this.upgrade(b as unknown as NodeUpgrade) },
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
    const env = this.sessionEnv(spec);
    const ctx: DriverContext = { spec, absCwd, env, ...(env.AT_SSH_CONFIG ? { systemExtra: sshPrompt((spec.ssh_servers ?? []).map((s) => s.name)) } : {}), policy: this.policy(spec, absCwd, readable), sink, log: this.o.log.child({ session: spec.session_id }) };
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
    const ssh = this.sshEnabled(spec);
    const cred = this.credEnv(spec, ssh);
    if (!ssh) this.ssh.drop(spec.session_id);
    const sshEnv: Record<string, string> = ssh ? { AT_SSH_CONFIG: this.ssh.open(spec.session_id, spec.open_gen, spec.ssh_servers ?? []) } : {};
    const fixed = spec.runtime === "claude" ? this.claudeFixed() : {};
    const extra: Record<string, string> = { ...gitIdentity(spec.employee.name), ...(spec.env ?? {}) };
    // The session's own SSH plumbing is the platform's: spec.env cannot point ssh elsewhere.
    for (const k of ["AT_SSH_CONFIG", "SSH_AUTH_SOCK"]) delete extra[k];
    return buildEnv({
      base: process.env, home: this.employeeHome, extra,
      passList: spec.runtime === "codex" ? [...this.o.passEnv, ...CODEX_PASS_ENV] : this.o.passEnv,
      fixed: { ...fixed, ...cred, ...sshEnv }, pathPrefix: cred.AT_CRED_SOCK || sshEnv.AT_SSH_CONFIG ? [this.platform.binDir] : [],
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
    return { ...gitCredentialEnv(this.o.nodeCmd, this.credServer.socketPath, spec.session_id, secret), AT_GH_WRAPPER_DIR: this.platform.binDir };
  }

  private forgetSession(id: string): void {
    this.ssh.forget(id);
    this.credServer.unregister(id);
    this.toolhost.remove(id);
  }
}
