import type { Logger } from "../util/log.js";
import { commandKey, isMessageType, isReliable, type Frame } from "../protocol.js";
import type { Inbox, Reply } from "./inbox.js";

export interface Sender {
  send(type: string, body: object): void;
  ackCommand(seq: number): void;
  sendError(refId: string | undefined, code: string, message: string): void;
}

export interface CommandHandler {
  /** Commands with the same lane run one after another (e.g. per session); no lane = run at once. */
  lane?: (body: Record<string, unknown>) => string | undefined;
  run: (body: Record<string, unknown>, frame: Frame) => Promise<Reply[]>;
  /** Runs as soon as a new command arrives, before it waits for its lane (session.close aborts the downloads of a session.send ahead of it). */
  onArrival?: (body: Record<string, unknown>) => void;
}

/** Types whose unfinished commands are re-run after a daemon restart (git jobs are idempotent). */
const RERUN_AFTER_RESTART = /^git\./;

/**
 * Applies console commands exactly as protocol.md §2.2 describes: duplicate keys re-send
 * the original replies, new commands are recorded and acknowledged, then executed in
 * their lane; replies are sent and remembered for 48 h.
 */
export class Dispatcher {
  private readonly lanes = new Map<string, Promise<void>>();

  constructor(
    private readonly link: Sender,
    private readonly inbox: Inbox,
    private readonly handlers: Partial<Record<string, CommandHandler>>,
    private readonly log: Logger,
  ) {}

  handle(frame: Frame): void {
    const handler = this.handlers[frame.type];
    if (!handler) return this.link.sendError(frame.id, "unsupported", `no handler for ${frame.type}`);
    if (!isMessageType(frame.type) || !isReliable(frame.type)) return void this.execute(handler, frame, null);
    this.handleReliable(handler, frame);
  }

  /** Re-runs commands an earlier process accepted but never finished. */
  resumeUnfinished(): void {
    for (const frame of this.inbox.unfinished()) {
      const key = commandKey(frame);
      const handler = this.handlers[frame.type];
      if (!handler || !RERUN_AFTER_RESTART.test(frame.type)) {
        this.inbox.forgetUnfinished(key);
        continue;
      }
      this.enqueue(handler, frame, key);
    }
  }

  private handleReliable(handler: CommandHandler, frame: Frame): void {
    const key = commandKey(frame);
    const replies = this.inbox.repliesFor(key);
    if (replies) {
      for (const r of replies) this.link.send(r.type, r.body);
      return this.ackSeq(frame.seq!);
    }
    if (this.inbox.isInFlight(key) || this.inbox.isDuplicateSeq(frame.seq!)) return this.ackSeq(frame.seq!);
    this.inbox.accept(frame, key);
    this.link.ackCommand(frame.seq!);
    this.enqueue(handler, frame, key);
  }

  private ackSeq(seq: number): void {
    this.inbox.noteSeq(seq);
    this.link.ackCommand(seq);
  }

  private enqueue(handler: CommandHandler, frame: Frame, key: string): void {
    try {
      handler.onArrival?.(frame.body);
    } catch (err) {
      this.log.warn({ type: frame.type, err: (err as Error).message }, "command arrival hook failed");
    }
    const lane = handler.lane?.(frame.body);
    if (!lane) return void this.execute(handler, frame, key);
    const prev = this.lanes.get(lane) ?? Promise.resolve();
    const next = prev.then(() => this.execute(handler, frame, key));
    this.lanes.set(lane, next);
    void next.finally(() => {
      if (this.lanes.get(lane) === next) this.lanes.delete(lane);
    });
  }

  private async execute(handler: CommandHandler, frame: Frame, key: string | null): Promise<void> {
    let replies: Reply[] = [];
    try {
      replies = await handler.run(frame.body, frame);
    } catch (err) {
      const code = errorCode(err);
      this.log.warn({ type: frame.type, code, err: (err as Error).message }, "command failed");
      this.link.sendError(frame.id, code, (err as Error).message);
    }
    for (const r of replies) this.link.send(r.type, r.body);
    if (key) this.inbox.finish(key, replies);
  }
}

const ERROR_CODES = new Set(["bad_schema", "unsupported", "unknown_session", "unknown_job", "busy"]);

function errorCode(err: unknown): string {
  const code = (err as { code?: unknown }).code;
  return typeof code === "string" && ERROR_CODES.has(code) ? code : "unsupported";
}
