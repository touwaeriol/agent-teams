const SECRET_PATTERNS = [
  /gh[pousr]_[A-Za-z0-9_]{8,}/g,
  /github_pat_[A-Za-z0-9_]{8,}/g,
  /sk-[A-Za-z0-9_-]{8,}/g,
  /(Bearer\s+)[A-Za-z0-9._~+/-]{8,}/gi,
];

/** Removes ghs_/ghp_/sk-… looking strings (protocol.md §4.8). */
export function redact(text: string): string {
  let out = text;
  for (const re of SECRET_PATTERNS) out = out.replace(re, (_m, p1) => (typeof p1 === "string" ? `${p1}***` : "***"));
  return out;
}

export function truncate(text: string, max: number): { text: string; truncated: boolean } {
  if (Buffer.byteLength(text) <= max) return { text, truncated: false };
  return { text: Buffer.from(text).subarray(0, max).toString("utf8").replace(/�$/, ""), truncated: true };
}

export function summarize(text: string, max = 200): string {
  const one = redact(text).replace(/\s+/g, " ").trim();
  return one.length > max ? one.slice(0, max - 1) + "…" : one;
}
