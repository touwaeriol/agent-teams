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

/** Dragging the handle left widens the panel (it sits on the right). */
export function dragWidth(startWidth: number, startX: number, x: number, viewport: number): number {
  return clampWidth(startWidth + (startX - x), viewport);
}

function globalStorage(): Storage | undefined {
  try {
    return typeof window === "undefined" ? undefined : window.localStorage;
  } catch {
    return undefined;
  }
}
