// The "交付物" strip at the top of the panel list: which deliverables it shows, which of them are
// new to this viewer, and the scroll arrows. Pure helpers plus a small per-project seen store.
import { ref } from "vue";
import type { DeliverableView } from "@/api/views";
import { isHosted } from "./artifacts";
import { extensionOf } from "./library";
import { readLocal, writeLocal } from "./persist";

/**
 * Hosted renders that read as a document or a picture: web pages (html/htm), Markdown (md),
 * PDF, images (png/jpg/gif/webp/svg). "text" counts only for .txt: raw .json/.csv data files
 * (also rendered as text) belong in the library, not the strip.
 */
const DOC_RENDERS = new Set(["html", "markdown", "pdf", "image"]);

/**
 * Whether the strip shows a deliverable. Only documents the viewer opens in the console:
 * - kept: files published with publish_file (detail.hosted) rendered as html / markdown / pdf /
 *   image / .txt text, whatever their kind (design, doc, slides, dashboard, board, data);
 *   and kind=design links, which open in the in-app viewer.
 * - dropped: kind=pr (PRs show on the thread instead: ThreadRow's PR icon, the thread's 交付 lines),
 *   plain external links registered with publish_deliverable (doc/slides/dashboard/board/data or
 *   unknown kinds with only a URL), and hosted raw data files (.json/.csv).
 * The server no longer creates kind=pr; this also guards against old cache entries and events.
 */
export function isStripDeliverable(d: Pick<DeliverableView, "kind" | "detail">): boolean {
  if (d.kind === "pr") return false;
  if (isHosted(d)) {
    const render = d.detail.render ?? "";
    if (DOC_RENDERS.has(render)) return true;
    return render === "text" && extensionOf(String(d.detail.entry ?? "")) === "txt";
  }
  return d.kind === "design";
}

/**
 * What this browser has seen of a project's deliverables: since = when the strip was first used
 * here (anything updated before counts as seen), seen = per deliverable, the time it was opened.
 */
export interface SeenRecord {
  since: string;
  seen: Record<string, string>;
}

const MAX_SEEN = 300;

const time = (iso: string | undefined) => {
  const t = iso ? Date.parse(iso) : NaN;
  return Number.isNaN(t) ? 0 : t;
};

/**
 * New = updated after the viewer last opened it (or after `since` when never opened).
 * Without a record (first use) nothing is new: the store starts at "now" instead of
 * flagging the whole history.
 */
export function isNewDeliverable(d: Pick<DeliverableView, "id" | "updated_at">, rec: SeenRecord | null | undefined): boolean {
  if (!rec) return false;
  const seenAt = Math.max(time(rec.since), time(rec.seen[d.id]));
  return time(d.updated_at) > seenAt;
}

/** The record after opening d: seen at now, or at its updated_at when the server clock is ahead. */
export function markSeen(rec: SeenRecord | null | undefined, d: Pick<DeliverableView, "id" | "updated_at">, now = Date.now()): SeenRecord {
  const base = rec ?? { since: new Date(now).toISOString(), seen: {} };
  const at = new Date(Math.max(now, time(d.updated_at))).toISOString();
  const seen = { ...base.seen, [d.id]: at };
  const ids = Object.keys(seen);
  if (ids.length > MAX_SEEN) {
    // forget the oldest; they fall back to `since`, which they predate unless updated again
    ids.sort((a, b) => time(seen[a]) - time(seen[b]));
    for (const id of ids.slice(0, ids.length - MAX_SEEN)) delete seen[id];
  }
  return { since: base.since, seen };
}

export function parseSeen(raw: string | null): SeenRecord | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as Partial<SeenRecord>;
    if (typeof v?.since !== "string") return null;
    const seen = v.seen && typeof v.seen === "object" ? Object.fromEntries(Object.entries(v.seen).filter(([, t]) => typeof t === "string")) : {};
    return { since: v.since, seen: seen as Record<string, string> };
  } catch {
    return null;
  }
}

const seenKey = (pid: string) => `deliverables.seen.${pid}`;
/** Records loaded so far, shared by the strip and the viewer so a mark shows everywhere at once. */
const records = ref<Record<string, SeenRecord>>({});

function load(pid: string): SeenRecord {
  const cached = records.value[pid];
  if (cached) return cached;
  let rec = parseSeen(readLocal(seenKey(pid)));
  if (!rec) {
    rec = { since: new Date().toISOString(), seen: {} };
    writeLocal(seenKey(pid), JSON.stringify(rec));
  }
  records.value = { ...records.value, [pid]: rec };
  return rec;
}

/** "新内容" state of a project's deliverables in this browser (localStorage, per project). */
export function useSeenDeliverables(pid: () => string) {
  return {
    isNew: (d: Pick<DeliverableView, "id" | "updated_at">) => isNewDeliverable(d, load(pid())),
    markSeen: (d: Pick<DeliverableView, "id" | "updated_at">) => {
      const p = pid();
      if (!p) return;
      const rec = markSeen(load(p), d);
      records.value = { ...records.value, [p]: rec };
      writeLocal(seenKey(p), JSON.stringify(rec));
    },
  };
}

/** Test hook: forget the loaded records (storage stays). */
export function resetSeenCache(): void {
  records.value = {};
}

/** Which scroll arrows a horizontal scroller needs (1px slack for fractional widths). */
export function scrollEdges(scrollLeft: number, clientWidth: number, scrollWidth: number): { left: boolean; right: boolean } {
  return { left: scrollLeft > 1, right: scrollLeft + clientWidth < scrollWidth - 1 };
}
