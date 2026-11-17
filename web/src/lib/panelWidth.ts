// Width of the right-hand thread panel: dragged by the user, clamped, remembered per user
// in localStorage (which may be unavailable: private windows, blocked storage).

export const PANEL_DEFAULT = 440;
export const PANEL_MIN = 340;
export const PANEL_MAX = 760;

/** The panel never takes more than this share of the window, so the chat stays usable. */
const MAX_SHARE = 0.6;

export function clampWidth(width: number, viewport: number): number {
  const max = Math.max(PANEL_MIN, Math.min(PANEL_MAX, Math.floor(viewport * MAX_SHARE)));
  if (!Number.isFinite(width)) return Math.min(PANEL_DEFAULT, max);
  return Math.round(Math.min(max, Math.max(PANEL_MIN, width)));
}

export const panelKey = (userId: string) => `at:thread-panel-width:${userId}`;

export function loadWidth(userId: string, storage: Pick<Storage, "getItem"> | undefined = globalStorage()): number {
  try {
    const raw = storage?.getItem(panelKey(userId));
    const n = raw ? Number(raw) : NaN;
    return Number.isFinite(n) ? n : PANEL_DEFAULT;
  } catch {
    return PANEL_DEFAULT;
  }
}

export function saveWidth(userId: string, width: number, storage: Pick<Storage, "setItem" | "removeItem"> | undefined = globalStorage()): void {
  try {
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

// ── Thread detail mode ──────────────────────────────────────────────────────────────────
// With a thread open the panel fills the board and leaves the coordinator chat a narrow
// strip (like Claude Code's desktop app). The board is the area right of the project
// sidebar, measured live, so a collapsed sidebar gives the panel more room.

/** What stays of the coordinator chat when the detail panel is at its widest. */
export const COORDINATOR_PEEK = 80;
/** The detail panel never gets narrower than this when dragged. */
export const DETAIL_MIN = 420;
/** Below this the coordinator chat shows as the clickable strip instead of reflowing. */
export const COORDINATOR_COMPACT = 360;

/** Default (and widest) detail width: the whole board but the coordinator strip. */
export function detailDefault(board: number): number {
  return Math.max(0, Math.floor(board - COORDINATOR_PEEK));
}

/** Clamp a dragged / remembered width to [DETAIL_MIN, default]; `null` (or junk) follows the default. */
export function clampDetail(width: number | null, board: number): number {
  const max = detailDefault(board);
  if (width === null || !Number.isFinite(width)) return max;
  return Math.round(Math.min(max, Math.max(Math.min(DETAIL_MIN, max), width)));
}

export const detailKey = (userId: string) => `at:thread-detail-width:${userId}`;

/** The remembered detail width, or `null` for "fill the board" (also when nothing is stored or storage is blocked). */
export function loadDetail(userId: string, storage: Pick<Storage, "getItem"> | undefined = globalStorage()): number | null {
  try {
    const raw = storage?.getItem(detailKey(userId));
    const n = raw ? Number(raw) : NaN;
    return Number.isFinite(n) && n > 0 ? n : null;
  } catch {
    return null;
  }
}

/** Remember a detail width; at the default (widest) it is forgotten so a bigger screen fills up again. */
export function saveDetail(userId: string, width: number | null, board: number, storage: Pick<Storage, "setItem" | "removeItem"> | undefined = globalStorage()): void {
  try {
    if (width === null || width >= detailDefault(board)) storage?.removeItem(detailKey(userId));
    else storage?.setItem(detailKey(userId), String(Math.round(width)));
  } catch {
    // storage blocked: the width just is not remembered
  }
}
