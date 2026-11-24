import type { ApprovalRequest, ApprovalResolve, CodexSessionOptions, ContextUsage, SessionOpen } from "../../protocol.js";
import type { LoopbackToolHost } from "../../toolhost/http-server.js";
import { deferred, type Deferred } from "../../util/async.js";
import { approvalId, newId } from "../../util/ids.js";
import { ResumeFailed, type CompactResult, type Driver, type DriverContext, type EventFields, type ModeApplied, type ModeChange, type ModelOption, type OutgoingMessage, type StartResult } from "../types.js";
import type { AppServer } from "./appserver.js";
import type { CodexHub, ThreadListener } from "./hub.js";
import { errorNotification, itemCompleted, itemStarted, turnCompleted } from "./mapper.js";
import { codexFileNote, codexImages } from "../images.js";
import { askForApproval, codexMode, modeTurnParams } from "./mode.js";

export interface CodexDriverDeps {
  hub: CodexHub;
  toolhost: LoopbackToolHost;
  /** Plugin MCP servers in Codex config form (sessions/plugins.ts codexMcpServers). */
  pluginServers?: Record<string, Record<string, unknown>>;
  /** The rollout file of a thread on this node (a thread moved here from another node, move/transcripts.ts). */
  findRollout?: (threadId: string) => string | null;
}

type Approval = Pick<ApprovalRequest, "source" | "tool_name" | "input"> & Partial<ApprovalRequest>;
const DECISION = { allow: "accept", allow_always: "acceptForSession", deny: "decline" } as const;
const MARKER = (msgId: string) => `<!-- ${msgId} -->\n`;

/**
 * One Codex employee = one Codex thread on the node's app-server (protocol.md §4):
 * turn/start when idle, turn/steer while a turn runs, `later` queued to turn/completed.
 */
export class CodexDriver implements Driver, ThreadListener {
  private threadId: string | null;
  private activeTurn: string | null = null;
  private turnStartedAt: number | undefined;
  private lastText: string | undefined;
  private readonly later: OutgoingMessage[] = [];
  private triggers: string[] = [];
  private readonly itemStarts = new Map<string, number>();
  private readonly diffs = new Map<string, string>();
  private readonly approvalsByRequest = new Map<string, string>();
  /** set_mode changes for the next turn/start (approvalPolicy, sandboxPolicy, approvalsReviewer, model, effort; they stick for later turns). */
  private turnOverride: Record<string, unknown> = {};
  private closed = false;
  private authReported = false;
  private readonly codex: CodexSessionOptions;
  /** The model Codex resolved for the thread (its own default when codex.model is empty). */
  private resolvedModel: string | undefined;
  /** Context use from the latest thread/tokenUsage/updated. */
  private usage: ContextUsage | undefined;
  /**
   * A session.compact in progress. turn: the turn thread/compact/start ran (the first turn that
   * starts without a message of ours); seen: a compaction was reported. Done at the end of the
   * turn that compacted, or ok=false at the end of the compaction turn without one; a turn of
   * a message that came in meanwhile does not end it.
   */
  private compacting: Compacting | null = null;
  /** The turn whose compaction already got the anchor (thread/compacted and the item both report it). */
  private anchoredTurn: string | null = null;

  constructor(private readonly ctx: DriverContext, private readonly deps: CodexDriverDeps) {
    this.codex = ctx.spec.codex!;
    this.threadId = ctx.spec.resume?.codex_thread_id ?? null;
  }

  get busy(): boolean {
    return this.activeTurn !== null;
  }

  async start(): Promise<StartResult> {
    const server = await this.deps.hub.ensure();
    this.closed = false;
    const url = this.deps.toolhost.rotate(this.ctx.spec.session_id, this.ctx.spec.platform_tools, (tool, args) => this.ctx.sink.callTool(tool, args));
    const params = this.threadParams(url);
    const resp = this.threadId ? await this.resume(server, params) : await server.request<{ thread: { id: string }; model?: string }>("thread/start", params);
    this.threadId = resp.thread.id;
    this.resolvedModel = this.codex.model || resp.model || undefined;
    this.deps.hub.register(this.threadId, this);
    return { codex_thread_id: this.threadId, init: await this.init(server) };
  }

  /**
   * thread/resume by id; when app-server does not know the id (a rollout copied here by a session
   * move is not in its index yet) and the rollout is on disk, again by its path.
   */
  private async resume(server: AppServer, params: Record<string, unknown>): Promise<{ thread: { id: string }; model?: string }> {
    const base = { ...params, threadId: this.threadId, excludeTurns: true };
    try {
      return await server.request("thread/resume", base);
    } catch (err) {
      const path = this.deps.findRollout?.(this.threadId!);
      if (!path) throw new ResumeFailed((err as Error).message);
      try {
        return await server.request("thread/resume", { ...base, path });
      } catch (err2) {
        throw new ResumeFailed((err2 as Error).message);
      }
    }
  }

  private threadParams(url: string): Record<string, unknown> {
    const config = {
      ...(this.codex.config ?? {}),
      ...autoCompactConfig(this.ctx.spec.context),
      ...(this.codex.effort ? { model_reasoning_effort: this.codex.effort } : {}),
      mcp_servers: { ...(this.deps.pluginServers ?? {}), platform: { url, default_tools_approval_mode: "approve", tool_timeout_sec: 86400 } },
      shell_environment_policy: { inherit: "core", set: this.ctx.env },
    };
    return {
      // An empty model is left out so Codex uses its own default: app-server rejects model "".
      ...(this.codex.model ? { model: this.codex.model } : {}),
      cwd: this.ctx.absCwd, approvalPolicy: askForApproval(this.codex.approval_policy), sandbox: this.codex.sandbox, config,
      ...(this.codex.approvals_reviewer ? { approvalsReviewer: this.codex.approvals_reviewer } : {}),
      ...(this.developerInstructions() ? { developerInstructions: this.developerInstructions() } : {}),
    };
  }

  private developerInstructions(): string {
    return [this.codex.developer_instructions, this.ctx.systemExtra].filter(Boolean).join("\n\n");
  }

  private async init(server: AppServer): Promise<StartResult["init"]> {
    const status = await server.request<{ data: Array<{ name: string; tools: Record<string, unknown>; toolsError: string | null }> }>("mcpServerStatus/list", { threadId: this.threadId, serverName: "platform", detail: "toolsAndAuthOnly" }).catch(() => ({ data: [] }));
    const platform = status.data.find((s) => s.name === "platform");
    const tools = Object.keys(platform?.tools ?? {}).map((t) => `mcp__platform__${t}`);
    const models = await this.models(server);
    return {
      ...(this.resolvedModel ? { model: this.resolvedModel } : {}), tools, permission_mode: `${this.codex.sandbox}/${this.codex.approval_policy}${this.codex.approvals_reviewer === "auto_review" ? "/auto_review" : ""}`,
      mcp_servers: [{ name: "platform", status: platform && !platform.toolsError ? "connected" : "failed" }],
      ...(this.codex.effort ? { effort: this.codex.effort } : {}), ...(models.length ? { models } : {}),
    };
  }

  /** The models the session can switch to (model/list, visible ones); [] when app-server does not answer. */
  private async models(server: AppServer): Promise<ModelOption[]> {
    try {
      const r = await server.request<{ data?: CodexModel[] }>("model/list", { includeHidden: false });
      return (r.data ?? []).filter((m) => !m.hidden).slice(0, 100).map(codexModelOption);
    } catch (err) {
      this.ctx.log.debug({ err: (err as Error).message }, "codex model/list failed");
      return [];
    }
  }

  async push(msg: OutgoingMessage): Promise<{ turn_id?: string }> {
    const server = await this.deps.hub.ensure();
    if (this.activeTurn && msg.priority === "later") {
      this.later.push(msg);
      return { turn_id: turnRef(this.activeTurn) };
    }
    if (this.activeTurn && (await this.steer(server, msg))) return { turn_id: turnRef(this.activeTurn) };
    return this.startTurn(server, msg);
  }

  private async steer(server: AppServer, msg: OutgoingMessage): Promise<boolean> {
    try {
      await server.request("turn/steer", { threadId: this.threadId, input: input(msg), expectedTurnId: this.activeTurn, clientUserMessageId: msg.msg_id });
      return true;
    } catch {
      return false;
    }
  }

  private async startTurn(server: AppServer, msg: OutgoingMessage): Promise<{ turn_id?: string }> {
    this.triggers.push(msg.msg_id);
    const resp = await server.request<{ turn: { id: string } }>("turn/start", { threadId: this.threadId, input: input(msg), clientUserMessageId: msg.msg_id, ...this.turnOverride });
    this.markTurn(resp.turn.id);
    return { turn_id: turnRef(resp.turn.id) };
  }

  private markTurn(turnId: string): void {
    if (this.activeTurn === turnId) return;
    if (this.compacting && !this.compacting.turn && this.triggers.length === 0) this.compacting.turn = turnId;
    this.activeTurn = turnId;
    this.turnStartedAt = Date.now();
    this.lastText = undefined;
    this.ctx.sink.turn(true);
    this.emit({ kind: "turn_started", trigger_msg_ids: this.triggers.splice(0), tools: this.ctx.spec.platform_tools.map((t) => `mcp__platform__${t.name}`) });
  }

  async interrupt(): Promise<string[]> {
    if (!this.activeTurn) return this.later.map((m) => m.msg_id);
    const server = await this.deps.hub.ensure();
    await server.request("turn/interrupt", { threadId: this.threadId, turnId: this.activeTurn });
    return this.later.map((m) => m.msg_id);
  }

  /**
   * set_mode from the next turn/start: the mode as approvalPolicy + sandboxPolicy + approvalsReviewer
   * (codexMode; turn/start takes the full sandbox policy, not thread/start's mode name), the model
   * and the reasoning effort. They stick for later turns of the thread.
   */
  async setMode(change: ModeChange): Promise<ModeApplied> {
    if (change.mode) {
      const m = codexMode(change.mode, this.codex);
      Object.assign(this.codex, m);
      Object.assign(this.turnOverride, modeTurnParams(m, this.codex.config, nodeRoot(this.ctx.absCwd, this.ctx.spec.cwd)));
    }
    if (change.model) {
      this.codex.model = change.model;
      this.turnOverride.model = change.model;
    }
    if (change.effort) {
      this.codex.effort = change.effort;
      this.turnOverride.effort = change.effort;
    }
    const model = this.codex.model || this.resolvedModel;
    return {
      permission_mode: this.codex.sandbox === "danger-full-access" ? "bypassPermissions" : this.codex.approvals_reviewer === "auto_review" ? "auto" : "default",
      ...(model ? { model } : {}), ...(this.codex.effort ? { effort: this.codex.effort } : {}), applies: "next_turn",
    };
  }

  /** Messages already in the thread history, found by their <!-- m_… --> marker (protocol.md §2.2). */
  async deliveredMsgIds(msgIds: string[]): Promise<Set<string>> {
    if (!this.threadId) return new Set();
    const server = await this.deps.hub.ensure();
    const history = JSON.stringify(await server.request("thread/read", { threadId: this.threadId, includeTurns: true }).catch(() => ({})));
    return new Set(msgIds.filter((id) => history.includes(`<!-- ${id} -->`)));
  }

  /** session.compact: thread/compact/start (no instructions in the app-server API). */
  async compact(): Promise<CompactResult> {
    if (!this.threadId) return { ok: false, error: "no codex thread" };
    const server = await this.deps.hub.ensure();
    const c: Compacting = { done: deferred<CompactResult>(), seen: false, turn: null };
    this.compacting = c; // before the request: its turn may start before the response arrives
    try {
      await server.request("thread/compact/start", { threadId: this.threadId });
    } catch (err) {
      if (this.compacting === c) this.compacting = null;
      return { ok: false, error: (err as Error).message };
    }
    return c.done.promise;
  }

  async close(): Promise<void> {
    this.finishCompact(false);
    this.closed = true;
    if (this.threadId) this.deps.hub.unregister(this.threadId);
    this.deps.toolhost.remove(this.ctx.spec.session_id);
    if (this.activeTurn) this.ctx.sink.turn(false);
    this.activeTurn = null;
  }

  onNotification(method: string, p: Record<string, unknown>): void {
    const handler = this.notificationHandlers[method];
    if (handler && !this.closed) handler(p);
  }

  private readonly notificationHandlers: Record<string, (p: Record<string, unknown>) => void> = {
    "turn/started": (p) => this.markTurn((p.turn as { id: string }).id),
    "turn/completed": (p) => this.onTurnCompleted(p.turn as { id: string; status: string }),
    "item/started": (p) => this.onItemStarted(p.item as { type: string; id: string }),
    "item/completed": (p) => this.onItemCompleted(p.item as { type: string; id: string }),
    "item/agentMessage/delta": (p) => this.ctx.sink.delta(String(p.delta ?? ""), this.activeTurn ? turnRef(this.activeTurn) : undefined),
    "serverRequest/resolved": (p) => this.onResolved(String(p.requestId)),
    "thread/tokenUsage/updated": (p) => this.onTokenUsage(p.tokenUsage as TokenUsage),
    "thread/compacted": (p) => this.onCompacted(String(p.turnId ?? "")),
    error: (p) => this.onError(p),
  };

  private onError(p: Record<string, unknown>): void {
    const ev = errorNotification((p.error ?? {}) as { message?: string }, Boolean(p.willRetry));
    if (!ev || (ev.code === "auth_failed" && this.authReported)) return;
    if (ev.code === "auth_failed") this.authReported = true;
    this.emit(ev);
  }

  private onTurnCompleted(turn: { id: string; status: string }): void {
    for (const ev of turnCompleted(turn, this.turnStartedAt, this.lastText)) this.emit(this.turnEnded(ev));
    this.activeTurn = null;
    const c = this.compacting;
    if (c && (c.seen || c.turn === turn.id)) this.finishCompact(c.seen);
    this.authReported = false;
    this.ctx.sink.turn(false);
    const next = this.later.shift();
    if (next) void this.push(next).catch((err) => this.ctx.log.warn({ err: (err as Error).message }, "queued codex message failed"));
  }

  private onItemStarted(item: { type: string; id: string; [k: string]: unknown }): void {
    this.itemStarts.set(item.id, Date.now());
    if (item.type === "fileChange") this.diffs.set(item.id, ((item.changes ?? []) as Array<{ diff?: string }>).map((c) => c.diff ?? "").join("\n"));
    for (const ev of itemStarted(item)) this.emit(ev);
  }

  /** turn_ended carries the model the turn ran with and the latest context use. */
  private turnEnded(ev: EventFields): EventFields {
    if (ev.kind !== "turn_ended") return ev;
    const model = (this.turnOverride.model as string | undefined) ?? this.resolvedModel;
    return { ...ev, ...(model ? { model } : {}), ...(this.usage ? { context: this.usage } : {}) };
  }

  private onTokenUsage(u: TokenUsage | undefined): void {
    if (!u?.last) return;
    this.usage = { used_tokens: Math.max(0, u.last.totalTokens), max_tokens: Math.max(0, u.modelContextWindow ?? 0) };
  }

  /**
   * A compaction finished (thread/compacted, or a contextCompaction item). For session.compact it
   * completes at the turn's end; one the runtime did by itself mid-turn gets the task anchor
   * steered into the running turn (shared-members.md §5.2).
   */
  private onCompacted(turnId: string): void {
    if (this.compacting) {
      this.compacting.seen = true;
      if (!this.activeTurn) this.finishCompact(true);
      return;
    }
    const anchor = this.ctx.sink.anchor();
    const turn = turnId || this.activeTurn;
    if (!anchor || !this.activeTurn || !turn || this.anchoredTurn === turn) return;
    this.anchoredTurn = turn;
    void this.steerAnchor(anchor);
  }

  /**
   * Only into the running turn (turn/steer): never a turn of its own. When the turn already ended
   * the anchor is dropped; the next task message brings its own brief.
   */
  private async steerAnchor(anchor: string): Promise<void> {
    const server = await this.deps.hub.ensure();
    const ok = this.activeTurn !== null && (await this.steer(server, { msg_id: newId("m_anchor_"), text: anchor, priority: "next" }));
    if (!ok) this.ctx.log.info({ session: this.ctx.spec.session_id }, "anchor after compaction dropped: no running turn");
  }

  private finishCompact(ok: boolean): void {
    const c = this.compacting;
    if (!c) return;
    this.compacting = null;
    c.done.resolve(ok ? { ok: true, ...(this.usage ? { context: this.usage } : {}) } : { ok: false, error: "the runtime did not compact" });
  }

  private onItemCompleted(item: { type: string; id: string; [k: string]: unknown }): void {
    if (item.type === "agentMessage" && typeof item.text === "string") this.lastText = item.text;
    if (item.type === "contextCompaction") this.onCompacted(this.activeTurn ?? "");
    for (const ev of itemCompleted(item, this.itemStarts.get(item.id))) this.emit(ev);
    this.itemStarts.delete(item.id);
  }

  onAppServerExit(err: Error): void {
    if (this.closed) return;
    this.activeTurn = null;
    this.ctx.sink.exited(err);
  }

  async onServerRequest(method: string, p: Record<string, unknown>, requestId: string): Promise<unknown> {
    p = { ...p, __rpcId: requestId };
    switch (method) {
      case "item/commandExecution/requestApproval":
        return { decision: await this.ask(p, { source: "codex_command", tool_name: "shell", input: { command: p.command ?? null, cwd: p.cwd ?? null }, description: str(p.reason) }) };
      case "item/fileChange/requestApproval":
        return { decision: await this.ask(p, { source: "codex_file_change", tool_name: "apply_patch", input: { grant_root: p.grantRoot ?? null }, description: str(p.reason), diff: this.diffs.get(String(p.itemId)) ?? null }) };
      case "item/permissions/requestApproval":
        return this.permissions(p);
      case "mcpServer/elicitation/request":
        return this.elicitation(p);
      default:
        throw new Error(`unsupported request ${method}`);
    }
  }

  private async permissions(p: Record<string, unknown>): Promise<unknown> {
    const decision = await this.ask(p, { source: "codex_permissions", tool_name: "permissions", input: (p.permissions ?? {}) as Record<string, unknown>, description: str(p.reason) });
    if (decision === "decline") return { permissions: {}, scope: "turn" };
    return { permissions: p.permissions ?? {}, scope: decision === "acceptForSession" ? "session" : "turn" };
  }

  private async elicitation(p: Record<string, unknown>): Promise<unknown> {
    const decision = await this.ask(p, { source: "codex_mcp_elicitation", tool_name: `mcp__${p.serverName}`, input: { message: p.message ?? null, mode: p.mode ?? null }, description: str(p.message) });
    return { action: decision === "decline" ? "decline" : "accept", content: null, _meta: null };
  }

  private async ask(p: Record<string, unknown>, a: Approval): Promise<"accept" | "acceptForSession" | "decline"> {
    const key = `${p.itemId ?? p.turnId ?? newId("x")}:${p.approvalId ?? ""}`;
    const id = approvalId(this.ctx.spec.session_id, key);
    this.approvalsByRequest.set(String(p.__rpcId), id);
    try {
      const res: ApprovalResolve = await this.ctx.sink.requestApproval({ approval_id: id, available: ["allow", "allow_always", "deny"], ...a, ...(p.itemId ? { tool_use_id: String(p.itemId) } : {}) });
      return DECISION[res.decision];
    } catch {
      return "decline";
    } finally {
      this.approvalsByRequest.delete(String(p.__rpcId));
    }
  }

  private onResolved(requestId: string): void {
    const id = this.approvalsByRequest.get(requestId);
    if (id) this.ctx.sink.cancelApproval(id, "codex_resolved");
  }

  private emit(ev: EventFields): void {
    this.ctx.sink.event(this.activeTurn ? { turn_id: turnRef(this.activeTurn), ...ev } : ev);
  }
}

type CodexModel = { id: string; model?: string; displayName?: string; description?: string; hidden?: boolean; isDefault?: boolean;
  supportedReasoningEfforts?: Array<{ reasoningEffort: string }>; defaultReasoningEffort?: string };

/** A model/list entry as a session.opened model option. */
export function codexModelOption(m: CodexModel): ModelOption {
  const efforts = (m.supportedReasoningEfforts ?? []).map((e) => e.reasoningEffort).filter(Boolean).slice(0, 32);
  return {
    id: m.model || m.id, ...(m.displayName ? { name: m.displayName.slice(0, 200) } : {}), ...(m.description ? { description: m.description.slice(0, 1000) } : {}),
    efforts, ...(m.defaultReasoningEffort ? { default_effort: m.defaultReasoningEffort } : {}), ...(m.isDefault ? { default: true } : {}),
  };
}

type Compacting = { done: Deferred<CompactResult>; seen: boolean; turn: string | null };
type TokenUsage = { last?: { totalTokens: number }; modelContextWindow?: number | null };

/** The role's in-task compaction as Codex config: an absolute token count (limit × pct; shared-members.md §5.1). */
export function autoCompactConfig(context: SessionOpen["context"]): Record<string, number> {
  const limit = effectiveLimit(context?.limit_tokens, context?.window_tokens);
  if (!limit || !context?.in_task_pct) return {};
  return { model_auto_compact_token_limit: Math.floor((limit * context.in_task_pct) / 100) };
}

/** The role's limit capped at the model's window (a larger one would never trigger), else the window. */
function effectiveLimit(limit?: number, window?: number): number {
  if (limit && window) return Math.min(limit, window);
  return limit ?? window ?? 0;
}

/** The node root: absCwd is root + the spec's relative cwd. */
export function nodeRoot(absCwd: string, cwd: string): string {
  const rel = cwd.replace(/^\.?\/+/, "").replace(/\/+$/, "");
  return rel && absCwd.endsWith(`/${rel}`) ? absCwd.slice(0, absCwd.length - rel.length - 1) : absCwd;
}

/** Protocol turn ids start with t_; Codex turn ids are UUIDv7. */
function turnRef(codexTurnId: string): string {
  return `t_${codexTurnId}`;
}

function input(msg: OutgoingMessage): Array<Record<string, unknown>> {
  return [{ type: "text", text: MARKER(msg.msg_id) + msg.text + codexFileNote(msg.images), text_elements: [] }, ...codexImages(msg.images)];
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v ? v : undefined;
}
