// "{name}" text templates used by plates, widgets, bubbles and sequences.

export function fillTemplate(template: string, vars: Record<string, string | number | undefined | null>): string {
  return template.replace(/\{([A-Za-z][A-Za-z0-9_]*)\}/g, (_, key: string) => {
    const v = vars[key];
    return v === undefined || v === null ? "" : String(v);
  }).replace(/\s+$/, "");
}

export function truncate(text: string, max: number | undefined): string {
  if (!max || [...text].length <= max) return text;
  return [...text].slice(0, Math.max(1, max - 1)).join("") + "…";
}

/**
 * Fills a template, shortening the `shrink` variable first so that the
 * result fits `max` characters (e.g. the thread title on a pod plate).
 */
export function fitTemplate(
  template: string,
  vars: Record<string, string | number | undefined | null>,
  shrink: string,
  max: number | undefined,
): string {
  const full = fillTemplate(template, vars);
  const value = String(vars[shrink] ?? "");
  const excess = [...full].length - (max ?? Infinity);
  if (excess <= 0 || !value) return truncate(full, max);
  const kept = Math.max(1, [...value].length - excess - 1);
  return truncate(fillTemplate(template, { ...vars, [shrink]: [...value].slice(0, kept).join("") + "…" }), max);
}
