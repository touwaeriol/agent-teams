import { realpathSync } from "node:fs";
import { basename, dirname, isAbsolute, join, resolve } from "node:path";
import type { BashPolicy } from "../protocol.js";
import { isInside } from "../util/paths.js";

/** Claude's own tools that only talk to the user (shown in the chat); they touch no files. */
export const CHAT_TOOLS = new Set(["TodoWrite", "AskUserQuestion"]);

/** Local tool restrictions for one session (architecture.md §10.2, §10.3). */
export interface ToolPolicy {
  /** Coordinator: only mcp__platform__* tools. */
  denyNonPlatform: boolean;
  /** Lead / read_only roles. */
  readOnly: boolean;
  bash: BashPolicy | null;
  /** Absolute roots the file tools may read in read-only sessions. */
  readRoots: string[];
  /** Absolute paths no tool may touch (daemon config, state, runtime logins). */
  secretPaths: string[];
}

export type Decision = { deny: string } | null;

const FILE_TOOLS: Record<string, string> = { Read: "file_path", Edit: "file_path", Write: "file_path", MultiEdit: "file_path", NotebookEdit: "notebook_path", Glob: "path", Grep: "path" };
const READ_TOOLS = new Set(["Read", "Glob", "Grep", "LS"]);
/** Tools that walk a directory tree: their root must not contain a secret path. */
const SEARCH_TOOLS = new Set(["Glob", "Grep"]);
const DEFAULT_DENY_FLAGS = ["--output", "-o", "--ext-diff", "--textconv", "-c", "--exec-path", "--git-dir", "--work-tree"];
const LIST_FLAGS = new Set(["--list", "-l", "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--no-color", "--color"]);
const LIST_FLAGS_WITH_ARG = new Set(["--contains", "--merged", "--no-merged", "--points-at"]);
const METACHARS = /[;&|<>$`(){}\n\r\\]/;
const DANGEROUS_SHELL = [
  { re: /\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|da|k)?sh\b/, why: "piping a download into a shell is not allowed" },
  { re: /\/proc\/[^\s/]+\/environ/, why: "reading another process's environment is not allowed" },
  { re: /\bgit\b.*\bconfig\b.*\b(core\.(hookspath|fsmonitor|sshcommand)|credential\.|filter\.)/i, why: "changing git hooks, fsmonitor, filters or credential settings is not allowed" },
  // Employees push with their own token (§10.4); the platform pre-push hook keeps protected branches safe.
  { re: /\bgit\b.*\s-c\s*["']?(core\.(hookspath|fsmonitor|sshcommand)|credential\.)/i, why: "overriding git hooks or credential settings is not allowed" },
  { re: /\bgit\b.*\bpush\b.*--no-verify\b/, why: "git push --no-verify skips the platform's protected-branch check" },
  { re: /\bgh\s+pr\s+merge\b|\bgh\s+api\b.*\/merge\b/, why: "merging goes through the platform merge queue (enqueue_merge / open_pr) and user acceptance, not gh" },
  { re: /\bgh\s+auth\s+token\b|\bgit\s+credential\b|\bgit\s+credential-/, why: "printing GitHub tokens is not allowed (gh and git get them automatically)" },
];

export const PLATFORM_TOOL_PREFIX = "mcp__platform__";

/**
 * PreToolUse decision: a deny reason, or null to let the normal permission flow continue.
 * Denials go back to the model as the tool result; they never become approval cards.
 */
export function decideTool(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): Decision {
  if (tool.startsWith(PLATFORM_TOOL_PREFIX) || CHAT_TOOLS.has(tool)) return null;
  if (policy.denyNonPlatform) return { deny: "This session may only use platform tools." };
  return secretDecision(policy, tool, input, cwd) ?? readOnlyDecision(policy, tool, input, cwd);
}

function secretDecision(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): Decision {
  const path = filePathOf(tool, input, cwd);
  const real = path ? realTarget(path) : null;
  if (path && policy.secretPaths.some((s) => isInside(s, path) || isInside(s, real!))) return { deny: "That path holds node secrets and is off limits." };
  if (SEARCH_TOOLS.has(tool) && policy.secretPaths.some((s) => isInside(path ?? cwd, s))) return { deny: "Searching a directory that contains node secrets is not allowed; search a narrower path." };
  if (tool !== "Bash") return null;
  const command = String(input.command ?? "");
  if (policy.secretPaths.some((s) => command.includes(s))) return { deny: "That command touches node secrets and is not allowed." };
  const hit = DANGEROUS_SHELL.find((d) => d.re.test(command));
  return hit ? { deny: `Blocked: ${hit.why}.` } : null;
}

function readOnlyDecision(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): Decision {
  if (!policy.readOnly) return null;
  if (tool === "Bash") return bashDecision(policy.bash, String(input.command ?? ""));
  if (FILE_TOOLS[tool] && !READ_TOOLS.has(tool)) return { deny: "This is a read-only session." };
  const path = filePathOf(tool, input, cwd) ?? cwd;
  if (READ_TOOLS.has(tool) && !policy.readRoots.some((r) => isInside(r, path))) return { deny: "Read-only sessions can only read inside their worktree." };
  return null;
}

/** The path with symlinks resolved on its existing part, so a link in the worktree cannot point a write outside. */
export function realTarget(path: string): string {
  try {
    return realpathSync(path);
  } catch {
    const parent = dirname(path);
    return parent === path ? path : join(realTarget(parent), basename(path));
  }
}

function filePathOf(tool: string, input: Record<string, unknown>, cwd: string): string | null {
  const key = FILE_TOOLS[tool];
  const raw = key ? input[key] : undefined;
  if (typeof raw !== "string" || !raw) return null;
  return isAbsolute(raw) ? resolve(raw) : resolve(cwd, raw);
}

/** argv-based allow list for read-only Bash (not regex matching of the whole command). */
export function bashDecision(policy: BashPolicy | null, command: string): Decision {
  if (!policy) return { deny: "Bash is not available in this read-only session." };
  if (policy.deny_metachar !== false && METACHARS.test(command)) return { deny: "Shell operators, redirection and substitution are not allowed here." };
  const argv = splitArgv(command);
  if (!argv) return { deny: "Could not parse the command." };
  const denyFlags = policy.deny_flags ?? DEFAULT_DENY_FLAGS;
  const bad = argv.find((a) => denyFlags.some((f) => deniedFlag(a, f)));
  if (bad) return { deny: `The flag ${bad} is not allowed in read-only sessions.` };
  return policy.allow.some((rule) => matchesRule(rule, argv)) ? null : { deny: `Read-only sessions may only run: ${policy.allow.join(", ")}.` };
}

/** Exact options git also accepts as unambiguous abbreviations (`--outp=x` is `--output=x`). */
const EXACT_OPTIONS = new Set(["--text"]);

function deniedFlag(arg: string, flag: string): boolean {
  if (arg === flag || arg.startsWith(`${flag}=`)) return true;
  if (!flag.startsWith("--") || !arg.startsWith("--")) return false;
  const name = arg.split("=", 1)[0]!;
  return name.length >= 4 && flag.startsWith(name) && !EXACT_OPTIONS.has(name);
}

function matchesRule(rule: string, argv: string[]): boolean {
  const [words, mode] = rule.split(":") as [string, string | undefined];
  const parts = words.trim().split(/\s+/);
  if (argv.length < parts.length || parts.some((p, i) => argv[i] !== p)) return false;
  return mode === "list" ? isListForm(argv.slice(parts.length)) : true;
}

function isListForm(args: string[]): boolean {
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!;
    if (LIST_FLAGS_WITH_ARG.has(a)) i++;
    else if (!LIST_FLAGS.has(a)) return false;
  }
  return true;
}

/** Minimal POSIX-ish word splitting with quotes; null on unbalanced quotes. */
export function splitArgv(command: string): string[] | null {
  const out: string[] = [];
  let cur = "";
  let quote: '"' | "'" | null = null;
  let has = false;
  for (const ch of command) {
    if (quote) {
      if (ch === quote) quote = null;
      else cur += ch;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has || cur) out.push(cur);
      cur = "";
      has = false;
    } else cur += ch;
  }
  if (quote) return null;
  if (has || cur) out.push(cur);
  return out;
}
