import type { ApprovalCancel, ApprovalRequest, ApprovalResolve, ContextDetail, ContextUsage, SessionEvent, SessionInit, SessionOpen } from "../protocol.js";
import type { McpToolResult } from "../toolhost/broker.js";
import type { Logger } from "../util/log.js";
import type { LoadedImage } from "./images.js";
import type { ToolPolicy } from "./policy.js";

export type EventFields = Omit<SessionEvent, "session_id" | "event_no" | "at" | "open_gen">;
export type ApprovalAsk = Omit<ApprovalRequest, "session_id" | "open_gen">;

/** What a driver reports back to the session manager. */
export interface SessionSink {
  event(fields: EventFields): void;
  delta(text: string, turnId?: string): void;
  turn(running: boolean): void;
  requestApproval(ask: ApprovalAsk): Promise<ApprovalResolve>;
  cancelApproval(approvalId: string, reason: ApprovalCancel["reason"]): void;
  callTool(tool: string, args: Record<string, unknown>, toolUseId?: string): Promise<McpToolResult>;
  /** The runtime process went away without close(). */
  exited(err: Error): void;
  /** The current task anchor (session.send anchor; "" = none), re-injected after the runtime compacts. */
  anchor(): string;
  /** The context breakdown after a turn (session.context reason turn; Claude only). */
  context?(detail: ContextDetail): void;
}

export interface DriverContext {
  spec: SessionOpen;
  absCwd: string;
  env: Record<string, string>;
  policy: ToolPolicy;
  sink: SessionSink;
  log: Logger;
  /** Platform text appended to the system prompt / developer instructions (e.g. remote servers). */
  systemExtra?: string;
}

export interface StartResult {
  sdk_session_id?: string | null;
  codex_thread_id?: string | null;
  init: SessionInit;
}

export interface OutgoingMessage {
  msg_id: string;
  text: string;
  priority: "now" | "next" | "later";
  /** Images the user attached, already downloaded. */
  images?: LoadedImage[];
}

/** A runtime (Claude Agent SDK query or Codex thread) behind one logical session. */
export interface Driver {
  start(): Promise<StartResult>;
  push(msg: OutgoingMessage): Promise<{ turn_id?: string }>;
  interrupt(): Promise<string[]>;
  setMode(mode?: string, model?: string | null): Promise<void>;
  close(): Promise<void>;
  readonly busy: boolean;
  /** msg_ids already present in the runtime's transcript (dedup after a daemon restart). */
  deliveredMsgIds?(msgIds: string[]): Promise<Set<string>>;
  /** Compacts the context now (session.compact); called between turns. */
  compact?(instructions: string): Promise<CompactResult>;
  /** The context breakdown now (session.context_refresh: full); undefined when it cannot be read. Claude only. */
  contextDetail?(detail: ContextDetail["detail"]): Promise<ContextDetail | undefined>;
}

/** The outcome of session.compact. */
export interface CompactResult {
  ok: boolean;
  context?: ContextUsage;
  error?: string;
}

export class ResumeFailed extends Error {}
