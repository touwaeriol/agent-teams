/**
 * Removes tool grants from a skill's or command's front matter. On SDK 0.3.325 a loaded skill
 * whose front matter has `allowed-tools` runs those tools without asking canUseTool (verified
 * with a real call: a plugin skill with `allowed-tools: Bash(echo:*)` ran Bash unasked), so the
 * daemon drops the key before writing skill and command files — approvals still apply.
 */
const GRANT_KEY = /^["']?allowed[-_]tools["']?\s*:/i;

export function stripToolGrants(text: string): string {
  const bom = text.startsWith("﻿") ? "﻿" : "";
  const lines = text.slice(bom.length).split("\n");
  if (lines[0]?.trimEnd() !== "---") return text;
  const end = lines.findIndex((l, i) => i > 0 && l.trimEnd() === "---");
  if (end < 0) return text;
  return bom + [lines[0], ...dropGrantKeys(lines.slice(1, end)), ...lines.slice(end)].join("\n");
}

/** Drops grant keys with their continuation lines (indented values or "- item" lists). */
function dropGrantKeys(lines: string[]): string[] {
  const out: string[] = [];
  let skipping = false;
  for (const line of lines) {
    if (GRANT_KEY.test(line)) {
      skipping = true;
      continue;
    }
    if (skipping && /^[\s-]/.test(line) && line.trim() !== "") continue;
    skipping = false;
    out.push(line);
  }
  return out;
}

/**
 * Files whose front matter the daemon cleans: every Markdown file, not just skills/<x>/SKILL.md
 * and commands/<x>.md, so a nested or differently cased skill file cannot slip through.
 */
export function isMarkdown(path: string): boolean {
  return /\.md$/i.test(path);
}
