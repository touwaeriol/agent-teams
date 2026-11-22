// Live handoff digests (protocol.md session.digest): after each turn of a session the daemon reads
// its transcript again, without the model, and sends the console the digest when it changed, so
// the console always holds a recent one even if this node goes away.
import type { SessionOpen } from "../protocol.js";
import type { Logger } from "../util/log.js";

export interface LiveDigestDeps {
  digestOf: (runtime: SessionOpen["runtime"], resume: SessionOpen["resume"], absCwd: string) => string;
  send: (type: string, body: object) => void;
  log: Logger;
  /** Wait after a turn ends before reading (the runtime flushes its transcript), and between reads of a session. */
  delayMs?: number;
}

/** What LiveDigest needs to know about a session when its turn ends. */
export interface TurnEnd {
  id: string;
  gen: number;
  spec: SessionOpen;
  absCwd: string;
}

export class LiveDigest {
  private readonly timers = new Map<string, NodeJS.Timeout>();
  private readonly last = new Map<string, string>();

  constructor(private readonly d: LiveDigestDeps) {}

  /** A turn ended: send the digest once the transcript settled (one read per burst of turns). */
  turnEnded(s: TurnEnd): void {
    if (s.spec.runtime !== "claude" && s.spec.runtime !== "codex") return;
    if (this.timers.has(s.id)) return;
    const t = setTimeout(() => {
      this.timers.delete(s.id);
      this.report(s);
    }, this.d.delayMs ?? 3000);
    t.unref?.();
    this.timers.set(s.id, t);
  }

  /** The session is gone for good. */
  forget(sessionId: string): void {
    clearTimeout(this.timers.get(sessionId));
    this.timers.delete(sessionId);
    this.last.delete(sessionId);
  }

  stop(): void {
    for (const t of this.timers.values()) clearTimeout(t);
    this.timers.clear();
  }

  private report(s: TurnEnd): void {
    let digest: string;
    try {
      digest = this.d.digestOf(s.spec.runtime, s.spec.resume, s.absCwd);
    } catch (err) {
      this.d.log.warn({ session: s.id, err: (err as Error).message }, "session digest failed");
      return;
    }
    if (!digest || this.last.get(s.id) === digest) return;
    this.last.set(s.id, digest);
    this.d.send("session.digest", { session_id: s.id, open_gen: s.gen, digest });
  }
}
