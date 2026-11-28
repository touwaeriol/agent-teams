import { lstatSync, realpathSync } from "node:fs";
import { basename, dirname, isAbsolute, join, resolve } from "node:path";
import type { BashPolicy } from "../protocol.js";
import { isInside, realpathLoose } from "../util/paths.js";

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
  /**
   * The shared folder's memory directories as real paths (claude.memory_files): the file tools work
   * inside them even in a read-only or platform-only session. Targets are compared by real path, so
   * a link cannot lead out.
   */
  memoryDirs?: string[];
}

export type Decision = { deny: string } | null;

const FILE_TOOLS: Record<string, string> = { Read: "file_path", Edit: "file_path", Write: "file_path", MultiEdit: "file_path", NotebookEdit: "notebook_path", Glob: "path", Grep: "path" };
const READ_TOOLS = new Set(["Read", "Glob", "Grep", "LS"]);
/** The file tools allowed inside the memory directories. */
const MEMORY_TOOLS = new Set(["Read", "Glob", "Grep", "Edit", "Write"]);
/** Tools that walk a directory tree: their root must not contain a secret path. */
const SEARCH_TOOLS = new Set(["Glob", "Grep"]);
const DEFAULT_DENY_FLAGS = ["--output", "-o", "--ext-diff", "--textconv", "--filters", "-c", "--exec-path", "--git-dir", "--work-tree", "--upload-pack"];
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
  { re: /\bgh\s+auth\s+token\b|\bgit\s+credential\b|\bgit\s+credential-/, why: "printing GitHub tokens is not allowed (gh and git get them automatically)" },
];

/**
 * Per-process procfs entries that expose another process's environment, memory, arguments,
 * open files or filesystem view (defence in depth for audit #55 F1; same-uid sessions can still
 * reach them through Bash or Codex, so this is not a fix). /proc/self and /proc/thread-self are
 * included: a session's own environment holds AT_CRED_SECRET, and realpath turns self into a pid anyway.
 */
const PROC_SENSITIVE = new Set([
  "environ", "mem", "maps", "smaps", "smaps_rollup", "numa_maps", "pagemap", "cmdline", "auxv",
  "fd", "fdinfo", "map_files", "root", "cwd", "exe", "stack", "syscall", "task",
]);
const PROC_PID = /^(\d+|self|thread-self)$/;

/** True when an absolute path is (or is inside) a sensitive per-process procfs entry. */
export function isSensitiveProcPath(path: string): boolean {
  const parts = resolve(path).split("/").filter(Boolean);
  if (parts[0] !== "proc" || parts.length < 3 || !PROC_PID.test(parts[1]!)) return false;
  return PROC_SENSITIVE.has(parts[2]!);
}

/** True when a search rooted at this path would walk per-process procfs directories. */
function walksProc(path: string): boolean {
  const parts = resolve(path).split("/").filter(Boolean);
  return parts[0] === "proc" && (parts.length === 1 || PROC_PID.test(parts[1]!));
}

export const PLATFORM_TOOL_PREFIX = "mcp__platform__";
const PLATFORM_ONLY = "This session may only use platform tools.";
const PLATFORM_OR_MEMORY = "This session may only use platform tools, and the file tools inside the memory directories.";

/**
 * PreToolUse decision: a deny reason, or null to let the normal permission flow continue.
 * Denials go back to the model as the tool result; they never become approval cards.
 */
export function decideTool(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): Decision {
  if (tool.startsWith(PLATFORM_TOOL_PREFIX) || CHAT_TOOLS.has(tool)) return null;
  const memory = inMemoryDirs(policy, tool, input, cwd);
  if (policy.denyNonPlatform && !memory) return { deny: policy.memoryDirs?.length ? PLATFORM_OR_MEMORY : PLATFORM_ONLY };
  return secretDecision(policy, tool, input, cwd) ?? (memory ? null : readOnlyDecision(policy, tool, input, cwd));
}

/**
 * A file tool whose target is inside one of the memory directories by real path. A target that is
 * or lies under a dangling link is refused (writing would create its target elsewhere), and so is
 * a memory directory turned into a link since the session opened. Glob's pattern must stay below its path: absolute or with
 * ".." anywhere (also inside braces) it could list anything.
 */
export function inMemoryDirs(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): boolean {
  if (!policy.memoryDirs?.length || !MEMORY_TOOLS.has(tool)) return false;
  const path = filePathOf(tool, input, cwd);
  if (!path || (tool === "Glob" && !patternStaysBelow(input.pattern))) return false;
  const real = realpathLoose(path);
  return real !== null && policy.memoryDirs.some((d) => isPlainDir(d) && isInside(d, real));
}

function patternStaysBelow(pattern: unknown): boolean {
  return typeof pattern === "string" && !isAbsolute(pattern) && !pattern.startsWith("~") && !pattern.includes("..");
}

function isPlainDir(p: string): boolean {
  try {
    return lstatSync(p).isDirectory();
  } catch {
    return false;
  }
}


function secretDecision(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): Decision {
  const path = filePathOf(tool, input, cwd);
  const real = path ? realTarget(path) : null;
  if (path && policy.secretPaths.some((s) => isInside(s, path) || isInside(s, real!))) return { deny: "That path holds node secrets and is off limits." };
  if (path && (isSensitiveProcPath(path) || isSensitiveProcPath(real!))) return { deny: "Reading other processes' environment, memory or files through /proc is not allowed." };
  if (SEARCH_TOOLS.has(tool) && (walksProc(path ?? cwd) || (real && walksProc(real)) || globWalksProc(tool, input, path ?? cwd))) return { deny: "Searching /proc process directories is not allowed; search a narrower path." };
  if (SEARCH_TOOLS.has(tool) && policy.secretPaths.some((s) => isInside(path ?? cwd, s))) return { deny: "Searching a directory that contains node secrets is not allowed; search a narrower path." };
  if (tool !== "Bash") return null;
  const command = String(input.command ?? "");
  if (policy.secretPaths.some((s) => command.includes(s))) return { deny: "That command touches node secrets and is not allowed." };
  const hit = DANGEROUS_SHELL.find((d) => d.re.test(command));
  return hit ? { deny: `Blocked: ${hit.why}.` } : null;
}

/**
 * Glob's pattern may itself be absolute (wildcards under /proc); check its literal prefix.
 * A prefix of `/` would also walk /proc, so it is refused like a search rooted at `/`.
 */
function globWalksProc(tool: string, input: Record<string, unknown>, root: string): boolean {
  if (tool !== "Glob" || typeof input.pattern !== "string") return false;
  const literal = input.pattern.split(/[*?[{]/, 1)[0]!;
  const prefix = isAbsolute(literal) ? literal : join(root, literal);
  return resolve(prefix) === "/" || walksProc(prefix) || isSensitiveProcPath(prefix);
}

function readOnlyDecision(policy: ToolPolicy, tool: string, input: Record<string, unknown>, cwd: string): Decision {
  if (!policy.readOnly) return null;
  if (tool === "Bash") return bashDecision(policy.bash, String(input.command ?? ""), { cwd, readRoots: policy.readRoots });
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

/** Where a read-only Bash command runs: lets `git -C <dir>` point at a repository inside the read roots. */
export interface BashContext {
  cwd: string;
  readRoots: string[];
}

/**
 * argv-based allow list for read-only Bash (not regex matching of the whole command).
 * `git -C <dir> …` is accepted when every <dir> is inside the read roots (literally and by real
 * path); the -C pairs are then dropped before matching the allow list. Sessions work above their
 * repositories (one subdirectory per repository) and may not cd, so this is how they reach them.
 */
export function bashDecision(policy: BashPolicy | null, command: string, ctx?: BashContext): Decision {
  if (!policy) return { deny: "Bash is not available in this read-only session." };
  if (policy.deny_metachar !== false && METACHARS.test(command)) return { deny: "Shell operators, redirection and substitution are not allowed here." };
  const parsed = splitArgv(command);
  if (!parsed) return { deny: "Could not parse the command." };
  const argv = stripGitDirs(parsed, ctx);
  if (typeof argv === "string") return { deny: argv };
  const denyFlags = policy.deny_flags ?? DEFAULT_DENY_FLAGS;
  const bad = argv.find((a) => denyFlags.some((f) => deniedFlag(a, f)));
  if (bad) return { deny: `The flag ${bad} is not allowed in read-only sessions.` };
  return policy.allow.some((rule) => matchesRule(rule, argv)) ? null : { deny: `Read-only sessions may only run: ${policy.allow.join(", ")}.` };
}

const GIT_DIR_OPTION = /^-C(.*)$/;

/**
 * Removes leading `-C <dir>` (or `-C<dir>`) options from a git command, checking each directory.
 * Returns the remaining argv, or a deny reason.
 */
function stripGitDirs(argv: string[], ctx: BashContext | undefined): string[] | string {
  if (argv[0] !== "git") return argv;
  let i = 1;
  let cwd = ctx?.cwd;
  while (i < argv.length) {
    const m = GIT_DIR_OPTION.exec(argv[i]!);
    if (!m) break;
    const dir = m[1] ? m[1] : argv[i + 1];
    i += m[1] ? 1 : 2;
    if (!dir) return "git -C needs a directory.";
    if (!ctx || !cwd) return "git -C is not available in this session.";
    // Successive -C options are relative to the previous one, as git does.
    const abs = isAbsolute(dir) ? resolve(dir) : resolve(cwd, dir);
    const real = realpathLoose(abs);
    const inside = real !== null && ctx.readRoots.some((r) => isInside(r, abs) && isInside(realpathLoose(resolve(r)) ?? resolve(r), real));
    if (!inside) return `git -C ${dir}: read-only sessions can only run git inside their working directory.`;
    cwd = abs;
  }
  return ["git", ...argv.slice(i)];
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
  const rest = argv.slice(parts.length);
  switch (mode) {
    case "list":
      return isListForm(rest);
    case "fetch":
      return isFetchForm(rest);
    case "reflog":
      return rest[0] !== "expire" && rest[0] !== "delete";
    default:
      return mode === undefined;
  }
}

/** Options of git fetch / ls-remote that only read the remote or update remote-tracking refs. */
const FETCH_FLAGS = new Set(["--prune", "-p", "--prune-tags", "-P", "--all", "--tags", "-t", "--no-tags", "-n", "--quiet", "-q",
  "--verbose", "-v", "--dry-run", "--unshallow", "--heads", "-h", "--refs", "--symref", "--get-url", "--branches", "-b"]);
const FETCH_FLAGS_WITH_VALUE = /^--(depth|deepen|shallow-since|sort)=[^=]+$/;

/**
 * `git fetch` / `git ls-remote` with a configured remote name and plain refs or patterns: no URLs,
 * paths or refspecs with a destination (`a:b`, `+a`), so nothing but FETCH_HEAD and remote-tracking
 * refs changes and no other host or transport is reached.
 */
function isFetchForm(args: string[]): boolean {
  return args.every((a) => {
    if (a.startsWith("-")) return FETCH_FLAGS.has(a) || FETCH_FLAGS_WITH_VALUE.test(a);
    return a !== "" && !a.includes(":") && !/^[/.~+]/.test(a);
  });
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
