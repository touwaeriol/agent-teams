// One Antigravity employee = one `agy acp --process-per-session` child speaking ACP (thread #41,
// docs: antigravity.google/docs/cli/acp). Flow: initialize → session/new (or session/resume /
// session/load with the stored sessionId) with the platform MCP server on the loopback tool host
// → one session/prompt per turn; session/update streams the turn; session/request_permission is
// answered locally or through an approval card; session/cancel interrupts.
//
// Not verified against a logged-in agy (marked "unverified" below): the system prompt _meta shape,
// session/set_model for the model, how platform MCP tools are titled, /compact as a prompt.
import type { AntigravitySessionOptions, ApprovalResolve, SessionInit } from "../../protocol.js";
import type { LoopbackToolHost } from "../../toolhost/http-server.js";
import { approvalId, newId } from "../../util/ids.js";
import { DAEMON_VERSION } from "../../version.js";
import { textEvent } from "../claude/mapper.js";
import type { LoadedImage } from "../images.js";
import { ResumeFailed, type CompactResult, type Driver, type DriverContext, type EventFields, type OutgoingMessage, type StartResult } from "../types.js";
import { AcpClient, AcpFailure, ACP_AUTH_REQUIRED } from "./acp.js";
import { isPlatformTool, planEvent, promptError, READ_ONLY_KINDS, toolEnded, toolStarted, turnEnded, WRITE_KINDS, type AcpToolCall } from "./mapper.js";
import { ALLOW_ALWAYS, ALLOW_ONCE, decidePermission, outcome, REJECT, type PermissionOption } from "./permissions.js";

/** An MCP server in ACP session/new form (stdio servers have no type field). */
export type AcpMcpServer =
  | { name: string; command: string; args: string[]; env: Array<{ name: string; value: string }> }
  | { type: "http" | "sse"; name: string; url: string; headers: Array<{ name: string; value: string }> };

export interface AntigravityDriverDeps {
  toolhost: LoopbackToolHost;
  /** The agy executable (and leading args), null when agy is not installed on this node. */
  command: () => string[] | null;
  /** Plugin MCP servers (sessions/plugins.ts acpMcpServers). */
  pluginServers?: AcpMcpServer[];
  /** Request timeout of the setup calls (initialize, session/new…); default 60 s. */
  setupTimeoutMs?: number;
}

export const AGY_NOT_INSTALLED = "Antigravity CLI (agy) is not installed on this node: install it with the official script (https://antigravity.google/cli/install.sh) and log in with `agy acp --login`";
export const AGY_NOT_LOGGED_IN = "agy is not logged in on this node: run `agy acp --login` as the node user";

const PDF = "application/pdf";

type Pending = { approvalId: string; resolve: (v: unknown) => void };
type SessionResp = { sessionId?: string; models?: { currentModelId?: string } | null };
type Call = AcpToolCall & { startedAt: number; ended?: boolean };

export class AntigravityDriver implements Driver {
  private client: AcpClient | null = null;
  private sessionId: string | null;
  private turnId: string | null = null;
  private turnStartedAt: number | undefined;
  /** Assistant text since the last tool call (a "text" event when a tool starts or the turn ends). */
  private segment = "";
  private thinking = false;
  private readonly queue: OutgoingMessage[] = [];
  private readonly tools = new Map<string, Call>();
  private readonly pending = new Set<Pending>();
  /** execute calls of a read-only session that bash_policy allowed (the guard lets them run). */
  private readonly allowedCommands = new Set<string>();
  /** Set while session/load replays the history: those updates are not this turn's. */
  private replaying = false;
  /** A /compact prompt in progress: its updates are not shown. */
  private compactDone: ((r: CompactResult) => void) | null = null;
  private violated = false;
  private closed = false;
  private model: string | undefined;
  private approval: "ask" | "auto";
  private readonly opts: AntigravitySessionOptions;

  constructor(private readonly ctx: DriverContext, private readonly deps: AntigravityDriverDeps) {
    this.opts = ctx.spec.antigravity ?? { model: "", read_only: true, approval: "ask" };
    this.approval = this.opts.approval;
    this.sessionId = ctx.spec.resume?.sdk_session_id ?? null;
    this.model = this.opts.model || undefined;
  }

  get busy(): boolean {
    return this.turnId !== null;
  }

  private get readOnly(): boolean {
    return this.opts.read_only || this.ctx.policy.readOnly;
  }

  private get platformNames(): string[] {
    return this.ctx.spec.platform_tools.map((t) => t.name);
  }

  private get compacting(): boolean {
    return this.compactDone !== null;
  }

  async start(): Promise<StartResult> {
    const command = this.deps.command();
    if (!command) throw new Error(AGY_NOT_INSTALLED);
    this.closed = false;
    const client = new AcpClient({ command, args: ["acp", "--process-per-session"], env: this.ctx.env, cwd: this.ctx.absCwd, log: this.ctx.log });
    client.on("notification", (method, p) => this.onNotification(method, p));
    client.on("exit", (err) => this.onExit(client, err));
    client.onRequest((method, p) => this.onRequest(method, p));
    this.client = client;
    try {
      await client.start();
    } catch (err) {
      this.client = null;
      throw new Error(`cannot start agy: ${(err as Error).message}`);
    }
    try {
      const t = this.deps.setupTimeoutMs ?? 60_000;
      const init = await client.request<{ agentCapabilities?: Caps } | null>("initialize", {
        protocolVersion: 1,
        clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false },
        clientInfo: { name: "agent-teams-node", version: DAEMON_VERSION },
      }, t);
      const url = this.deps.toolhost.rotate(this.ctx.spec.session_id, this.ctx.spec.platform_tools, (tool, args) => this.ctx.sink.callTool(tool, args));
      const params = this.sessionParams(url);
      const resp = this.sessionId ? await this.resume(client, init?.agentCapabilities ?? {}, params, t) : await this.newSession(client, params, t);
      if (!resp.sessionId) throw new Error("agy did not return a session id");
      this.sessionId = resp.sessionId;
      await this.applyModel(resp);
      return { sdk_session_id: this.sessionId, init: this.init() };
    } catch (err) {
      this.client = null;
      client.stop();
      throw err;
    }
  }

  /** session/new: cwd, MCP servers (platform over HTTP, plugin servers) and the system prompt in _meta. */
  private sessionParams(url: string): Record<string, unknown> {
    const mcpServers: AcpMcpServer[] = [{ type: "http", name: "platform", url, headers: [] }, ...(this.deps.pluginServers ?? [])];
    const append = this.systemPrompt();
    // Unverified: agy 1.5.14 advertises agentCapabilities._meta.antigravity.systemPrompt.append = true;
    // the request side is assumed to mirror it ({_meta: {antigravity: {systemPrompt: {append}}}}).
    return { cwd: this.ctx.absCwd, mcpServers, ...(append ? { _meta: { antigravity: { systemPrompt: { append } } } } : {}) };
  }

  private systemPrompt(): string {
    return [this.opts.system_append, this.ctx.systemExtra].filter(Boolean).join("\n\n");
  }

  private async newSession(client: AcpClient, params: Record<string, unknown>, t: number): Promise<SessionResp> {
    try {
      return (await client.request<SessionResp | null>("session/new", params, t)) ?? {};
    } catch (err) {
      if (err instanceof AcpFailure && err.error.code === ACP_AUTH_REQUIRED) {
        this.ctx.sink.event({ kind: "error", code: "auth_failed", message: AGY_NOT_LOGGED_IN, fatal: false });
        throw new Error(AGY_NOT_LOGGED_IN);
      }
      throw err;
    }
  }

  /**
   * Reopens the stored session with fresh MCP servers (the tool host URL rotates on every start):
   * session/resume when agy offers it (no history replay), else session/load (its replayed
   * updates are dropped). A refusal means the conversation is gone: ResumeFailed.
   */
  private async resume(client: AcpClient, caps: Caps, params: Record<string, unknown>, t: number): Promise<SessionResp> {
    const method = caps.sessionCapabilities?.resume ? "session/resume" : caps.loadSession ? "session/load" : null;
    if (!method) throw new ResumeFailed("agy cannot resume sessions");
    this.replaying = method === "session/load";
    try {
      const r = await client.request<SessionResp | null>(method, { ...params, sessionId: this.sessionId }, t);
      return { ...(r ?? {}), sessionId: this.sessionId! };
    } catch (err) {
      if (err instanceof AcpFailure && err.error.code === ACP_AUTH_REQUIRED) throw new Error(AGY_NOT_LOGGED_IN);
      throw new ResumeFailed((err as Error).message);
    } finally {
      this.replaying = false;
    }
  }

  /**
   * The role's model. Unverified: ACP's (unstable) session/set_model; agy's session options offer a
   * Model selector. A refusal is reported and the session goes on with agy's default.
   */
  private async applyModel(resp: SessionResp): Promise<void> {
    const current = resp.models?.currentModelId;
    if (!this.opts.model || this.opts.model === current) {
      this.model = this.opts.model || current || undefined;
      return;
    }
    try {
      await this.client!.request("session/set_model", { sessionId: this.sessionId, modelId: this.opts.model }, 15_000);
      this.model = this.opts.model;
    } catch (err) {
      this.model = current;
      this.ctx.log.warn({ err: (err as Error).message, model: this.opts.model }, "agy did not take the model");
      this.ctx.sink.event({ kind: "error", code: "protocol", message: `agy did not switch to model ${this.opts.model}: ${(err as Error).message}`.slice(0, 500), fatal: false });
    }
  }

  private init(): SessionInit {
    return {
      ...(this.model ? { model: this.model } : {}),
      tools: this.ctx.spec.platform_tools.map((t) => `mcp__platform__${t.name}`),
      permission_mode: `acp/${this.approval}${this.readOnly ? "/read_only" : ""}`,
      mcp_servers: [{ name: "platform", status: "connected" }],
    };
  }

  // ---- turns ----

  async push(msg: OutgoingMessage): Promise<{ turn_id?: string }> {
    if (!this.client || !this.sessionId) throw new Error("agy session is not running");
    if (this.turnId) {
      // One prompt at a time (ACP): a message for a running turn waits for its end.
      if (msg.priority === "now") this.queue.unshift(msg);
      else this.queue.push(msg);
      return this.compacting ? {} : { turn_id: this.turnId };
    }
    return { turn_id: this.startTurn(msg, false) };
  }

  private startTurn(msg: OutgoingMessage, quiet: boolean): string {
    const turnId = newId("t_");
    this.turnId = turnId;
    this.turnStartedAt = Date.now();
    this.segment = "";
    this.thinking = false;
    this.violated = false;
    if (!quiet) {
      this.ctx.sink.turn(true);
      this.emit({ kind: "turn_started", trigger_msg_ids: [msg.msg_id], tools: this.ctx.spec.platform_tools.map((t) => `mcp__platform__${t.name}`) });
    }
    const client = this.client!;
    client.request<{ stopReason?: string } | null>("session/prompt", { sessionId: this.sessionId, prompt: promptBlocks(msg.text, msg.images) }).then(
      (r) => this.onTurnDone(client, turnId, String(r?.stopReason ?? "end_turn")),
      (err: Error) => this.onTurnFailed(client, turnId, err),
    );
    return turnId;
  }

  private onTurnDone(client: AcpClient, turnId: string, stopReason: string): void {
    if (client !== this.client || this.turnId !== turnId || this.closed) return;
    if (this.compacting) return this.endCompact(stopReason === "end_turn" ? { ok: true } : { ok: false, error: `compaction ended with ${stopReason}` });
    if (this.segment) this.emit(textEvent(this.segment, true));
    this.emit(turnEnded(stopReason, this.turnStartedAt));
    this.endTurn();
  }

  private onTurnFailed(client: AcpClient, turnId: string, err: Error): void {
    if (client !== this.client || this.turnId !== turnId || this.closed) return;
    if (this.compacting) return this.endCompact({ ok: false, error: err.message });
    this.emit(promptError(err instanceof AcpFailure ? err.error.code : undefined, err.message));
    this.emit({ ...turnEnded("error", this.turnStartedAt), is_error: true });
    this.endTurn();
  }

  private endTurn(): void {
    this.turnId = null;
    this.segment = "";
    this.tools.clear();
    this.allowedCommands.clear();
    this.ctx.sink.turn(false);
    this.next();
  }

  private next(): void {
    const next = this.queue.shift();
    if (next) void this.push(next).catch((err: Error) => this.ctx.log.warn({ err: err.message }, "queued agy message failed"));
  }

  async interrupt(): Promise<string[]> {
    if (this.turnId && this.client && this.sessionId) {
      this.client.notify("session/cancel", { sessionId: this.sessionId });
      this.cancelPending("interrupted");
    }
    return this.queue.map((m) => m.msg_id);
  }

  /** set_mode: bypassPermissions → permission requests allowed here, default → asked; a model goes through session/set_model. */
  async setMode(mode?: string, model?: string | null): Promise<void> {
    if (mode === "bypassPermissions" && !this.readOnly) this.approval = "auto";
    if (mode === "default" || mode === "acceptEdits") this.approval = "ask";
    if (model && this.client && this.sessionId) {
      await this.client.request("session/set_model", { sessionId: this.sessionId, modelId: model }, 15_000);
      this.model = model;
    }
  }

  /** session.compact: "/compact <instructions>" sent as a prompt (unverified: agy's /compact over ACP). */
  async compact(instructions: string): Promise<CompactResult> {
    if (!this.client || !this.sessionId) return { ok: false, error: "no agy session" };
    if (this.turnId) return { ok: false, error: "busy: a turn is running" };
    return new Promise<CompactResult>((resolve) => {
      this.compactDone = resolve;
      this.startTurn({ msg_id: newId("m_compact_"), text: `/compact${instructions ? ` ${instructions}` : ""}`, priority: "next" }, true);
    });
  }

  private endCompact(r: CompactResult): void {
    const done = this.compactDone;
    this.compactDone = null;
    this.turnId = null;
    this.segment = "";
    done?.(r);
    if (!this.closed) this.next();
  }

  async close(): Promise<void> {
    this.closed = true;
    if (this.compacting) this.endCompact({ ok: false, error: "session closed" });
    this.cancelPending("session_closed");
    const client = this.client;
    this.client = null;
    if (client && this.sessionId) await client.request("session/close", { sessionId: this.sessionId }, 3000).catch(() => undefined);
    client?.stop();
    this.deps.toolhost.remove(this.ctx.spec.session_id);
    if (this.turnId) this.ctx.sink.turn(false);
    this.turnId = null;
  }

  private onExit(client: AcpClient, err: Error): void {
    if (client !== this.client || this.closed) return;
    this.client = null;
    this.cancelPending("process_restarted");
    if (this.compacting) this.endCompact({ ok: false, error: `exited: ${err.message}` });
    this.turnId = null;
    this.ctx.sink.exited(err);
  }

  // ---- session/update ----

  private onNotification(method: string, p: Record<string, unknown>): void {
    if (method !== "session/update" || this.closed || this.replaying || this.compacting) return;
    if (this.sessionId && p.sessionId !== undefined && p.sessionId !== this.sessionId) return;
    const u = (p.update ?? {}) as Record<string, unknown>;
    switch (u.sessionUpdate) {
      case "agent_message_chunk":
        return this.onText(u.content as { type?: string; text?: string } | undefined);
      case "agent_thought_chunk":
        if (!this.thinking) this.emit({ kind: "thinking" });
        this.thinking = true;
        return;
      case "tool_call":
        return this.onToolCall(u as unknown as AcpToolCall);
      case "tool_call_update":
        return this.onToolUpdate(u as unknown as AcpToolCall);
      case "plan": {
        const ev = planEvent(u.entries);
        if (ev) this.emit(ev);
        return;
      }
      default:
        this.ctx.log.debug({ update: u.sessionUpdate }, "agy update ignored");
    }
  }

  private onText(content: { type?: string; text?: string } | undefined): void {
    if (content?.type !== "text" || !content.text) return;
    this.thinking = false;
    this.segment += content.text;
    this.ctx.sink.delta(content.text, this.turnId ?? undefined);
  }

  private onToolCall(t: AcpToolCall): void {
    if (!t.toolCallId) return;
    if (this.segment) this.emit(textEvent(this.segment, false));
    this.segment = "";
    this.thinking = false;
    const call: Call = { ...stripNull(t), toolCallId: t.toolCallId, startedAt: Date.now() };
    this.tools.set(t.toolCallId, call);
    this.emit(toolStarted(call, this.platformNames));
    this.guard(call);
    if (call.status === "completed" || call.status === "failed") this.finishTool(call);
  }

  private onToolUpdate(t: AcpToolCall): void {
    const call = this.tools.get(t.toolCallId);
    if (!call) return;
    Object.assign(call, stripNull(t));
    this.guard(call);
    if (call.status === "completed" || call.status === "failed") this.finishTool(call);
  }

  private finishTool(call: Call): void {
    if (call.ended) return;
    call.ended = true;
    this.emit(toolEnded(call, call.startedAt));
  }

  /**
   * Backstop for read-only sessions: their writing requests are refused, so a writing tool that
   * runs anyway (agy did not ask) cancels the turn with an error instead of going on.
   */
  private guard(call: AcpToolCall): void {
    if (!this.readOnly || this.violated || isPlatformTool(call, this.platformNames)) return;
    const kind = call.kind ?? "other";
    const running = call.status === "in_progress" || call.status === "completed";
    // Only the kinds that surely write or run something: "other" is also how MCP tools (the
    // platform's included) may show up, and their permission requests are refused anyway.
    if (!running || READ_ONLY_KINDS.has(kind) || !WRITE_KINDS.has(kind)) return;
    if (kind === "execute" && this.allowedCommands.has(call.toolCallId)) return;
    this.violated = true;
    this.ctx.log.warn({ session: this.ctx.spec.session_id, kind, title: call.title }, "read-only agy session ran a writing tool; turn cancelled");
    this.emit({ kind: "error", code: "protocol", message: `read-only session: agy ran a ${kind} tool (${call.title ?? call.toolCallId}) without permission; the turn was cancelled`.slice(0, 500), fatal: false });
    void this.interrupt();
  }

  // ---- session/request_permission ----

  private async onRequest(method: string, p: Record<string, unknown>): Promise<unknown> {
    if (method !== "session/request_permission") throw new Error(`unsupported request ${method}`);
    const options = (Array.isArray(p.options) ? p.options : []) as PermissionOption[];
    const tc = (p.toolCall ?? {}) as AcpToolCall;
    const known = tc.toolCallId ? this.tools.get(tc.toolCallId) : undefined;
    const call: AcpToolCall = { ...(known ?? {}), ...stripNull(tc), toolCallId: tc.toolCallId ?? known?.toolCallId ?? newId("tc_") };
    const d = decidePermission(this.ctx.policy, this.approval, call, this.ctx.absCwd, this.platformNames);
    if (d.action === "deny") {
      this.ctx.log.info({ session: this.ctx.spec.session_id, kind: call.kind, reason: d.reason }, "agy permission refused");
      return outcome(options, REJECT);
    }
    if (d.action === "allow") {
      if (call.kind === "execute") this.allowedCommands.add(call.toolCallId);
      return outcome(options, ALLOW_ONCE);
    }
    return this.ask(call, options);
  }

  private ask(call: AcpToolCall, options: PermissionOption[]): Promise<unknown> {
    const id = approvalId(this.ctx.spec.session_id, call.toolCallId);
    return new Promise((resolve) => {
      const entry: Pending = { approvalId: id, resolve };
      this.pending.add(entry);
      const input = call.rawInput && typeof call.rawInput === "object" && !Array.isArray(call.rawInput) ? (call.rawInput as Record<string, unknown>) : { title: call.title ?? null };
      this.ctx.sink.requestApproval({
        approval_id: id, source: "antigravity_permission", tool_name: toolNameOf(call), input,
        available: options.some((o) => o.kind === "allow_always") ? ["allow", "allow_always", "deny"] : ["allow", "deny"],
        tool_use_id: call.toolCallId, ...(call.title ? { description: call.title } : {}),
      }).then(
        (res: ApprovalResolve) => this.settle(entry, outcome(options, res.decision === "allow_always" ? ALLOW_ALWAYS : res.decision === "allow" ? ALLOW_ONCE : REJECT)),
        () => this.settle(entry, outcome(options, REJECT)),
      );
    });
  }

  private settle(entry: Pending, value: unknown): void {
    if (!this.pending.delete(entry)) return;
    entry.resolve(value);
  }

  /** Interrupt / close / exit: open permission requests are answered "cancelled" and their cards withdrawn. */
  private cancelPending(reason: "interrupted" | "session_closed" | "process_restarted"): void {
    for (const e of [...this.pending]) {
      this.ctx.sink.cancelApproval(e.approvalId, reason);
      this.settle(e, { outcome: { outcome: "cancelled" } });
    }
  }

  private emit(ev: EventFields): void {
    this.ctx.sink.event(this.turnId ? { turn_id: this.turnId, ...ev } : ev);
  }
}

type Caps = { loadSession?: boolean; sessionCapabilities?: { resume?: unknown } };

function stripNull(t: AcpToolCall): AcpToolCall {
  return Object.fromEntries(Object.entries(t).filter(([, v]) => v !== null && v !== undefined)) as unknown as AcpToolCall;
}

function toolNameOf(t: AcpToolCall): string {
  const names: Record<string, string> = { edit: "Edit", delete: "Delete", move: "Move", execute: "Bash", read: "Read", search: "Search", fetch: "WebFetch" };
  return names[t.kind ?? ""] ?? (t.title ? t.title.slice(0, 100) : (t.kind ?? "other"));
}

/** ACP prompt content: the text, then images as base64 image blocks; PDFs are noted by path. */
export function promptBlocks(text: string, images: LoadedImage[] | undefined): Array<Record<string, unknown>> {
  const pdfs = (images ?? []).filter((i) => i.media_type === PDF);
  const note = pdfs.length ? `\n\n[附件 PDF 已保存在本机，可以直接读取：${pdfs.map((p) => p.path).join("、")}]` : "";
  const imgs = (images ?? []).filter((i) => i.media_type !== PDF).map((i) => ({ type: "image", mimeType: i.media_type, data: i.data.toString("base64") }));
  return [{ type: "text", text: text + note }, ...imgs];
}
