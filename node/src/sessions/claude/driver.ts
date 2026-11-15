import { randomUUID } from "node:crypto";
import { query as sdkQuery, type HookCallbackMatcher, type HookEvent, type HookInput, type HookJSONOutput, type Options, type PermissionUpdate, type Query, type SDKMessage, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import type { ClaudeSessionOptions } from "../../protocol.js";
import { AsyncQueue } from "../../util/async.js";
import { approvalId, msgUuid } from "../../util/ids.js";
import { newId } from "../../util/ids.js";
import { platformSdkServer, PLATFORM_PREFIX } from "../../toolhost/sdk-server.js";
import { decideTool } from "../policy.js";
import { ResumeFailed, type Driver, type DriverContext, type EventFields, type OutgoingMessage, type StartResult } from "../types.js";
import { buildClaudeOptions } from "./options.js";
import { describeTool, mapClaudeMessage } from "./mapper.js";
import { toPermissionResult } from "./permissions.js";
import { ToolUseCorrelator } from "./correlate.js";
import { findTranscript, transcriptUserUuids } from "./transcript.js";

export type QueryFn = (params: { prompt: AsyncIterable<SDKUserMessage>; options: Options }) => Query;

export interface ClaudeDriverDeps {
  root: string;
  claudeConfigDir: string;
  secretPaths: string[];
  queryFn?: QueryFn;
  /** The role's skills as a local plugin (written by the daemon before the driver starts). */
  skills?: { pluginDir: string; names: string[] } | null;
  /** Wait this long after a result for folded-in messages before declaring the turn idle. */
  foldGraceMs?: number;
}

/**
 * One long-lived Claude employee session: a single Agent SDK query() with streaming input
 * (protocol.md §4). Messages are pushed into the prompt queue at any time; parking closes
 * the query and the next start() resumes it by SDK session id.
 */
export class ClaudeDriver implements Driver {
  private q: Query | null = null;
  private prompt: AsyncQueue<SDKUserMessage> | null = null;
  private closing = false;
  private turnId: string | undefined;
  private lastTools: string[] | null = null;
  private readonly waitingUuids = new Map<string, string>();
  private readonly uuidToMsg = new Map<string, string>();
  private readonly toolStarts = new Map<string, number>();
  private readonly correlator = new ToolUseCorrelator();
  private running = false;
  private idleTimer: NodeJS.Timeout | null = null;
  private sdkSessionId: string;
  private readonly claude: ClaudeSessionOptions;

  constructor(private readonly ctx: DriverContext, private readonly deps: ClaudeDriverDeps) {
    this.claude = ctx.spec.claude!;
    this.sdkSessionId = ctx.spec.resume?.sdk_session_id ?? randomUUID();
  }

  get busy(): boolean {
    return this.running;
  }

  async start(): Promise<StartResult> {
    const resume = findTranscript(this.deps.claudeConfigDir, this.sdkSessionId) !== null;
    if (!resume && this.ctx.spec.resume?.sdk_session_id && this.lastTools === null) throw new ResumeFailed(`transcript ${this.sdkSessionId} not found`);
    this.closing = false;
    this.prompt = new AsyncQueue<SDKUserMessage>();
    this.q = (this.deps.queryFn ?? sdkQuery)({ prompt: this.prompt, options: this.options(resume) });
    void this.consume(this.q);
    return this.initialize(this.q, resume);
  }

  private options(resume: boolean): Options {
    const server = platformSdkServer(this.ctx.spec.platform_tools, (tool, args) =>
      this.ctx.sink.callTool(tool, args, this.correlator.take(tool, args)),
    );
    return buildClaudeOptions({
      claude: this.claude, root: this.deps.root, cwd: this.ctx.absCwd, env: this.ctx.env, sessionId: this.sdkSessionId, resume, server,
      canUseTool: (tool, input, opts) => this.canUseTool(tool, input, opts),
      hooks: this.hooks(), secretPaths: this.deps.secretPaths, skills: this.deps.skills ?? null,
      stderr: (d) => this.ctx.log.debug({ stderr: d.slice(0, 2000) }, "claude stderr"),
    });
  }

  private async initialize(q: Query, resume: boolean): Promise<StartResult> {
    try {
      const init = await q.initializationResult();
      const mcp = await q.mcpServerStatus();
      const platform = mcp.find((s) => s.name === "platform");
      const tools = [...this.claude.tools, ...(platform?.tools ?? []).map((t) => PLATFORM_PREFIX + t.name)];
      this.lastTools ??= tools;
      return {
        sdk_session_id: this.sdkSessionId,
        init: { model: this.claude.model, tools, permission_mode: this.claude.permission_mode, ...(init.claude_code_version ? { claude_code_version: init.claude_code_version } : {}), mcp_servers: mcp.map((s) => ({ name: s.name, status: s.status })) },
      };
    } catch (err) {
      await this.close();
      if (resume) throw new ResumeFailed((err as Error).message);
      throw err;
    }
  }

  async push(msg: OutgoingMessage): Promise<{ turn_id?: string }> {
    if (!this.prompt) throw new Error("session is not running");
    const uuid = msgUuid(msg.msg_id);
    this.uuidToMsg.set(uuid, msg.msg_id);
    this.waitingUuids.set(uuid, msg.msg_id);
    this.setRunning(true);
    this.prompt.push({ type: "user", message: { role: "user", content: msg.text }, parent_tool_use_id: null, uuid: uuid as SDKUserMessage["uuid"], priority: msg.priority });
    return this.turnId ? { turn_id: this.turnId } : {};
  }

  async interrupt(): Promise<string[]> {
    if (!this.q) return [];
    const receipt = await this.q.interrupt();
    return (receipt?.still_queued ?? []).map((u) => this.uuidToMsg.get(u)).filter((x): x is string => Boolean(x));
  }

  async setMode(mode?: string, model?: string | null): Promise<void> {
    if (!this.q) throw new Error("session is not running");
    if (mode === "bypassPermissions" && !this.claude.allow_dangerously_skip_permissions) throw new Error("bypassPermissions requires a session opened with allow_dangerously_skip_permissions");
    if (mode) await this.q.setPermissionMode(mode as Parameters<Query["setPermissionMode"]>[0]);
    if (model) await this.q.setModel(model);
    if (mode) this.claude.permission_mode = mode as ClaudeSessionOptions["permission_mode"];
    if (model) this.claude.model = model;
  }

  async close(): Promise<void> {
    this.closing = true;
    this.prompt?.end();
    this.q?.close();
    this.q = null;
    this.prompt = null;
    this.setRunning(false);
  }

  async deliveredMsgIds(msgIds: string[]): Promise<Set<string>> {
    const path = findTranscript(this.deps.claudeConfigDir, this.sdkSessionId);
    if (!path) return new Set();
    const uuids = transcriptUserUuids(path);
    return new Set(msgIds.filter((id) => uuids.has(msgUuid(id))));
  }

  private async consume(q: Query): Promise<void> {
    try {
      for await (const msg of q) this.onMessage(msg);
      if (!this.closing && this.q === q) this.ctx.sink.exited(new Error("claude process ended"));
    } catch (err) {
      if (!this.closing && this.q === q) this.ctx.sink.exited(err as Error);
    }
  }

  private onMessage(msg: SDKMessage): void {
    if (msg.type === "system" && msg.subtype === "init") return this.onInit(msg.tools);
    if (msg.type === "assistant") this.onAssistant(msg.user_message_uuids, msg.message.content);
    for (const ev of mapClaudeMessage(msg)) this.emit(ev);
    if (msg.type === "result") this.onResult();
  }

  private onInit(tools: string[]): void {
    this.turnId = newId("t_");
    const triggers = [...this.waitingUuids.values()];
    this.waitingUuids.clear();
    this.setRunning(true);
    this.emit({ kind: "turn_started", trigger_msg_ids: triggers, tools });
  }

  private onAssistant(uuids: string[] | undefined, content: unknown): void {
    for (const u of uuids ?? []) this.waitingUuids.delete(u);
    if (!this.ctx.sink || !Array.isArray(content)) return;
    for (const block of content as Array<{ type: string; text?: string }>) {
      if (block.type === "text" && block.text) this.ctx.sink.delta(block.text, this.turnId);
    }
  }

  private onResult(): void {
    if (this.waitingUuids.size === 0) return this.setRunning(false);
    if (this.idleTimer) clearTimeout(this.idleTimer);
    this.idleTimer = setTimeout(() => {
      this.waitingUuids.clear();
      this.setRunning(false);
    }, this.deps.foldGraceMs ?? 3000);
  }

  private setRunning(on: boolean): void {
    if (this.idleTimer && on) clearTimeout(this.idleTimer);
    if (this.running === on) return;
    this.running = on;
    this.ctx.sink.turn(on);
  }

  private emit(ev: EventFields): void {
    this.ctx.sink.event(this.turnId ? { turn_id: this.turnId, ...ev } : ev);
  }

  private async canUseTool(tool: string, input: Record<string, unknown>, opts: { signal: AbortSignal; suggestions?: PermissionUpdate[]; toolUseID: string; title?: string; description?: string; decisionReason?: string; blockedPath?: string }) {
    const id = approvalId(this.ctx.spec.session_id, opts.toolUseID);
    const onAbort = () => this.ctx.sink.cancelApproval(id, "interrupted");
    opts.signal.addEventListener("abort", onAbort, { once: true });
    try {
      const res = await this.ctx.sink.requestApproval({
        approval_id: id, source: "claude_can_use_tool", tool_name: tool, tool_use_id: opts.toolUseID, input,
        ...(opts.title ? { title: opts.title } : {}), ...(opts.description ? { description: opts.description } : {}),
        ...(opts.decisionReason ? { decision_reason: opts.decisionReason } : {}), blocked_path: opts.blockedPath ?? null,
        suggestions: (opts.suggestions ?? []) as unknown as Record<string, unknown>[], available: ["allow", "allow_always", "deny"],
      });
      return toPermissionResult(res, input, opts.suggestions);
    } catch {
      return { behavior: "deny" as const, message: "The approval request was cancelled." };
    } finally {
      opts.signal.removeEventListener("abort", onAbort);
    }
  }

  private hooks(): Partial<Record<HookEvent, HookCallbackMatcher[]>> {
    const on = (fn: (input: HookInput) => HookJSONOutput | void) => [{ hooks: [async (input: HookInput) => fn(input) ?? {}] }];
    return {
      PreToolUse: on((i) => this.preToolUse(i)),
      PostToolUse: on((i) => this.toolEnded(i, true)),
      PostToolUseFailure: on((i) => this.toolEnded(i, false)),
      Notification: on((i) => this.notification(i)),
      SubagentStart: on((i) => this.subagent(i, true)),
      SubagentStop: on((i) => this.subagent(i, false)),
    };
  }

  private preToolUse(i: HookInput): HookJSONOutput | void {
    if (i.hook_event_name !== "PreToolUse") return;
    const input = (i.tool_input ?? {}) as Record<string, unknown>;
    const verdict = decideTool(this.ctx.policy, i.tool_name, input, this.ctx.absCwd);
    if (verdict) return { hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: verdict.deny } };
    if (i.tool_name.startsWith(PLATFORM_PREFIX)) this.correlator.remember(i.tool_name.slice(PLATFORM_PREFIX.length), input, i.tool_use_id);
    this.toolStarts.set(i.tool_use_id, Date.now());
    this.emit({ kind: "tool_started", tool: i.tool_name, tool_use_id: i.tool_use_id, ...describeTool(i.tool_name, input) });
  }

  private toolEnded(i: HookInput, ok: boolean): void {
    if (i.hook_event_name !== "PostToolUse" && i.hook_event_name !== "PostToolUseFailure") return;
    const started = this.toolStarts.get(i.tool_use_id);
    this.toolStarts.delete(i.tool_use_id);
    const duration = i.duration_ms ?? (started ? Date.now() - started : 0);
    this.emit({ kind: "tool_ended", tool_use_id: i.tool_use_id, ok, duration_ms: duration });
  }

  private notification(i: HookInput): void {
    if (i.hook_event_name !== "Notification") return;
    this.emit({ kind: "notification", message: i.message, notification_type: i.notification_type });
  }

  private subagent(i: HookInput, on: boolean): void {
    if (i.hook_event_name !== "SubagentStart" && i.hook_event_name !== "SubagentStop") return;
    this.emit({ kind: "subagent", on, agent_type: i.agent_type });
  }
}
