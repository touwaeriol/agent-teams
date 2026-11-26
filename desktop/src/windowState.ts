// Saved window size and position, kept only when it is still on a connected display. Pure.

export interface Bounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface WindowState {
  bounds?: Bounds;
  maximized?: boolean;
}

export const DEFAULT_SIZE = { width: 1280, height: 820 };
export const MIN_SIZE = { width: 480, height: 360 };

const isNum = (n: unknown): n is number => typeof n === "number" && Number.isFinite(n);

/** Parsed contents of the state file; anything malformed becomes {}. */
export function parseWindowState(text: string | null): WindowState {
  if (!text) return {};
  try {
    const raw = JSON.parse(text) as { bounds?: Partial<Bounds>; maximized?: unknown };
    const b = raw?.bounds;
    const bounds = b && isNum(b.x) && isNum(b.y) && isNum(b.width) && isNum(b.height) ? { x: b.x, y: b.y, width: b.width, height: b.height } : undefined;
    return { bounds, maximized: raw?.maximized === true };
  } catch {
    return {};
  }
}

const overlap = (a: Bounds, b: Bounds) =>
  Math.max(0, Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)) * Math.max(0, Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y));

/** The bounds to open with: the saved ones if a good part (a quarter) is on some display, else only the default size. */
export function restoreBounds(saved: Bounds | undefined, workAreas: readonly Bounds[]): Partial<Bounds> & { width: number; height: number } {
  if (!saved || saved.width < MIN_SIZE.width || saved.height < MIN_SIZE.height) return { ...DEFAULT_SIZE };
  const visible = workAreas.some((area) => overlap(saved, area) >= (saved.width * saved.height) / 4);
  return visible ? { ...saved } : { width: saved.width, height: saved.height };
}
