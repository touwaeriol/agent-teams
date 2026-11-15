import type { ApprovalCancel, SessionInit, SessionOpen, SessionSend, SessionState } from "../protocol.js";
import type { ApprovalBroker } from "../approvals.js";
import type { ToolBroker } from "../toolhost/broker.js";
import type { ApprovalAsk, Driver, EventFields, SessionSink } from "./types.js";

export type State = SessionState["state"];
export type SendFn = (type: string, body: object) => void;

export interface SessionRecord {
  session_id: string;
  open_gen: number;
  kind: "claude" | "codex";
  state: State;
  released: boolean;
  sdk_session_id: string | null;
  codex_thread_id: string | null;
  spec: SessionOpen;
}

export interface SessionHost {
  send: SendFn;
  approvals: ApprovalBroker;
  tools: ToolBroker;
  onTurn(s: Session, running: boolean): void;
  onExit(s: Session, err: Error): void;
}

/** One logical session: protocol state, event numbering and the SessionSink for its driver. */
export class Session implements SessionSink {
  state: State = "closed";
  released = false;
  held: SessionSend[] = [];
  watching = false;
  running = false;
  /** Holds one of the node's max_active_sessions process slots. */
  slot = false;
  lastActivity = Date.now();
  crashes = 0;
  init: SessionInit | null = null;
  driver: Driver | null = null;
  absCwd = "";
  /** Set for sessions loaded from disk: the first send checks the transcript for duplicates. */
  checkTranscript = false;
  private lastEventNo = 0;

  constructor(public spec: SessionOpen, private readonly host: SessionHost) {}

  get id(): string {
    return this.spec.session_id;
  }

  get gen(): number {
    return this.spec.open_gen;
  }

  get idle(): boolean {
    return !this.driver?.busy && this.host.approvals.pendingFor(this.id).length === 0 && this.host.tools.pendingFor(this.id).length === 0;
  }

  setState(state: State, detail?: string): void {
    if (this.state === state && !detail) return;
    this.state = state;
    this.host.send("session.state", { session_id: this.id, open_gen: this.gen, state, ...(detail ? { detail } : {}) });
  }

  cancelPending(approvalReason: ApprovalCancel["reason"], toolReason: "interrupted" | "session_closed" | "process_restarted"): void {
    this.host.approvals.cancelSession(this.id, approvalReason);
    this.host.tools.cancelSession(this.id, toolReason);
  }

  record(): SessionRecord {
    return {
      session_id: this.id, open_gen: this.gen, kind: this.spec.runtime, state: this.state, released: this.released,
      sdk_session_id: this.spec.resume?.sdk_session_id ?? null, codex_thread_id: this.spec.resume?.codex_thread_id ?? null, spec: this.spec,
    };
  }

  // SessionSink

  event(fields: EventFields): void {
    this.lastActivity = Date.now();
    this.lastEventNo = Math.max(this.lastEventNo + 1, Date.now() * 1000);
    this.host.send("session.event", { session_id: this.id, open_gen: this.gen, event_no: this.lastEventNo, at: new Date().toISOString(), ...fields });
  }

  delta(text: string, turnId?: string): void {
    if (this.watching && text) this.host.send("stream.delta", { session_id: this.id, ...(turnId ? { turn_id: turnId } : {}), text });
  }

  turn(running: boolean): void {
    this.running = running;
    this.lastActivity = Date.now();
    this.host.onTurn(this, running);
  }

  requestApproval(ask: ApprovalAsk) {
    this.lastActivity = Date.now();
    this.event({ kind: "notification", message: `Waiting for approval: ${ask.tool_name}`, notification_type: "approval_wait" });
    return this.host.approvals.request({ ...ask, session_id: this.id, open_gen: this.gen });
  }

  cancelApproval(approvalId: string, reason: ApprovalCancel["reason"]): void {
    this.host.approvals.cancel(approvalId, reason);
  }

  callTool(tool: string, args: Record<string, unknown>, toolUseId?: string) {
    this.lastActivity = Date.now();
    return this.host.tools.call({ sessionId: this.id, openGen: this.gen, tool, args, cwd: this.absCwd, ...(toolUseId ? { toolUseId } : {}) });
  }

  exited(err: Error): void {
    this.host.onExit(this, err);
  }
}
