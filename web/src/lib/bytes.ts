// The one byte formatter of the console. Binary units (1 KB = 1024 B) by default; the deliverable
// library uses decimal units (1 kB = 1000 B) like its reference design.

const UNITS = { binary: { base: 1024, units: ["KB", "MB", "GB", "TB"] }, decimal: { base: 1000, units: ["kB", "MB", "GB", "TB"] } };

/** 820 B / 1.5 KB / 20 MB (binary) or 9.4 kB / 209 kB (decimal); "—" when unknown. */
export function formatBytes(n: number | undefined | null, system: keyof typeof UNITS = "binary"): string {
  if (n === undefined || n === null || !Number.isFinite(n) || n < 0) return "—";
  const { base, units } = UNITS[system];
  if (n < base) return `${n} B`;
  let v = n / base;
  let i = 0;
  while (v >= base && i < units.length - 1) {
    v /= base;
    i += 1;
  }
  return `${v >= 100 ? Math.round(v) : Number(v.toFixed(1))} ${units[i]}`;
}
