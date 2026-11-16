// Color expressions (office-layout.md §2): literals, $palette, @slot,
// {threadColor} and shade(<color>, k).

export interface ColorContext {
  palette?: Record<string, string>;
  slots?: Record<string, string | undefined>;
  vars?: Record<string, string | undefined>;
}

const SHADE = /^shade\((.+),\s*(-?[0-9.]+)\)$/;
const FALLBACK = "#FF00FF";

export function resolveColor(expr: string | undefined, ctx: ColorContext, depth = 0): string | undefined {
  if (!expr || depth > 8) return undefined;
  const shade = SHADE.exec(expr);
  if (shade) {
    const base = resolveColor(shade[1].trim(), ctx, depth + 1);
    return base ? shadeColor(base, Number(shade[2])) : undefined;
  }
  const head = expr[0];
  if (head === "$") return resolveColor(ctx.palette?.[expr.slice(1)], ctx, depth + 1);
  if (head === "@") return resolveColor(ctx.slots?.[expr.slice(1)], ctx, depth + 1);
  if (head === "{") return resolveColor(ctx.vars?.[expr.slice(1, -1)], ctx, depth + 1);
  return expr;
}

export function colorOr(expr: string | undefined, ctx: ColorContext, fallback = FALLBACK): string {
  return resolveColor(expr, ctx) ?? fallback;
}

export interface RGBA {
  r: number;
  g: number;
  b: number;
  a: number;
}

export function parseColor(color: string): RGBA | undefined {
  if (color.startsWith("#")) return parseHex(color.slice(1));
  const m = /^rgba?\(([^)]+)\)$/.exec(color);
  if (!m) return undefined;
  const [r, g, b, a = 1] = m[1].split(",").map((s) => Number(s.trim()));
  return { r, g, b, a };
}

function parseHex(hex: string): RGBA | undefined {
  const full = hex.length <= 4 ? [...hex].map((c) => c + c).join("") : hex;
  if (!/^[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$/.test(full)) return undefined;
  const n = (i: number) => parseInt(full.slice(i, i + 2), 16);
  return { r: n(0), g: n(2), b: n(4), a: full.length === 8 ? n(6) / 255 : 1 };
}

export function formatColor({ r, g, b, a }: RGBA): string {
  const h = (v: number) => Math.round(Math.max(0, Math.min(255, v))).toString(16).padStart(2, "0");
  return a >= 1 ? `#${h(r)}${h(g)}${h(b)}` : `rgba(${Math.round(r)},${Math.round(g)},${Math.round(b)},${a})`;
}

/** k in [-1, 1]: negative darkens, positive lightens (HSL lightness scaled). */
export function shadeColor(color: string, k: number): string {
  const c = parseColor(color);
  if (!c) return color;
  const [h, s, l] = rgbToHsl(c.r, c.g, c.b);
  const l2 = k < 0 ? l * (1 + k) : l + (1 - l) * k;
  const [r, g, b] = hslToRgb(h, s, l2);
  return formatColor({ r, g, b, a: c.a });
}

export function grayscale(color: string): string {
  const c = parseColor(color);
  if (!c) return color;
  const y = 0.299 * c.r + 0.587 * c.g + 0.114 * c.b;
  return formatColor({ r: y, g: y, b: y, a: c.a });
}

function rgbToHsl(r: number, g: number, b: number): [number, number, number] {
  const [rr, gg, bb] = [r / 255, g / 255, b / 255];
  const max = Math.max(rr, gg, bb);
  const min = Math.min(rr, gg, bb);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  const h = max === rr ? (gg - bb) / d + (gg < bb ? 6 : 0) : max === gg ? (bb - rr) / d + 2 : (rr - gg) / d + 4;
  return [h / 6, s, l];
}

function hslToRgb(h: number, s: number, l: number): [number, number, number] {
  if (s === 0) return [l * 255, l * 255, l * 255];
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
  const p = 2 * l - q;
  return [hue(p, q, h + 1 / 3) * 255, hue(p, q, h) * 255, hue(p, q, h - 1 / 3) * 255];
}

function hue(p: number, q: number, t0: number): number {
  const t = t0 < 0 ? t0 + 1 : t0 > 1 ? t0 - 1 : t0;
  if (t < 1 / 6) return p + (q - p) * 6 * t;
  if (t < 1 / 2) return q;
  if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
  return p;
}
