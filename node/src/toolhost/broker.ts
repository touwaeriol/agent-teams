import type { ContentBlock, ToolCancel, ToolResult } from "../protocol.js";
import { deferred, type Deferred } from "../util/async.js";
import { newId } from "../util/ids.js";

export type SendFn = (type: string, body: object) => void;

export interface ToolCallRequest {
  sessionId: string;
  openGen?: number;
  tool: string;
  args: Record<string, unknown>;
  toolUseId?: string;
}

export interface McpToolResult {
  [k: string]: unknown;
  content: ContentBlock[];
  isError?: boolean;
  structuredContent?: Record<string, unknown>;
}

interface Pending {
  sessionId: string;
  wait: Deferred<ToolResult>;
}

/**
 * Forwards platform tool calls to the console (protocol.md §6): tool.call (reliable,
 * call_id generated here, persisted by the outbox before sending) and waits — without
 * a timeout, blocking tools can take hours — for tool.result.
 */
export class ToolBroker {
  private readonly pending = new Map<string, Pending>();

  constructor(private readonly send: SendFn) {}

  async call(req: ToolCallRequest): Promise<McpToolResult> {
    const callId = newId("tc_");
    const wait = deferred<ToolResult>();
    this.pending.set(callId, { sessionId: req.sessionId, wait });
    this.send("tool.call", {
      call_id: callId,
      session_id: req.sessionId,
      ...(req.openGen !== undefined ? { open_gen: req.openGen } : {}),
      tool: req.tool,
      args: req.args,
      ...(req.toolUseId ? { tool_use_id: req.toolUseId } : {}),
    });
    return toMcp(await wait.promise);
  }

  /** Handles tool.result; false when no call is waiting (stale). */
  resolve(body: ToolResult): boolean {
    const p = this.pending.get(body.call_id);
    if (!p || p.sessionId !== body.session_id) return false;
    this.pending.delete(body.call_id);
    p.wait.resolve(body);
    return true;
  }

  cancelSession(sessionId: string, reason: ToolCancel["reason"]): void {
    for (const id of this.pendingFor(sessionId)) {
      const p = this.pending.get(id)!;
      this.pending.delete(id);
      this.send("tool.cancel", { call_id: id, session_id: sessionId, reason });
      p.wait.resolve({ session_id: sessionId, call_id: id, ok: false, content: [{ type: "text", text: `Tool call cancelled (${reason}).` }] });
    }
  }

  pendingFor(sessionId: string): string[] {
    return [...this.pending].filter(([, p]) => p.sessionId === sessionId).map(([id]) => id);
  }
}

function toMcp(r: ToolResult): McpToolResult {
  const out: McpToolResult = { content: r.content.length ? r.content : [{ type: "text", text: r.ok ? "ok" : "error" }] };
  if (!r.ok) out.isError = true;
  if (r.structured) out.structuredContent = r.structured;
  return out;
}
