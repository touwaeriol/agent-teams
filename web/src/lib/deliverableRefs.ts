// Deliverable ids mentioned in a message's text ("交付物 dl_01m8…"): which ones to look up and
// show as cards under the message. Only ids the server returns for the current project (it
// checks the user owns it) become cards; unknown or foreign ids stay plain text.
import type { DeliverableView } from "@/api/views";

/** A deliverable id as the server makes them: "dl_" + a lowercase ULID (Crockford base32, 26 chars),
 * not inside a longer word. The id is group 2: group 1 is the start or the character before it (no
 * lookbehind, which Safari before 16.4 cannot parse). */
const ID = /(^|[^0-9A-Za-z_])(dl_[0-9a-hjkmnp-tv-z]{26})(?![0-9A-Za-z_])/g;

/** At most this many ids are looked up per message (the server accepts 50). */
export const MAX_MENTIONED = 12;

/** Valid deliverable ids in text, unique, in order of first mention. */
export function deliverableIdsIn(text: string | null | undefined, max = MAX_MENTIONED): string[] {
  const out: string[] = [];
  for (const m of (text ?? "").matchAll(ID)) {
    if (!out.includes(m[2])) out.push(m[2]);
    if (out.length >= max) break;
  }
  return out;
}

/**
 * The mentioned deliverables the server returned, in mention order. Anything not asked for is
 * dropped too (a stale cache entry or a server that ignored the filter must not add cards).
 */
export function mentionedDeliverables<T extends Pick<DeliverableView, "id">>(ids: string[], found: T[] | undefined): T[] {
  const byId = new Map((found ?? []).map((d) => [d.id, d]));
  return ids.flatMap((id) => {
    const d = byId.get(id);
    return d ? [d] : [];
  });
}
