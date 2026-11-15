// Replay (office-layout.md §5): a list of status events on a time axis drives
// the same director as live data. Pure helpers + a small clock hook.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toMs } from "./rules";
import type { EmployeeStatus, OfficeEmployee, OfficeStatusEvent } from "./types";

export const REPLAY_SPEEDS = [1, 4, 16] as const;

export function eventEmployee(e: OfficeStatusEvent): string {
  return typeof e.employee === "string" ? e.employee : e.employee.id;
}

export function eventTime(e: OfficeStatusEvent): number {
  return toMs(e.at) ?? 0;
}

export function sortEvents(events: OfficeStatusEvent[]): OfficeStatusEvent[] {
  return [...events].sort((a, b) => a.seq - b.seq);
}

/** Number of events (sorted by time) at or before `t`. Binary search. */
export function countUntil(sorted: OfficeStatusEvent[], t: number): number {
  let lo = 0;
  let hi = sorted.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (eventTime(sorted[mid]) <= t) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/** Latest status per employee: the roster's statuses overridden by events in order. */
export function foldStatuses(employees: OfficeEmployee[], events: OfficeStatusEvent[]): Map<string, EmployeeStatus> {
  const out = new Map<string, EmployeeStatus>();
  for (const e of employees) if (e.status) out.set(e.id, e.status);
  for (const ev of events) out.set(eventEmployee(ev), { since: ev.at, ...ev.status });
  return out;
}

export interface FeedCursor {
  count: number;
  lastSeq: number | undefined;
}

export type FeedChange = { kind: "append"; from: number } | { kind: "reset" } | { kind: "none" };

/**
 * How a new event list relates to what was already applied: an extension
 * (apply the tail with walking), unchanged, or anything else (rebuild and
 * teleport — e.g. the replay scrubber moved backwards).
 */
export function diffFeed(cursor: FeedCursor, events: OfficeStatusEvent[]): FeedChange {
  if (cursor.count === 0) return events.length ? { kind: "append", from: 0 } : { kind: "none" };
  if (events.length < cursor.count) return { kind: "reset" };
  if (events[cursor.count - 1]?.seq !== cursor.lastSeq) return { kind: "reset" };
  return events.length === cursor.count ? { kind: "none" } : { kind: "append", from: cursor.count };
}

export function cursorOf(events: OfficeStatusEvent[]): FeedCursor {
  return { count: events.length, lastSeq: events[events.length - 1]?.seq };
}

export interface Timeline {
  start: number;
  end: number;
}

export function timelineOf(events: OfficeStatusEvent[], from?: number, to?: number): Timeline {
  const times = events.map(eventTime);
  const start = from ?? (times.length ? Math.min(...times) : 0);
  const end = to ?? (times.length ? Math.max(...times) : start);
  return { start, end: Math.max(start, end) };
}

/** Advances a replay clock; stops at the end. */
export function stepClock(time: number, dtMs: number, speed: number, end: number): { time: number; done: boolean } {
  const next = time + dtMs * speed;
  return next >= end ? { time: end, done: true } : { time: next, done: false };
}

export interface ReplayState {
  time: number;
  start: number;
  end: number;
  playing: boolean;
  speed: number;
  /** Events at or before `time` — pass to <Office events>. */
  events: OfficeStatusEvent[];
  /** Changes on every scrub — pass to <Office seekKey> so people teleport. */
  seekKey: number;
  play(): void;
  pause(): void;
  seek(t: number): void;
  setSpeed(s: number): void;
}

/** Drives a replay of `events` between `from` and `to` (epoch ms). */
export function useReplay(events: OfficeStatusEvent[], from?: number, to?: number): ReplayState {
  const sorted = useMemo(() => [...events].sort((a, b) => eventTime(a) - eventTime(b) || a.seq - b.seq), [events]);
  const { start, end } = useMemo(() => timelineOf(sorted, from, to), [sorted, from, to]);
  const [time, setTime] = useState(start);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState<number>(1);
  const [seekKey, setSeekKey] = useState(0);
  const timeRef = useRef(time);
  timeRef.current = time;

  useEffect(() => {
    if (!playing) return;
    let last = performance.now();
    let raf = requestAnimationFrame(function frame(now) {
      const r = stepClock(timeRef.current, now - last, speed, end);
      last = now;
      setTime(r.time);
      if (r.done) return setPlaying(false);
      raf = requestAnimationFrame(frame);
    });
    return () => cancelAnimationFrame(raf);
  }, [playing, speed, end]);

  const seek = useCallback((t: number) => {
    setTime(Math.max(start, Math.min(end, t)));
    setSeekKey((k) => k + 1);
  }, [start, end]);
  const play = useCallback(() => {
    if (timeRef.current >= end) seek(start);
    setPlaying(true);
  }, [end, start, seek]);
  const pause = useCallback(() => setPlaying(false), []);
  const count = countUntil(sorted, time);
  const visible = useMemo(() => sorted.slice(0, count), [sorted, count]);
  return { time, start, end, playing, speed, events: visible, seekKey, play, pause, seek, setSpeed };
}
