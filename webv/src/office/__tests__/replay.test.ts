import { describe, expect, it } from "vitest";
import { countUntil, cursorOf, diffFeed, foldStatuses, stepClock, timelineOf } from "../replay";
import type { OfficeStatusEvent } from "../types";

const ev = (seq: number, at: number, employee: string, state: OfficeStatusEvent["status"]["state"]): OfficeStatusEvent => ({ seq, at, employee, status: { state } });
const events = [ev(1, 1000, "a", "typing"), ev(2, 2000, "b", "reading"), ev(3, 2000, "a", "idle"), ev(4, 5000, "b", "waiting")];

describe("replay", () => {
  it("counts events up to a time", () => {
    expect(countUntil(events, 0)).toBe(0);
    expect(countUntil(events, 1000)).toBe(1);
    expect(countUntil(events, 2500)).toBe(3);
    expect(countUntil(events, 9999)).toBe(4);
  });

  it("folds statuses over the roster", () => {
    const m = foldStatuses([{ id: "c", name: "c", tier: "member", role: "dev", status: { state: "offline" } }], events.slice(0, 3));
    expect(m.get("a")).toMatchObject({ state: "idle", since: 2000 });
    expect(m.get("b")!.state).toBe("reading");
    expect(m.get("c")!.state).toBe("offline");
  });

  it("diffs a feed: append, none, reset", () => {
    expect(diffFeed({ count: 0, lastSeq: undefined }, events)).toEqual({ kind: "append", from: 0 });
    const c = cursorOf(events.slice(0, 2));
    expect(diffFeed(c, events)).toEqual({ kind: "append", from: 2 });
    expect(diffFeed(c, events.slice(0, 2))).toEqual({ kind: "none" });
    expect(diffFeed(c, events.slice(0, 1))).toEqual({ kind: "reset" });
    expect(diffFeed(c, [events[0], events[2], events[3]])).toEqual({ kind: "reset" });
  });

  it("builds a timeline and steps the clock at speed", () => {
    expect(timelineOf(events)).toEqual({ start: 1000, end: 5000 });
    expect(timelineOf(events, 0, 9000)).toEqual({ start: 0, end: 9000 });
    expect(stepClock(1000, 100, 16, 5000)).toEqual({ time: 2600, done: false });
    expect(stepClock(4900, 100, 4, 5000)).toEqual({ time: 5000, done: true });
  });
});
