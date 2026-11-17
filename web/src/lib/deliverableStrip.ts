// The "交付物" strip at the top of the panel list: which deliverables it shows, which of them are
// new to this viewer, and the scroll arrows. Pure helpers plus a small per-project seen store.
import { ref, watch } from "vue";
import type { DeliverableView } from "@/api/views";
import { isHosted } from "./artifacts";
import { extensionOf, isImageDeliverable } from "./library";
import { readLocal, writeLocal } from "./persist";

/**
 * Hosted renders that read as a document: web pages (html/htm), Markdown (md), PDF. "text"
 * counts only for .txt: raw .json/.csv data files (also rendered as text) belong in the
 * library, not the strip. Images are not deliverables (see isImageDeliverable).
 */
const DOC_RENDERS = new Set(["html", "markdown", "pdf"]);

/**
 * Whether the strip shows a deliverable. Only documents the viewer opens in the console:
 * - kept: files published with publish_file (detail.hosted) rendered as html / markdown / pdf /
 *   .txt text, whatever their kind (design, doc, slides, dashboard, board, data);
 *   and kind=design links, which open in the in-app viewer.
 * - dropped: hosted images (png/jpg/jpeg/gif/webp/svg, by render, content type or extension;
 *   also when kind=design): screenshots go into a report document, older image deliverables
 *   stay in the data but are not shown; kind=pr (PRs show on the thread instead: ThreadRow's PR icon, the thread's 交付 lines),
 *   plain external links registered with publish_deliverable (doc/slides/dashboard/board/data or
 *   unknown kinds with only a URL), and hosted raw data files (.json/.csv).
 * The server no longer creates kind=pr; this also guards against old cache entries and events.
 */
export function isStripDeliverable(d: Pick<DeliverableView, "kind" | "detail">): boolean {
  if (d.kind === "pr") return false;
  if (isImageDeliverable(d)) return false;
  if (isHosted(d)) {
    const render = d.detail.render ?? "";
    if (DOC_RENDERS.has(render)) return true;
    return render === "text" && extensionOf(String(d.detail.entry ?? "")) === "txt";
  }
  return d.kind === "design";
}

/**
 * What this browser has seen of a project's deliverables: since = anything updated before it counts
 * as seen (first: when the strip was first used here), seen = per deliverable, the time it was opened.
 *
 * Times are the browser's clock compared with the server's updated_at. markSeen takes the later of
 * the two, so opening a deliverable always clears it; but `since` starts at the browser's "now":
 * with a browser clock behind the server, deliverables updated just before first use can show as
 * new once, and with a clock ahead, ones updated just after can be missed. Good enough for a hint.
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
  let since = base.since;
  if (ids.length > MAX_SEEN) {
    // forget the oldest entries and move `since` up to the latest of them, so a forgotten one
    // stays seen (unless updated again). Never-opened ones updated before that time also count as
    // seen from then on: after 300 opens that is an acceptable loss for a bounded record.
    ids.sort((a, b) => time(seen[a]) - time(seen[b]));
    for (const id of ids.slice(0, ids.length - MAX_SEEN)) {
      if (time(seen[id]) > time(since)) since = seen[id];
      delete seen[id];
    }
  }
  return { since, seen };
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

/**
 * "新内容" state of a project's deliverables in this browser (localStorage, per project). The record
 * is loaded (and created on first use) when the project is known, not while rendering; no project,
 * nothing is written.
 */
export function useSeenDeliverables(pid: () => string) {
  watch(pid, (p) => p && load(p), { immediate: true });
  return {
    isNew: (d: Pick<DeliverableView, "id" | "updated_at">) => isNewDeliverable(d, records.value[pid()]),
    markSeen: (d: Pick<DeliverableView, "id" | "updated_at">) => {
      const p = pid();
      if (!p) return;
      const rec = markSeen(records.value[p] ?? load(p), d);
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
