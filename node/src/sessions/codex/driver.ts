import type { ApprovalRequest, ApprovalResolve, CodexSessionOptions } from "../../protocol.js";
import type { LoopbackToolHost } from "../../toolhost/http-server.js";
import { approvalId, newId } from "../../util/ids.js";
import { ResumeFailed, type Driver, type DriverContext, type EventFields, type OutgoingMessage, type StartResult } from "../types.js";
import type { AppServer } from "./appserver.js";
import type { CodexHub, ThreadListener } from "./hub.js";
import { errorNotification, itemCompleted, itemStarted, turnCompleted } from "./mapper.js";

export interface CodexDriverDeps {
  hub: CodexHub;
  toolhost: LoopbackToolHost;
  /** Plugin MCP servers in Codex config form (sessions/plugins.ts codexMcpServers). */
  pluginServers?: Record<string, Record<string, unknown>>;
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
  private modeOverride: { approvalPolicy?: string; sandbox?: string; model?: string } = {};
  private closed = false;
  private authReported = false;
  private readonly codex: CodexSessionOptions;

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
    const resp = this.threadId ? await this.resume(server, params) : await server.request<{ thread: { id: string } }>("thread/start", params);
    this.threadId = resp.thread.id;
    this.deps.hub.register(this.threadId, this);
    return { codex_thread_id: this.threadId, init: await this.init(server) };
  }

  private async resume(server: AppServer, params: Record<string, unknown>): Promise<{ thread: { id: string } }> {
    try {
      return await server.request("thread/resume", { ...params, threadId: this.threadId, excludeTurns: true });
    } catch (err) {
      throw new ResumeFailed((err as Error).message);
    }
  }

  private threadParams(url: string): Record<string, unknown> {
    const config = {
      ...(this.codex.config ?? {}),
      mcp_servers: { ...(this.deps.pluginServers ?? {}), platform: { url, default_tools_approval_mode: "approve", tool_timeout_sec: 86400 } },
      shell_environment_policy: { inherit: "core", set: this.ctx.env },
    };
    return {
      model: this.codex.model, cwd: this.ctx.absCwd, approvalPolicy: this.codex.approval_policy, sandbox: this.codex.sandbox, config,
      ...(this.codex.developer_instructions ? { developerInstructions: this.codex.developer_instructions } : {}),
    };
  }

  private async init(server: AppServer): Promise<StartResult["init"]> {
    const status = await server.request<{ data: Array<{ name: string; tools: Record<string, unknown>; toolsError: string | null }> }>("mcpServerStatus/list", { threadId: this.threadId, serverName: "platform", detail: "toolsAndAuthOnly" }).catch(() => ({ data: [] }));
    const platform = status.data.find((s) => s.name === "platform");
    const tools = Object.keys(platform?.tools ?? {}).map((t) => `mcp__platform__${t}`);
    return { model: this.codex.model, tools, permission_mode: `${this.codex.sandbox}/${this.codex.approval_policy}`, mcp_servers: [{ name: "platform", status: platform && !platform.toolsError ? "connected" : "failed" }] };
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
    const resp = await server.request<{ turn: { id: string } }>("turn/start", { threadId: this.threadId, input: input(msg), clientUserMessageId: msg.msg_id, ...this.modeOverride });
    this.markTurn(resp.turn.id);
    return { turn_id: turnRef(resp.turn.id) };
  }

  private markTurn(turnId: string): void {
    if (this.activeTurn === turnId) return;
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

  async setMode(mode?: string, model?: string | null): Promise<void> {
    if (mode === "bypassPermissions") this.modeOverride = { ...this.modeOverride, approvalPolicy: "never", sandbox: "danger-full-access" };
    if (mode === "default" || mode === "acceptEdits") this.modeOverride = { ...(this.modeOverride.model ? { model: this.modeOverride.model } : {}) };
    if (model) this.modeOverride.model = model;
  }

  /** Messages already in the thread history, found by their <!-- m_… --> marker (protocol.md §2.2). */
  async deliveredMsgIds(msgIds: string[]): Promise<Set<string>> {
    if (!this.threadId) return new Set();
    const server = await this.deps.hub.ensure();
    const history = JSON.stringify(await server.request("thread/read", { threadId: this.threadId, includeTurns: true }).catch(() => ({})));
    return new Set(msgIds.filter((id) => history.includes(`<!-- ${id} -->`)));
  }

  async close(): Promise<void> {
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
    error: (p) => this.onError(p),
  };

  private onError(p: Record<string, unknown>): void {
    const ev = errorNotification((p.error ?? {}) as { message?: string }, Boolean(p.willRetry));
    if (!ev || (ev.code === "auth_failed" && this.authReported)) return;
    if (ev.code === "auth_failed") this.authReported = true;
    this.emit(ev);
  }

  private onTurnCompleted(turn: { id: string; status: string }): void {
    for (const ev of turnCompleted(turn, this.turnStartedAt, this.lastText)) this.emit(ev);
    this.activeTurn = null;
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

  private onItemCompleted(item: { type: string; id: string; [k: string]: unknown }): void {
    if (item.type === "agentMessage" && typeof item.text === "string") this.lastText = item.text;
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

/** Protocol turn ids start with t_; Codex turn ids are UUIDv7. */
function turnRef(codexTurnId: string): string {
  return `t_${codexTurnId}`;
}

function input(msg: OutgoingMessage): Array<Record<string, unknown>> {
  return [{ type: "text", text: MARKER(msg.msg_id) + msg.text, text_elements: [] }];
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v ? v : undefined;
}
