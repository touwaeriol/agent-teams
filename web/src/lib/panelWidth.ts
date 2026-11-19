// Width of the right-hand thread panel: one width for the thread list and an open thread, so
// opening or leaving a thread never moves the panel's edge. Dragged by the user, clamped to the
// board (the area right of the project sidebar, measured live) and remembered per user in
// localStorage (which may be unavailable: private windows, blocked storage).

export const PANEL_DEFAULT = 440;
export const PANEL_MIN = 340;
/** The list's expand button widens the panel to this (or as far as the board allows). */
export const PANEL_WIDE = 760;
/** What always stays of the coordinator chat: the panel never gets wider than the board less this. */
export const COORDINATOR_PEEK = 80;
/** Below this the coordinator chat shows as a clickable strip instead of reflowing. */
export const COORDINATOR_COMPACT = 360;

/** The widest the panel may be on this board: all of it but the coordinator strip. */
export function panelMax(board: number): number {
  return Math.max(PANEL_MIN, Math.floor(board - COORDINATOR_PEEK));
}

/** Clamp a dragged / remembered width to [PANEL_MIN, panelMax]; `null` and junk give the default. */
export function clampWidth(width: number | null, board: number): number {
  const max = panelMax(board);
  if (width === null || !Number.isFinite(width)) return Math.min(PANEL_DEFAULT, max);
  return Math.round(Math.min(max, Math.max(PANEL_MIN, width)));
}

/** The width that leaves the coordinator chat usable (not a strip), when the board allows it. */
export function roomForChat(width: number, board: number): number {
  return clampWidth(Math.min(width, board - COORDINATOR_COMPACT), board);
}

export const panelKey = (userId: string) => `at:thread-panel-width:${userId}`;
/** Where the separate open-thread width used to be kept; removed when the width is next saved. */
const oldDetailKey = (userId: string) => `at:thread-detail-width:${userId}`;

export function loadWidth(userId: string, storage: Pick<Storage, "getItem"> | undefined = globalStorage()): number {
  try {
    const raw = storage?.getItem(panelKey(userId));
    const n = raw ? Number(raw) : NaN;
    return Number.isFinite(n) && n > 0 ? n : PANEL_DEFAULT;
  } catch {
    return PANEL_DEFAULT;
  }
}

export function saveWidth(userId: string, width: number, storage: Pick<Storage, "setItem" | "removeItem"> | undefined = globalStorage()): void {
  try {
    storage?.removeItem(oldDetailKey(userId));
    if (width === PANEL_DEFAULT) storage?.removeItem(panelKey(userId));
    else storage?.setItem(panelKey(userId), String(Math.round(width)));
  } catch {
    // storage blocked: the width just is not remembered
  }
}

/** Dragging the handle left widens the panel (it sits on the right); the caller clamps. */
export function dragWidth(startWidth: number, startX: number, x: number): number {
  return startWidth + (startX - x);
}

function globalStorage(): Storage | undefined {
  try {
    return typeof window === "undefined" ? undefined : window.localStorage;
  } catch {
    return undefined;
  }
}
