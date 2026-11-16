// What voice input does to the message box: speech goes in where the cursor was (replacing a
// selection). Each utterance is a segment keyed by OpenAI's item_id; deltas grow it (shown grey),
// the completed transcript replaces it (shown normally). The box's value is always
// before + segments + after, so the text can be sent at any moment.

export interface Segment {
  id: string;
  text: string;
  final: boolean;
}

export interface Part {
  text: string;
  partial: boolean;
}

const WORDY = /[\p{L}\p{N}]/u;
const ASCII_END = /[A-Za-z0-9.,!?;:)"']$/;
const ASCII_START = /^[A-Za-z0-9("']/;

/** Space between two pieces when both sides are Latin script (none for Chinese/Japanese). */
export function joiner(prev: string, next: string): string {
  if (!prev || !next || /\s$/.test(prev) || /^\s/.test(next)) return "";
  return ASCII_END.test(prev) && ASCII_START.test(next) ? " " : "";
}

export class Transcript {
  before = "";
  after = "";
  segments: Segment[] = [];

  /** Starts inserting into text at [start, end) (the selection is replaced). */
  begin(text: string, start = text.length, end = start): void {
    const s = Math.max(0, Math.min(start, text.length));
    const e = Math.max(s, Math.min(end, text.length));
    this.before = text.slice(0, s);
    this.after = text.slice(e);
    this.segments = [];
  }

  private seg(id: string): Segment {
    let s = this.segments.find((x) => x.id === id);
    if (!s) {
      s = { id, text: "", final: false };
      this.segments.push(s);
    }
    return s;
  }

  /** input_audio_buffer.committed: keeps utterances in the order they were spoken. */
  committed(id: string): void {
    this.seg(id);
  }

  delta(id: string, delta: string): void {
    const s = this.seg(id);
    if (!s.final) s.text += delta;
  }

  complete(id: string, transcript: string): void {
    const s = this.seg(id);
    s.text = transcript;
    s.final = true;
  }

  /** A failed or abandoned utterance: drop it. */
  drop(id: string): void {
    this.segments = this.segments.filter((s) => s.id !== id);
  }

  /** Keeps whatever partial text there is as final (stop timed out, connection lost). */
  settle(): void {
    for (const s of this.segments) s.final = true;
  }

  /** Throws away partial text (cancel). */
  dropPartials(): void {
    this.segments = this.segments.filter((s) => s.final);
  }

  get pending(): boolean {
    return this.segments.some((s) => !s.final);
  }

  /** before, each segment, after: with the spaces the joins need. */
  get parts(): Part[] {
    const out: Part[] = [{ text: this.before, partial: false }];
    let tail = this.before;
    for (const s of this.segments) {
      const text = tail && !/\s$/.test(tail) ? s.text : s.text.trimStart();
      if (!text) continue;
      const piece = joiner(tail, text) + text;
      out.push({ text: piece, partial: !s.final });
      tail += piece;
    }
    const after = this.after && WORDY.test(this.after[0] ?? "") ? joiner(tail, this.after) + this.after : this.after;
    out.push({ text: after, partial: false });
    return out.filter((p) => p.text);
  }

  get value(): string {
    return this.parts.map((p) => p.text).join("");
  }

  /** Cursor position right after the spoken text. */
  get caret(): number {
    return this.value.length - this.after.length;
  }
}
