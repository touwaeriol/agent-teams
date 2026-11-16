// Employee looks (office-layout.md §4.6): resolve the look, then compose
// character frames into pixel lists and cache them as small canvases.
import { grayscale as toGray, resolveColor, shadeColor, type ColorContext } from "./color";
import { instanceOf } from "./compile";
import type { Cell, Character, Look, LookPiece, OfficeEmployee, OfficeLayout } from "./types";

export interface ResolvedLook {
  look: Look;
  hair: string;
  skin: string;
  /** Slot values: color, tag, hair, skin, shirt, accent, pants. */
  slots: Record<string, string>;
  /** Character colour-key overrides (single-character keys such as S or C). */
  keyColors: Record<string, string>;
  patch: Record<string, Record<string, string>>;
  cells: Cell[];
  hash: string;
}

const DEFAULT_SKIN = "#F1C9A5";

export type LookSubject = Pick<OfficeEmployee, "id" | "name" | "tier" | "role" | "look" | "instance">;

/** Fills the look: employee → defaults[role] → defaults[tier] → defaults["*"]. */
export function baseLook(layout: OfficeLayout, e: LookSubject): Look & { hairExplicit: boolean } {
  const d = layout.looks.defaults;
  const chain = [e.look ?? {}, d[e.role] ?? {}, d[e.tier] ?? {}, d["*"]] as Partial<Look>[];
  const pick = <K extends keyof Look>(k: K): Look[K] | undefined => chain.find((l) => l[k] !== undefined && l[k] !== null)?.[k];
  const hair = pick("hair");
  return {
    color: pick("color") ?? "#4A4843",
    outfit: validKey(pick("outfit"), layout.looks.outfits, "tee"),
    accessory: validKey(pick("accessory"), layout.looks.accessories, "none"),
    hair: hair ?? null,
    hairExplicit: Boolean(hair),
  };
}

function validKey(v: string | undefined, table: Record<string, unknown>, fallback: string): string {
  return v && v in table ? v : fallback;
}

export function resolveLook(layout: OfficeLayout, e: LookSubject): ResolvedLook {
  const looks = layout.looks;
  const base = baseLook(layout, e);
  const n = Math.max(0, instanceOf(e as OfficeEmployee) - 1);
  const hair = base.hair ?? variedHair(layout, e, n);
  const skin = looks.skin?.[0] ?? DEFAULT_SKIN;
  const outfit = looks.outfits[base.outfit];
  const accessory = looks.accessories[base.accessory];
  const slots: Record<string, string> = { color: base.color, tag: base.color, hair, skin };
  const keyColors: Record<string, string> = {};
  applyPieceColors(outfit, slots, keyColors, layout.palette);
  applyPieceColors(accessory, slots, keyColors, layout.palette);
  const shade = looks.variation?.shirtShade?.[n % Math.max(1, looks.variation?.shirtShade?.length ?? 1)] ?? 0;
  if (shade && slots.shirt) slots.shirt = shadeColor(slots.shirt, shade);
  const look = { color: base.color, outfit: base.outfit, accessory: base.accessory, hair };
  return {
    look,
    hair,
    skin,
    slots,
    keyColors,
    patch: mergePatches(outfit?.patch, accessory?.patch),
    cells: [...(outfit?.cells ?? []), ...(accessory?.cells ?? [])],
    hash: JSON.stringify([slots, keyColors, base.outfit, base.accessory]),
  };
}

function variedHair(layout: OfficeLayout, e: LookSubject, n: number): string {
  const list = layout.looks.hair;
  const mode = layout.looks.variation?.hair ?? "byInstance";
  if (mode === "fixed") return list[0];
  if (mode === "random") return list[hashString(e.id) % list.length];
  return list[n % list.length];
}

function applyPieceColors(piece: LookPiece | undefined, slots: Record<string, string>, keys: Record<string, string>, palette?: Record<string, string>): void {
  for (const [k, expr] of Object.entries(piece?.colors ?? {})) {
    const value = resolveColor(expr, { palette, slots });
    if (!value) continue;
    if (k.length === 1) keys[k] = value;
    else slots[k] = value;
  }
}

function mergePatches(...patches: (LookPiece["patch"] | undefined)[]): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {};
  for (const p of patches) for (const [part, rows] of Object.entries(p ?? {})) out[part] = { ...(out[part] ?? {}), ...rows };
  return out;
}

export function hashString(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

export interface Pixel {
  x: number;
  y: number;
  color: string;
}

export interface ComposeOptions {
  overlay?: string;
  grayscale?: boolean;
}

/** Rows of a frame (parts stacked top to bottom), with the look's patches applied. */
export function frameRows(ch: Character, frame: string, patch: Record<string, Record<string, string>>): string[] {
  const parts = ch.frames[frame] ?? ch.frames.stand ?? [];
  return parts.flatMap((name) => (ch.parts[name] ?? []).map((row, i) => patch[name]?.[String(i)] ?? row));
}

/** Composes one frame (+ optional overlay) into art-pixel coordinates. */
export function composeFrame(layout: OfficeLayout, look: ResolvedLook, frame: string, opts: ComposeOptions = {}): Pixel[] {
  const ch = layout.characters.person;
  const ctx: ColorContext = { palette: layout.palette, slots: look.slots };
  const keyColor = (k: string) => look.keyColors[k] ?? resolveColor(ch.colors[k], ctx);
  const cellColor = (c: string) => (c.length === 1 ? keyColor(c) : resolveColor(c, ctx));
  const pixels: Pixel[] = [];
  frameRows(ch, frame, look.patch).forEach((row, y) =>
    [...row].forEach((k, x) => {
      const color = k === "." || k === " " ? undefined : keyColor(k);
      if (color) pixels.push({ x, y, color });
    }),
  );
  const overlay = opts.overlay ? ch.overlays?.[opts.overlay]?.cells ?? [] : [];
  for (const [x, y, c] of [...look.cells, ...overlay]) {
    const color = cellColor(c);
    if (color) pixels.push({ x, y, color });
  }
  return opts.grayscale ? pixels.map((p) => ({ ...p, color: toGray(p.color) })) : pixels;
}

export interface Bounds {
  minX: number;
  minY: number;
  w: number;
  h: number;
}

export function pixelBounds(pixels: Pixel[]): Bounds {
  if (!pixels.length) return { minX: 0, minY: 0, w: 1, h: 1 };
  const xs = pixels.map((p) => p.x);
  const ys = pixels.map((p) => p.y);
  const minX = Math.min(...xs);
  const minY = Math.min(...ys);
  return { minX, minY, w: Math.max(...xs) - minX + 1, h: Math.max(...ys) - minY + 1 };
}

export interface SpriteBitmap {
  canvas: HTMLCanvasElement;
  bounds: Bounds;
}

const bitmapCache = new Map<string, SpriteBitmap>();
const CACHE_LIMIT = 2000;

/**
 * Cached bitmap of a composed frame at 1 art pixel = 1 canvas px (drawn
 * scaled by `pixel` with smoothing off). Key = look hash + frame + options.
 */
export function frameBitmap(layout: OfficeLayout, look: ResolvedLook, frame: string, opts: ComposeOptions = {}): SpriteBitmap {
  const key = `${layout.id}|${look.hash}|${frame}|${opts.overlay ?? ""}|${opts.grayscale ? 1 : 0}`;
  const hit = bitmapCache.get(key);
  if (hit) return hit;
  const pixels = composeFrame(layout, look, frame, opts);
  const bounds = pixelBounds(pixels);
  const canvas = document.createElement("canvas");
  canvas.width = bounds.w;
  canvas.height = bounds.h;
  const ctx = canvas.getContext("2d")!;
  for (const p of pixels) {
    ctx.fillStyle = p.color;
    ctx.fillRect(p.x - bounds.minX, p.y - bounds.minY, 1, 1);
  }
  if (bitmapCache.size > CACHE_LIMIT) bitmapCache.clear();
  const bmp = { canvas, bounds };
  bitmapCache.set(key, bmp);
  return bmp;
}

export function clearBitmapCache(): void {
  bitmapCache.clear();
}
