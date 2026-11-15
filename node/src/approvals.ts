import type { ApprovalCancel, ApprovalRequest, ApprovalResolve } from "./protocol.js";
import { deferred, type Deferred } from "./util/async.js";

export type SendFn = (type: string, body: object) => void;
export type CancelReason = ApprovalCancel["reason"];

interface Pending {
  sessionId: string;
  wait: Deferred<ApprovalResolve>;
}

export class ApprovalCancelled extends Error {
  constructor(readonly reason: CancelReason) {
    super(`approval cancelled: ${reason}`);
  }
}

/**
 * Pending approvals (protocol.md §5): one Promise per approval_id, resolved by
 * approval.resolve (→ approval.ack) or cancelled (→ approval.cancel).
 */
export class ApprovalBroker {
  private readonly pending = new Map<string, Pending>();

  constructor(private readonly send: SendFn) {}

  request(req: ApprovalRequest): Promise<ApprovalResolve> {
    const existing = this.pending.get(req.approval_id);
    if (existing) return existing.wait.promise;
    const wait = deferred<ApprovalResolve>();
    this.pending.set(req.approval_id, { sessionId: req.session_id, wait });
    this.send("approval.request", req);
    return wait.promise;
  }

  /** Handles approval.resolve; returns the approval.ack body. */
  resolve(body: ApprovalResolve): { approval_id: string; session_id?: string; stale?: boolean } {
    const p = this.pending.get(body.approval_id);
    if (!p) return { approval_id: body.approval_id, stale: true };
    this.pending.delete(body.approval_id);
    p.wait.resolve(body);
    return { approval_id: body.approval_id, session_id: p.sessionId };
  }

  cancel(approvalId: string, reason: CancelReason): void {
    const p = this.pending.get(approvalId);
    if (!p) return;
    this.pending.delete(approvalId);
    this.send("approval.cancel", { approval_id: approvalId, session_id: p.sessionId, reason });
    p.wait.reject(new ApprovalCancelled(reason));
  }

  cancelSession(sessionId: string, reason: CancelReason): void {
    for (const id of this.pendingFor(sessionId)) this.cancel(id, reason);
  }

  pendingFor(sessionId: string): string[] {
    return [...this.pending].filter(([, p]) => p.sessionId === sessionId).map(([id]) => id);
  }
}
