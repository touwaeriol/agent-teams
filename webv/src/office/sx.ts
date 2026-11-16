// React adds "px" to numeric style values; Vue does not. The office code was written with
// React-style numbers (left: 12, fontSize: 10), so every inline style goes through sx().
import type { CSSProperties } from "vue";

const UNITLESS = new Set([
  "opacity", "zIndex", "fontWeight", "lineHeight", "flex", "flexGrow", "flexShrink", "order", "zoom", "scale",
  "aspectRatio", "orphans", "widows", "columnCount", "gridRow", "gridColumn", "fillOpacity", "strokeOpacity",
]);

type Style = Record<string, string | number | undefined | null>;

/** Copies a style object, turning numbers into "<n>px" except for unitless properties. */
export function sx(style: CSSProperties | Style): CSSProperties {
  const out: Record<string, string | number> = {};
  for (const [key, value] of Object.entries(style as Style)) {
    if (value === undefined || value === null) continue;
    out[key] = typeof value === "number" && !UNITLESS.has(key) ? `${value}px` : value;
  }
  return out as CSSProperties;
}
