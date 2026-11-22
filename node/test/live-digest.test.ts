import { afterEach, describe, expect, it, vi } from "vitest";
import { LiveDigest } from "../src/move/live.js";
import type { SessionOpen } from "../src/protocol.js";

const log = { info: () => {}, warn: () => {}, error: () => {}, debug: () => {}, child() { return this; } } as never;
const spec = { session_id: "s1", open_gen: 2, runtime: "claude", cwd: "w", resume: { sdk_session_id: "abc" } } as unknown as SessionOpen;

describe("LiveDigest", () => {
  afterEach(() => vi.useRealTimers());

  it("sends the digest once per burst of turns and only when it changed", () => {
    vi.useFakeTimers();
    let text = "待办 v1";
    const reads: string[] = [];
    const sent: object[] = [];
    const live = new LiveDigest({ digestOf: (_r, resume, cwd) => { reads.push(`${resume?.sdk_session_id}@${cwd}`); return text; }, send: (type, body) => sent.push({ type, body }), log, delayMs: 1000 });
    const s = { id: "s1", gen: 2, spec, absCwd: "/root/w" };
    live.turnEnded(s);
    live.turnEnded(s);
    vi.advanceTimersByTime(1000);
    expect(reads).toEqual(["abc@/root/w"]);
    expect(sent).toEqual([{ type: "session.digest", body: { session_id: "s1", open_gen: 2, digest: "待办 v1" } }]);
    live.turnEnded(s);
    vi.advanceTimersByTime(1000);
    expect(sent).toHaveLength(1); // unchanged
    text = "待办 v2";
    live.turnEnded(s);
    vi.advanceTimersByTime(1000);
    expect(sent).toHaveLength(2);
    text = "";
    live.turnEnded(s);
    vi.advanceTimersByTime(1000);
    expect(sent).toHaveLength(2); // no transcript: nothing
  });

  it("drops a pending read when the session goes away and skips other runtimes", () => {
    vi.useFakeTimers();
    const sent: object[] = [];
    const live = new LiveDigest({ digestOf: () => "x", send: (_t, b) => sent.push(b), log, delayMs: 1000 });
    live.turnEnded({ id: "s1", gen: 1, spec, absCwd: "/w" });
    live.forget("s1");
    live.turnEnded({ id: "s2", gen: 1, spec: { ...spec, runtime: "antigravity" } as unknown as SessionOpen, absCwd: "/w" });
    vi.advanceTimersByTime(5000);
    expect(sent).toEqual([]);
  });
});
