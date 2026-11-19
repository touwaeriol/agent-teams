// Per-viewer conveniences kept in localStorage: unsent drafts and the chat scroll position,
// per conversation. Storage can be missing or throw (private windows, blocked site data);
// every access is guarded and the page works without it.
import { computed, onScopeDispose, ref, shallowReactive, toValue, watch, type MaybeRefOrGetter, type WritableComputedRef } from "vue";

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

// ---- Drafts ----
// One entry per viewer, project and conversation: localStorage[draft:v2:<user>:<pid>:<cid>] =
// {"t": text, "at": saved ms}. The latest text also lives in memory for the page, so a box that
// is remounted (switching threads remounts the whole board) reads it at once, ahead of the
// debounced write; pending writes go to storage when the key changes, the box unmounts or the
// page is hidden or closed. Entries older than DRAFT_TTL_MS are not restored and are swept.
// Old entries draft:<cid> (no user, no time) are moved into the new key the first time that
// conversation's box opens; the rest are swept DRAFT_TTL_MS after a page first saw them.

const DRAFT_V2 = "draft:v2:";
const LEGACY_SEEN = "draft-legacy-seen";
export const DRAFT_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const DRAFT_DEBOUNCE_MS = 250;

/** The storage key of a conversation's draft; undefined until the viewer and conversation are known. */
export function draftKey(user: string | undefined | null, pid: string | undefined | null, cid: string | undefined | null): string | undefined {
  return user && pid && cid ? `${DRAFT_V2}${user}:${pid}:${cid}` : undefined;
}

interface StoredDraft {
  t: string;
  at: number;
}

function parseDraft(raw: string | null): StoredDraft | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as StoredDraft;
    return v && typeof v.t === "string" && typeof v.at === "number" ? v : null;
  } catch {
    return null;
  }
}

const fresh = (d: StoredDraft, now: number) => now - d.at <= DRAFT_TTL_MS;

/** Latest text per key in this page (reactive: two boxes on one key show the same text). */
const memory = shallowReactive(new Map<string, string>());
const pending = new Map<string, ReturnType<typeof setTimeout>>();
let swept = false;
let listening = false;

function storageKeys(): string[] {
  try {
    const s = window.localStorage;
    return Array.from({ length: s.length }, (_, i) => s.key(i)).filter((k): k is string => k !== null);
  } catch {
    return [];
  }
}

/** Drops expired and unreadable drafts, and old-format ones once they have been around for the TTL. */
export function sweepDrafts(now = Date.now()): void {
  const keys = storageKeys().filter((k) => k.startsWith("draft:"));
  const legacy = keys.filter((k) => !k.startsWith(DRAFT_V2));
  for (const k of keys) {
    if (!k.startsWith(DRAFT_V2)) continue;
    const d = parseDraft(readLocal(k));
    if (!d || !fresh(d, now)) writeLocal(k, null);
  }
  if (legacy.length === 0) return writeLocal(LEGACY_SEEN, null);
  const seen = Number(readLocal(LEGACY_SEEN));
  if (!seen) return writeLocal(LEGACY_SEEN, String(now));
  if (now - seen <= DRAFT_TTL_MS) return;
  legacy.forEach((k) => writeLocal(k, null));
  writeLocal(LEGACY_SEEN, null);
}

function persistDraft(k: string): void {
  const t = pending.get(k);
  if (!t) return;
  clearTimeout(t);
  pending.delete(k);
  const v = memory.get(k) ?? "";
  writeLocal(k, v ? JSON.stringify({ t: v, at: Date.now() } satisfies StoredDraft) : null);
}

/** Writes every draft still waiting for its debounce (page hidden or closing). */
export function flushDrafts(): void {
  [...pending.keys()].forEach(persistDraft);
}

function setDraft(k: string, v: string): void {
  memory.set(k, v);
  const t = pending.get(k);
  if (t) clearTimeout(t);
  pending.delete(k);
  if (v === "") return writeLocal(k, null);
  pending.set(
    k,
    setTimeout(() => persistDraft(k), DRAFT_DEBOUNCE_MS),
  );
}

function loadDraft(k: string): string {
  const known = memory.get(k);
  if (known !== undefined) return known;
  if (!swept) {
    swept = true;
    sweepDrafts();
  }
  const now = Date.now();
  let v = "";
  const d = parseDraft(readLocal(k));
  if (d) v = fresh(d, now) ? d.t : "";
  else if (k.startsWith(DRAFT_V2)) {
    // An old-format draft of the same conversation: adopt it once.
    const old = `draft:${k.slice(k.lastIndexOf(":") + 1)}`;
    const raw = readLocal(old);
    if (raw) {
      v = raw;
      writeLocal(old, null);
      writeLocal(k, JSON.stringify({ t: v, at: now } satisfies StoredDraft));
    }
  }
  if (d && !v) writeLocal(k, null);
  memory.set(k, v);
  return v;
}

function listen(): void {
  if (listening || typeof window === "undefined") return;
  listening = true;
  window.addEventListener("pagehide", flushDrafts);
  window.addEventListener("beforeunload", flushDrafts);
  document.addEventListener("visibilitychange", () => document.visibilityState === "hidden" && flushDrafts());
}

/** Clears the draft under k when it is exactly v (a failed send put v back; a retry from the chat delivered it). */
export function clearDraftIf(k: string, v: string): void {
  if (loadDraft(k).trim() === v.trim()) setDraft(k, "");
}

/** Forgets the in-memory drafts (tests: a fresh page that only has localStorage). */
export function resetDraftMemory(): void {
  pending.forEach((t) => clearTimeout(t));
  pending.clear();
  memory.clear();
  swept = false;
}

/**
 * A text field's value that survives remounts and reloads under key (see draftKey). With no key
 * yet it is plain state; text typed then is kept when the key arrives (after any saved draft).
 */
export function useDraft(key: MaybeRefOrGetter<string | undefined>): WritableComputedRef<string> {
  listen();
  const local = ref("");
  const current = ref<string | undefined>();
  watch(
    () => toValue(key),
    (k, prev) => {
      if (prev) persistDraft(prev);
      const typed = current.value ? "" : local.value;
      current.value = k;
      local.value = "";
      if (!k) return;
      const saved = loadDraft(k);
      if (typed && typed !== saved) setDraft(k, saved ? `${saved}\n${typed}` : typed);
    },
    { immediate: true },
  );
  onScopeDispose(() => current.value && persistDraft(current.value));
  return computed({
    get: () => (current.value ? (memory.get(current.value) ?? "") : local.value),
    set: (v: string) => (current.value ? setDraft(current.value, v) : (local.value = v)),
  });
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
