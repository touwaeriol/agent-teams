// Per-viewer conveniences kept in localStorage: unsent drafts and the chat scroll position,
// per conversation. Storage can be missing or throw (private windows, blocked site data);
// every access is guarded and the page works without it.
import { useCallback, useEffect, useRef, useState } from "react";

export function readLocal(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeLocal(key: string, value: string | null): void {
  try {
    if (value === null || value === "") window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value);
  } catch {
    // storage unavailable: nothing to keep
  }
}

/** A text field's value that survives reloads under key (no key: plain state). */
export function useDraft(key: string | undefined): [string, (v: string) => void] {
  const [text, setText] = useState(() => (key ? (readLocal(key) ?? "") : ""));
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    setText(key ? (readLocal(key) ?? "") : "");
  }, [key]);
  const set = useCallback(
    (v: string) => {
      setText(v);
      if (!key) return;
      if (timer.current) clearTimeout(timer.current);
      if (v === "") writeLocal(key, null);
      else timer.current = setTimeout(() => writeLocal(key, v), 250);
    },
    [key],
  );
  return [text, set];
}

/** Where a chat was scrolled to: the offset from the bottom (0 = following the newest message). */
export interface ScrollMark {
  fromBottom: number;
}

export function readScroll(cid: string): ScrollMark | null {
  const raw = readLocal(`chat-scroll:${cid}`);
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as ScrollMark;
    return typeof v.fromBottom === "number" && v.fromBottom >= 0 ? v : null;
  } catch {
    return null;
  }
}

/** Near the bottom counts as following (new messages keep scrolling into view). */
export const FOLLOW_PX = 120;

export function scrollMarkOf(el: { scrollTop: number; scrollHeight: number; clientHeight: number }): ScrollMark {
  const fromBottom = Math.max(0, Math.round(el.scrollHeight - el.clientHeight - el.scrollTop));
  return { fromBottom: fromBottom < FOLLOW_PX ? 0 : fromBottom };
}

export function writeScroll(cid: string, mark: ScrollMark): void {
  writeLocal(`chat-scroll:${cid}`, mark.fromBottom === 0 ? null : JSON.stringify(mark));
}

/** scrollTop that puts a saved mark back (clamped to the content). */
export function restoredTop(mark: ScrollMark | null, el: { scrollHeight: number; clientHeight: number }): number {
  const max = Math.max(0, el.scrollHeight - el.clientHeight);
  if (!mark) return max;
  return Math.max(0, Math.min(max, max - mark.fromBottom));
}

/** The nearest scrolling ancestor (the chat column scrolls inside its page). */
export function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let p = el?.parentElement ?? null; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY;
    if (o === "auto" || o === "scroll") return p;
  }
  return null;
}
