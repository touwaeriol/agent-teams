import { appendFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { Options, Query, SDKMessage, SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";

export interface FakeQueryLog {
  options: Options[];
  prompts: string[];
  closed: number;
  interrupts: number;
  modes: string[];
  models?: string[];
  /** applyFlagSettings calls. */
  flags?: Array<Record<string, unknown>>;
  /** What PreCompact / SessionStart hooks returned (compaction tests). */
  hooks?: Array<{ event: string; output: unknown }>;
}

/**
 * Stand-in for the Agent SDK query(): replies "echo: <text>"; "/compact <instructions>" and
 * "autocompact" compact like SDK 0.3.325 does (hooks, compact_boundary, context shrinks); "use <tool> <json>" calls a
 * platform tool through the real in-process MCP server; "run <command>" asks canUseTool for
 * Bash (after running the PreToolUse hooks) and reports the decision.
 */
export function fakeQueryFactory(log: FakeQueryLog) {
  return (params: { prompt: AsyncIterable<SDKUserMessage>; options: Options }): Query => {
    log.options.push(params.options);
    return new FakeQuery(params.prompt, params.options, log) as unknown as Query;
  };
}

/** initialize's models, shaped like SDK 0.3.325's (an alias row, a default row, a model without auto). */
export const FAKE_MODELS = [
  { value: "default", resolvedModel: "claude-opus-5-5", displayName: "Default (recommended)", description: "Opus 5.5", supportedEffortLevels: ["low", "medium", "high", "xhigh", "max"], supportsAutoMode: true },
  { value: "opus", resolvedModel: "claude-opus-5-5", displayName: "Opus 5.5", description: "For complex work", supportedEffortLevels: ["low", "medium", "high", "xhigh", "max"], supportsAutoMode: true },
  { value: "claude-haiku-x", resolvedModel: "claude-haiku-x", displayName: "Haiku X", description: "", supportedEffortLevels: ["low", "medium", "high"], supportsAutoMode: true },
  { value: "claude-haiku-4-5-20251001", resolvedModel: "claude-haiku-4-5-20251001", displayName: "Haiku 4.5", description: "" },
];

class FakeQuery {
  private out: SDKMessage[] = [];
  private wake: (() => void) | null = null;
  private done = false;
  private client: Client | null = null;
  private readonly sid: string;

  constructor(private readonly prompt: AsyncIterable<SDKUserMessage>, private readonly options: Options, private readonly log: FakeQueryLog) {
    this.sid = options.resume ?? options.sessionId ?? "sid";
    void this.loop();
  }

  async initializationResult() {
    return {
      claude_code_version: "2.1.325-fake", account: {}, agents: [], models: FAKE_MODELS,
      commands: [{ name: "design", description: "Claude Design access", argumentHint: "", builtin: true }, { name: "agent-teams-skills:design", description: "Make HTML prototypes", argumentHint: "" }],
    };
  }

  async mcpServerStatus() {
    const tools = await (await this.mcp()).listTools();
    return [{ name: "platform", status: "connected", tools: tools.tools.map((t) => ({ name: t.name })) }];
  }

  async interrupt() {
    this.log.interrupts++;
    return { still_queued: [] };
  }

  async setPermissionMode(m: string) {
    // Like SDK 0.3.325: auto only for a model with supportsAutoMode.
    const model = this.model ?? this.options.model;
    if (m === "auto" && !FAKE_MODELS.some((x) => (x.value === model || x.resolvedModel === model) && x.supportsAutoMode)) throw new Error("Cannot set permission mode to auto: auto mode unavailable for this model");
    this.log.modes.push(m);
  }

  async applyFlagSettings(s: Record<string, unknown>) {
    (this.log.flags ??= []).push(s);
  }

  private model: string | undefined;
  /** Context use: grows 1000 tokens per prompt, a compaction brings it back to 500. */
  private tokens = 5000;

  async getContextUsage() {
    const max = Number(this.options.env?.CLAUDE_CODE_AUTO_COMPACT_WINDOW ?? 200000);
    return { totalTokens: this.tokens, maxTokens: max, rawMaxTokens: max, percentage: Math.round((this.tokens / max) * 100), categories: [], gridRows: [] };
  }

  async setModel(m?: string) {
    (this.log.models ??= []).push(m ?? "");
    this.model = m;
  }

  close() {
    this.log.closed++;
    this.done = true;
    this.wake?.();
  }

  private crashed = false;

  async *[Symbol.asyncIterator](): AsyncGenerator<SDKMessage> {
    while (!this.done || this.out.length) {
      if (this.crashed) throw new Error("claude process exited with code 1");
      const m = this.out.shift();
      if (m) yield m;
      else await new Promise<void>((r) => (this.wake = r));
    }
  }

  private push(m: Record<string, unknown>): void {
    this.out.push(m as unknown as SDKMessage);
    this.wake?.();
  }

  private async mcp(): Promise<Client> {
    if (this.client) return this.client;
    const server = (this.options.mcpServers!.platform as unknown as { instance: { connect(t: unknown): Promise<void> } }).instance;
    const [a, b] = InMemoryTransport.createLinkedPair();
    await server.connect(a);
    this.client = new Client({ name: "fake", version: "1" });
    await this.client.connect(b);
    return this.client;
  }

  private async loop(): Promise<void> {
    for await (const msg of this.prompt) {
      if (this.done) return;
      const text = promptText(msg.message.content);
      this.log.prompts.push(text);
      if (text === "crash" || text === "/compact crash") {
        this.crashed = true;
        this.wake?.();
        return;
      }
      this.transcript({ type: "user", uuid: msg.uuid, message: { role: "user", content: text } });
      this.tokens += 1000;
      this.push({ type: "system", subtype: "init", tools: [...(this.options.tools as string[]), "mcp__platform__complete_task"], session_id: this.sid });
      if (text.startsWith("/compact")) {
        await this.compaction("manual", text.slice("/compact".length).trim() || null);
        this.push({ type: "result", subtype: "success", result: "", is_error: false, num_turns: 0, duration_ms: 5, total_cost_usd: 0, usage: {}, session_id: this.sid });
        continue;
      }
      if (text === "autocompact") await this.compaction("auto", null);
      const reply = await this.respond(text);
      this.push({ type: "assistant", message: { model: this.model ?? this.options.model, content: [{ type: "text", text: reply }] }, parent_tool_use_id: null, user_message_uuids: [msg.uuid], session_id: this.sid });
      this.push({ type: "result", subtype: "success", result: reply, is_error: false, num_turns: 1, duration_ms: 5, total_cost_usd: 0, usage: {}, session_id: this.sid });
    }
  }

  /** PreCompact hooks, status compacting, compact_boundary, SessionStart(compact) hooks. */
  private async compaction(trigger: "manual" | "auto", instructions: string | null): Promise<void> {
    await this.runHooks("PreCompact", { trigger, custom_instructions: instructions });
    this.push({ type: "system", subtype: "status", status: "compacting", session_id: this.sid });
    const pre = this.tokens;
    this.tokens = 500;
    this.push({ type: "system", subtype: "compact_boundary", compact_metadata: { trigger, pre_tokens: pre, post_tokens: this.tokens }, session_id: this.sid });
    await this.runHooks("SessionStart", { source: "compact" });
  }

  private async runHooks(event: "PreCompact" | "SessionStart", fields: Record<string, unknown>): Promise<void> {
    for (const m of this.options.hooks?.[event] ?? []) {
      for (const h of m.hooks) {
        const output = await h({ hook_event_name: event, ...fields, session_id: this.sid, transcript_path: "", cwd: this.options.cwd! } as never, undefined, { signal: new AbortController().signal });
        (this.log.hooks ??= []).push({ event, output });
      }
    }
  }

  private transcript(rec: Record<string, unknown>): void {
    const dir = this.options.env?.CLAUDE_CONFIG_DIR;
    if (!dir) return;
    mkdirSync(join(dir, "projects", "fake"), { recursive: true });
    appendFileSync(join(dir, "projects", "fake", `${this.sid}.jsonl`), JSON.stringify(rec) + "\n");
  }

  private async respond(text: string): Promise<string> {
    const use = /^use (\w+) (.*)$/s.exec(text);
    if (use) return this.useTool(use[1]!, JSON.parse(use[2]!));
    const run = /^run (.*)$/s.exec(text);
    if (run) return this.runBash(run[1]!);
    const builtin = /^builtin (\w+) (.*)$/s.exec(text);
    if (builtin) return this.builtin(builtin[1]!, JSON.parse(builtin[2]!));
    return `echo: ${text}`;
  }

  private async useTool(tool: string, args: Record<string, unknown>): Promise<string> {
    const denied = await this.preToolUse(`mcp__platform__${tool}`, args, `toolu_${tool}`);
    if (denied) return `denied: ${denied}`;
    const res = await (await this.mcp()).callTool({ name: tool, arguments: args });
    const content = res.content as Array<{ text: string }>;
    return `tool ${res.isError ? "error" : "ok"}: ${content.map((c) => c.text).join("")}`;
  }

  private async runBash(command: string): Promise<string> {
    const id = `toolu_${Math.random().toString(36).slice(2)}`;
    const denied = await this.preToolUse("Bash", { command }, id);
    if (denied) return `denied by hook: ${denied}`;
    const ctrl = new AbortController();
    const r = await this.options.canUseTool!("Bash", { command }, { signal: ctrl.signal, toolUseID: id, suggestions: [{ type: "addRules", rules: [{ toolName: "Bash", ruleContent: command }], behavior: "allow", destination: "localSettings" }], requestId: "r1" } as never);
    const res = r as { behavior: string; updatedPermissions?: unknown[]; message?: string };
    return res.behavior === "allow" ? `allowed ${JSON.stringify(res.updatedPermissions ?? [])}` : `refused: ${res.message}`;
  }

  /** A built-in tool (AskUserQuestion, TodoWrite, ExitPlanMode): PreToolUse hooks (by matcher), then PostToolUse. */
  private async builtin(tool: string, input: Record<string, unknown>): Promise<string> {
    const id = `toolu_${tool}_${Math.random().toString(36).slice(2, 8)}`;
    let effective = input;
    for (const m of this.options.hooks?.PreToolUse ?? []) {
      if (m.matcher && m.matcher !== tool) continue;
      for (const h of m.hooks) {
        const out = (await h({ hook_event_name: "PreToolUse", tool_name: tool, tool_input: input, tool_use_id: id, session_id: this.sid, transcript_path: "", cwd: this.options.cwd! } as never, id, { signal: new AbortController().signal })) as { hookSpecificOutput?: { permissionDecision?: string; permissionDecisionReason?: string; updatedInput?: Record<string, unknown> } };
        if (out.hookSpecificOutput?.permissionDecision === "deny") return `denied: ${out.hookSpecificOutput.permissionDecisionReason}`;
        if (out.hookSpecificOutput?.updatedInput) effective = out.hookSpecificOutput.updatedInput;
      }
    }
    const response = tool === "ExitPlanMode" ? { plan: null, filePath: String(input.planFile ?? "") } : {};
    for (const m of this.options.hooks?.PostToolUse ?? []) {
      for (const h of m.hooks) await h({ hook_event_name: "PostToolUse", tool_name: tool, tool_input: input, tool_response: response, tool_use_id: id, session_id: this.sid, transcript_path: "", cwd: this.options.cwd! } as never, id, { signal: new AbortController().signal });
    }
    return `${tool} ran with ${JSON.stringify(effective.answers ?? effective)}`;
  }

  private async preToolUse(tool: string, input: Record<string, unknown>, id: string): Promise<string | null> {
    for (const m of this.options.hooks?.PreToolUse ?? []) {
      for (const h of m.hooks) {
        const out = (await h({ hook_event_name: "PreToolUse", tool_name: tool, tool_input: input, tool_use_id: id, session_id: this.sid, transcript_path: "", cwd: this.options.cwd! } as never, id, { signal: new AbortController().signal })) as { hookSpecificOutput?: { permissionDecision?: string; permissionDecisionReason?: string } };
        if (out.hookSpecificOutput?.permissionDecision === "deny") return out.hookSpecificOutput.permissionDecisionReason ?? "deny";
      }
    }
    return null;
  }
}

/** The prompt as text; image blocks become "[image <type> <bytes>B]". */
function promptText(content: unknown): string {
  if (typeof content === "string") return content;
  return (content as Array<{ type: string; text?: string; source?: { media_type: string; data: string } }>)
    .map((b) => (b.type === "image" ? `[image ${b.source!.media_type} ${Buffer.from(b.source!.data, "base64").length}B]` : b.text ?? ""))
    .join(" ");
}
