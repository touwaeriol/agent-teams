import type { ContentBlock, ToolCancel, ToolResult } from "../protocol.js";
import { deferred, type Deferred } from "../util/async.js";
import { newId } from "../util/ids.js";
import { prepareUpload, type PreparedUpload } from "./upload.js";

export type SendFn = (type: string, body: object) => void;

export interface ToolCallRequest {
  sessionId: string;
  openGen?: number;
  tool: string;
  args: Record<string, unknown>;
  toolUseId?: string;
  /** Absolute working directory of the session (publish_file reads files from it). */
  cwd?: string;
}

/** Tools whose files the daemon uploads (file.chunk frames) before forwarding the call. */
export const UPLOAD_TOOLS = new Set(["publish_file"]);

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
    let upload: PreparedUpload | undefined;
    if (UPLOAD_TOOLS.has(req.tool)) {
      try {
        upload = await prepareUpload(req.cwd ?? "", req.args.path);
      } catch (err) {
        return { content: [{ type: "text", text: `publish_file: ${(err as Error).message}` }], isError: true };
      }
    }
    const callId = newId("tc_");
    const wait = deferred<ToolResult>();
    this.pending.set(callId, { sessionId: req.sessionId, wait });
    if (upload) this.sendChunks(req, upload);
    this.send("tool.call", {
      call_id: callId,
      session_id: req.sessionId,
      ...(req.openGen !== undefined ? { open_gen: req.openGen } : {}),
      tool: req.tool,
      args: req.args,
      ...(req.toolUseId ? { tool_use_id: req.toolUseId } : {}),
      ...(upload ? { upload: upload.upload } : {}),
    });
    return toMcp(await wait.promise);
  }

  /** file.chunk frames go out (reliable, in order) before the tool.call that claims them. */
  private sendChunks(req: ToolCallRequest, up: PreparedUpload): void {
    up.chunks.forEach((data, index) =>
      this.send("file.chunk", {
        session_id: req.sessionId,
        ...(req.openGen !== undefined ? { open_gen: req.openGen } : {}),
        upload_id: up.upload.upload_id,
        index,
        data,
      }),
    );
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
