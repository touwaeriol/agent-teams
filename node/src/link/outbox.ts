import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { newId } from "../util/ids.js";
import { appendJsonl, readJsonFile, readJsonl, writeFileAtomic, writeJsonlAtomic } from "../util/jsonl.js";
import type { Frame } from "../protocol.js";

interface Cursor {
  acked: number;
}

/**
 * Daemon outbox (protocol.md §2.1): every reliable frame is appended to
 * state/outbox/<epoch>.jsonl before it is sent and stays pending until the console's
 * cumulative ACK covers its seq. seq continues from the file after a restart; the epoch
 * only changes when the state directory is wiped.
 */
export class Outbox {
  readonly epoch: string;
  private lastSeq = 0;
  private acked = 0;
  private pending: Frame[] = [];
  private readonly dir: string;
  private readonly file: string;

  constructor(stateDir: string) {
    this.dir = join(stateDir, "outbox");
    mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    this.epoch = this.loadEpoch();
    this.file = join(this.dir, `${this.epoch}.jsonl`);
    this.load();
  }

  get lastSentSeq(): number {
    return this.lastSeq;
  }

  get ackedSeq(): number {
    return this.acked;
  }

  /** Persists a reliable frame and returns it (with seq) for sending. */
  append(type: string, body: object): Frame {
    const frame: Frame = { v: 2, type, id: newId("n_"), seq: ++this.lastSeq, ts: new Date().toISOString(), body: body as Record<string, unknown> };
    appendJsonl(this.file, frame);
    this.pending.push(frame);
    return frame;
  }

  /** Cumulative ACK from the console. */
  ack(seq: number): void {
    if (seq <= this.acked) return;
    this.acked = Math.min(seq, this.lastSeq);
    this.pending = this.pending.filter((f) => f.seq! > this.acked);
    writeFileAtomic(join(this.dir, "cursor.json"), JSON.stringify({ acked: this.acked } satisfies Cursor));
    if (this.pending.length === 0) this.compact();
  }

  /** Frames not yet acknowledged, in seq order. */
  unacked(): Frame[] {
    return [...this.pending];
  }

  /**
   * Drops pending frames for which `replace` returns a substitute list (e.g. an
   * approval.request whose Promise died with the previous process becomes an
   * approval.cancel). Substitutes get fresh seqs.
   */
  rewritePending(replace: (f: Frame) => Array<{ type: string; body: object }> | null): void {
    const substitutes: Array<{ type: string; body: object }> = [];
    const keep: Frame[] = [];
    for (const f of this.pending) {
      const sub = replace(f);
      if (sub === null) keep.push(f);
      else substitutes.push(...sub);
    }
    if (keep.length === this.pending.length) return;
    this.pending = keep;
    writeJsonlAtomic(this.file, this.pending);
    for (const s of substitutes) this.append(s.type, s.body);
  }

  private compact(): void {
    writeJsonlAtomic(this.file, []);
  }

  private loadEpoch(): string {
    const path = join(this.dir, "epoch");
    if (existsSync(path)) return readFileSync(path, "utf8").trim();
    const epoch = newId("").toString();
    for (const f of readdirSync(this.dir)) rmSync(join(this.dir, f), { force: true });
    writeFileAtomic(path, epoch + "\n");
    return epoch;
  }

  private load(): void {
    const frames = readJsonl<Frame>(this.file);
    const cursor = readJsonFile<Cursor>(join(this.dir, "cursor.json"), { acked: 0 });
    this.acked = cursor.acked;
    this.lastSeq = Math.max(cursor.acked, ...frames.map((f) => f.seq ?? 0));
    this.pending = frames.filter((f) => (f.seq ?? 0) > this.acked);
  }
}
