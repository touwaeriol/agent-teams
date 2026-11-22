// A handoff digest of a conversation, read from the runtime's transcript without the model (its
// quota is spent when a session moves): the last plan / todo list, the files it changed, its
// recent commands and the recent exchange in plain text. It goes back in session.exported and the
// console quotes it when the conversation itself cannot be resumed (protocol.md §4.11).

/** Upper bound of a digest (session.exported.digest maxLength). */
export const MAX_DIGEST = 24000;

const RECENT_TURNS = 30;
const TURN_CHARS = 700;
const MAX_FILES = 60;
const MAX_COMMANDS = 15;
const COMMAND_CHARS = 200;

interface Turn {
  who: "user" | "assistant";
  text: string;
}

interface Facts {
  todos: string[] | null;
  files: Set<string>;
  commands: string[];
  turns: Turn[];
}

type Json = Record<string, unknown>;

/** The digest of a Claude transcript (.jsonl) or a Codex rollout; "" when nothing is readable. */
export function conversationDigest(runtime: "claude" | "codex", jsonl: string): string {
  const facts: Facts = { todos: null, files: new Set(), commands: [], turns: [] };
  const read = runtime === "claude" ? readClaude : readCodex;
  for (const line of jsonl.split("\n")) {
    const rec = parse(line);
    if (rec) read(rec, facts);
  }
  return render(facts);
}

function parse(line: string): Json | null {
  if (!line.trim()) return null;
  try {
    const v = JSON.parse(line) as unknown;
    return v && typeof v === "object" ? (v as Json) : null;
  } catch {
    return null;
  }
}

// ---- Claude Code transcript -----------------------------------------------------------------

const CLAUDE_FILE_TOOLS = new Set(["Edit", "Write", "MultiEdit", "NotebookEdit"]);

function readClaude(rec: Json, f: Facts): void {
  if (rec.type !== "user" && rec.type !== "assistant") return;
  if (rec.isSidechain === true || rec.isMeta === true) return;
  const msg = rec.message as Json | undefined;
  const content = msg?.content;
  if (typeof content === "string") return addTurn(f, rec.type, content);
  if (!Array.isArray(content)) return;
  const texts: string[] = [];
  for (const block of content as Json[]) {
    if (block.type === "text" && typeof block.text === "string") texts.push(block.text);
    if (block.type === "tool_use") claudeTool(String(block.name), (block.input ?? {}) as Json, f);
  }
  if (texts.length) addTurn(f, rec.type, texts.join("\n"));
}

function claudeTool(name: string, input: Json, f: Facts): void {
  if (CLAUDE_FILE_TOOLS.has(name)) {
    const path = input.file_path ?? input.notebook_path;
    if (typeof path === "string") f.files.add(path);
  } else if (name === "Bash" && typeof input.command === "string") {
    addCommand(f, input.command);
  } else if (name === "TodoWrite" && Array.isArray(input.todos)) {
    f.todos = (input.todos as Json[]).map((t) => `[${String(t.status ?? "?")}] ${String(t.content ?? "")}`);
  }
}

// ---- Codex rollout --------------------------------------------------------------------------

function readCodex(rec: Json, f: Facts): void {
  if (rec.type !== "response_item") return;
  const p = (rec.payload ?? {}) as Json;
  if (p.type === "message") return codexMessage(p, f);
  if (p.type === "function_call") return codexCall(String(p.name), parseArgs(p.arguments), f);
  if (p.type === "custom_tool_call" && p.name === "apply_patch" && typeof p.input === "string") return patchFiles(p.input, f);
  if (p.type === "local_shell_call") addCommand(f, shellText(((p.action ?? {}) as Json).command));
}

function codexMessage(p: Json, f: Facts): void {
  if (p.role !== "user" && p.role !== "assistant") return;
  const parts = Array.isArray(p.content) ? (p.content as Json[]) : [];
  const text = parts.map((c) => (typeof c.text === "string" ? c.text : "")).filter(Boolean).join("\n");
  // Codex records its environment and instructions as user messages wrapped in tags.
  if (text && !/^\s*<(environment_context|user_instructions|permissions)/.test(text)) addTurn(f, p.role, text);
}

function codexCall(name: string, args: Json, f: Facts): void {
  if (name === "shell" || name === "exec_command" || name === "container.exec") addCommand(f, shellText(args.command ?? args.cmd));
  else if (name === "apply_patch" && typeof args.input === "string") patchFiles(args.input, f);
  else if (name === "update_plan" && Array.isArray(args.plan)) {
    f.todos = (args.plan as Json[]).map((s) => `[${String(s.status ?? "?")}] ${String(s.step ?? "")}`);
  }
}

function parseArgs(raw: unknown): Json {
  if (typeof raw !== "string") return (raw ?? {}) as Json;
  return parse(raw) ?? {};
}

function shellText(cmd: unknown): string {
  if (Array.isArray(cmd)) {
    const parts = cmd.map(String);
    // ["bash", "-lc", "<script>"] → the script
    return parts.length === 3 && /(^|\/)(ba|z)?sh$/.test(parts[0]) && parts[1].startsWith("-") ? parts[2] : parts.join(" ");
  }
  return typeof cmd === "string" ? cmd : "";
}

function patchFiles(patch: string, f: Facts): void {
  for (const m of patch.matchAll(/^\*\*\* (?:Update|Add|Delete) File: (.+)$/gm)) f.files.add(m[1].trim());
}

// ---- shared ---------------------------------------------------------------------------------

function addTurn(f: Facts, who: unknown, text: string): void {
  const t = text.trim();
  if (!t || (who !== "user" && who !== "assistant")) return;
  f.turns.push({ who, text: t });
  if (f.turns.length > RECENT_TURNS * 2) f.turns.splice(0, f.turns.length - RECENT_TURNS);
}

function addCommand(f: Facts, cmd: string): void {
  const c = cmd.trim();
  if (!c) return;
  f.commands.push(c);
  if (f.commands.length > MAX_COMMANDS * 2) f.commands.splice(0, f.commands.length - MAX_COMMANDS);
}

function clip(s: string, n: number): string {
  const one = s.replace(/\s+\n/g, "\n");
  return one.length <= n ? one : `${one.slice(0, n)}…`;
}

function render(f: Facts): string {
  const out: string[] = [];
  if (f.todos?.length) out.push("待办 / 计划（最后一次更新）：", ...f.todos.map((t) => `- ${clip(t, 300)}`));
  const files = [...f.files];
  if (files.length) {
    out.push(`改过的文件（${files.length} 个${files.length > MAX_FILES ? `，列出最近 ${MAX_FILES} 个` : ""}）：`);
    out.push(...files.slice(-MAX_FILES).map((p) => `- ${p}`));
  }
  if (f.commands.length) out.push("最近执行的命令：", ...f.commands.slice(-MAX_COMMANDS).map((c) => `- ${clip(c.replace(/\n/g, " ⏎ "), COMMAND_CHARS)}`));
  const head = out.join("\n");
  const turns = f.turns.slice(-RECENT_TURNS).map((t) => `- ${t.who === "user" ? "收到" : "你"}：${clip(t.text, TURN_CHARS)}`);
  return fit(head, turns);
}

/** head plus as many of the newest turns as fit in MAX_DIGEST. */
function fit(head: string, turns: string[]): string {
  const title = "会话里最近的往来（旧→新）：";
  let budget = MAX_DIGEST - head.length - title.length - 4;
  const kept: string[] = [];
  for (let i = turns.length - 1; i >= 0 && budget - turns[i].length - 1 > 0; i--) {
    kept.unshift(turns[i]);
    budget -= turns[i].length + 1;
  }
  const parts = [head];
  if (kept.length) parts.push([title, ...kept].join("\n"));
  return parts.filter(Boolean).join("\n\n").slice(0, MAX_DIGEST);
}
