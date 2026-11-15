/**
 * GFM autolink literals run until whitespace, so "见 https://x/pull/1。下一句" links the
 * Chinese text too. A bare URL directly followed by a non-ASCII character is wrapped in
 * <…> (a CommonMark autolink, which ends at ">") so the link stops at the last ASCII char.
 * URLs inside `code`, fenced blocks and [text](url) links are left alone.
 */
const BARE_URL = /(^|[^<(\[`/\w])(https?:\/\/[!-~]+?)(?=[^\x00-\x7F])/g;

export function fenceCjkUrls(markdown: string): string {
  return markdown
    .split(/(```[\s\S]*?```|`[^`\n]*`)/)
    .map((part, i) => (i % 2 === 1 ? part : part.replace(BARE_URL, (_, pre: string, url: string) => `${pre}<${url}>`)))
    .join("");
}
