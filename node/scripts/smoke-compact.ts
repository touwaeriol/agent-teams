// Real smoke test of compaction with the pinned Agent SDK (shared-members.md §5.2, plan P2-7),
// using this machine's Claude Code login. Checks, with a tiny window and short prompts:
//   a. Query.getContextUsage() shape before / after turns;
//   b. CLAUDE_CODE_AUTO_COMPACT_WINDOW + CLAUDE_AUTOCOMPACT_PCT_OVERRIDE trigger autocompact
//      at the threshold the driver computes (claudeCompactEnv);
//   c. a "/compact <instructions>" user message compacts and emits compact_boundary;
//   d. a PreCompact hook's systemMessage is added to the compaction instructions;
//   e. SessionStart(source "compact") fires after compaction and its additionalContext reaches the model.
//   SMOKE_MODEL=<model> npm run smoke:compact      (a small model keeps it cheap)
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { query, type HookInput, type HookJSONOutput, type SDKMessage, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import { AsyncQueue } from "../src/util/async.js";
import { claudeCompactEnv } from "../src/sessions/claude/compact.js";

const MODEL = process.env.SMOKE_MODEL ?? "claude-opus-5-5";
/** The role-level limit and in-task percentage the smoke pretends to have. */
const LIMIT = Number(process.env.SMOKE_LIMIT ?? 30000);
const PCT = Number(process.env.SMOKE_PCT ?? 80);
const HOOK_WORD = "ZEBRA-42";
const USER_WORD = "MANGO-3";
const ANCHOR_WORD = "PELICAN-7";

const dir = mkdtempSync(join(tmpdir(), "at-smoke-compact-"));
const prompt = new AsyncQueue<SDKUserMessage>();
const seen: SDKMessage[] = [];
const hooks: Array<{ event: string; detail: string }> = [];
let waiter: { pred: (m: SDKMessage) => boolean; resolve: (m: SDKMessage) => void } | null = null;

function check(cond: unknown, what: string): void {
  console.log(`${cond ? "ok" : "NOT OK"} - ${what}`);
}

function send(text: string): void {
  prompt.push({ type: "user", message: { role: "user", content: text }, parent_tool_use_id: null });
}

function next(pred: (m: SDKMessage) => boolean, ms = 240_000): Promise<SDKMessage> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("timed out waiting for a message")), ms);
    waiter = { pred, resolve: (m) => { clearTimeout(t); resolve(m); } };
  });
}

const isResult = (m: SDKMessage) => m.type === "result";

function onHook(i: HookInput): HookJSONOutput {
  if (i.hook_event_name === "PreCompact") {
    hooks.push({ event: "PreCompact", detail: `trigger=${i.trigger} custom_instructions=${JSON.stringify(i.custom_instructions)}` });
    return { systemMessage: `The summary must contain the literal code word ${HOOK_WORD}.` };
  }
  if (i.hook_event_name === "PostCompact") {
    hooks.push({ event: "PostCompact", detail: i.compact_summary });
    return {};
  }
  if (i.hook_event_name === "SessionStart") {
    hooks.push({ event: "SessionStart", detail: i.source });
    if (i.source === "compact") return { hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: `Task anchor: the secret anchor word is ${ANCHOR_WORD}.` } };
  }
  return {};
}

/** ~n tokens of filler the model has to read (this filler is about 2 chars per token). */
function filler(name: string, tokens: number): string {
  const line = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor\n";
  const path = join(dir, name);
  writeFileSync(path, line.repeat(Math.ceil((tokens * 2) / line.length)));
  return path;
}

async function usage(label: string, q: ReturnType<typeof query>): Promise<number> {
  const u = await q.getContextUsage({ detail: "summary" });
  console.log(`[usage ${label}] totalTokens=${u.totalTokens} maxTokens=${u.maxTokens} rawMaxTokens=${u.rawMaxTokens} percentage=${u.percentage}`);
  return u.totalTokens;
}

async function main(): Promise<void> {
  const env = claudeCompactEnv({ limit_tokens: LIMIT, in_task_pct: PCT });
  console.log("compact env:", JSON.stringify(env), `(limit ${LIMIT} × ${PCT}% = ${Math.floor((LIMIT * PCT) / 100)} tokens)`);
  const on = [{ hooks: [async (i: HookInput) => onHook(i)] }];
  const q = query({
    prompt,
    options: {
      cwd: dir, model: MODEL, tools: ["Read"], allowedTools: ["Read"], permissionMode: "dontAsk", settingSources: [],
      systemPrompt: { type: "preset", preset: "claude_code", append: "You are a terse test employee. Answer in one short line." },
      env: { ...(process.env as Record<string, string>), ...env },
      hooks: { PreCompact: on, PostCompact: on, SessionStart: on },
    },
  });
  void (async () => {
    for await (const m of q) {
      seen.push(m);
      if (m.type === "system" && m.subtype === "compact_boundary") console.log("compact_boundary:", JSON.stringify(m.compact_metadata));
      if (m.type === "system" && m.subtype === "status") console.log("status:", JSON.stringify(m));
      if (waiter?.pred(m)) { const w = waiter; waiter = null; w.resolve(m); }
    }
  })();

  send("Reply with the word ok.");
  await next(isResult);
  const base = await usage("after turn 1", q);
  check(base > 0, "a. getContextUsage reports used tokens after a turn");

  // b. grow the context past limit × pct with file reads; autocompact should fire before a request.
  const threshold = Math.floor((LIMIT * PCT) / 100);
  const read = Math.max(2000, Math.ceil((threshold - base) * 0.6));
  const autoBefore = seen.length;
  for (const [i, name] of ["a.txt", "b.txt"].entries()) {
    send(`Use the Read tool to read ${filler(name, read)} completely, then reply with the word read.`);
    await next(isResult);
    await usage(`after read ${i + 1}`, q);
  }
  send("Reply with the word ok.");
  await next(isResult);
  const autos = seen.slice(autoBefore).flatMap((m) => (m.type === "system" && m.subtype === "compact_boundary" ? [m.compact_metadata] : []));
  check(autos.length === 1 && autos[0]!.trigger === "auto" && autos[0]!.pre_tokens >= threshold, `b. autocompact fired once, above the threshold ${threshold} (pre_tokens ${autos[0]?.pre_tokens})`);
  await usage("after autocompact", q);

  // c + d. manual /compact with instructions; the PreCompact hook adds its own.
  const manualBefore = seen.length;
  send(`/compact Keep the code word ${USER_WORD} in the summary.`);
  const boundary = await next((m) => m.type === "system" && m.subtype === "compact_boundary");
  check(boundary.type === "system" && boundary.subtype === "compact_boundary" && boundary.compact_metadata.trigger === "manual", "c. /compact emits compact_boundary (manual)");
  const after = await next(isResult, 60_000).catch(() => null);
  console.log("messages after /compact:", seen.slice(manualBefore).map((m) => `${m.type}/${"subtype" in m ? m.subtype : ""}`).join(", "));
  console.log("result after /compact:", after ? JSON.stringify({ subtype: after.type === "result" ? after.subtype : "", result: after.type === "result" && "result" in after ? after.result : "" }) : "none");
  await usage("after /compact", q);
  const post = hooks.filter((h) => h.event === "PostCompact").at(-1)?.detail ?? "";
  check(post.includes(USER_WORD), "c. the /compact instructions shaped the summary");
  check(post.includes(HOOK_WORD), "d. PreCompact systemMessage was added to the compaction instructions");

  // e. SessionStart(compact) additionalContext reaches the model.
  send("What is the secret anchor word? Reply with only that word.");
  const res = await next(isResult);
  const answer = res.type === "result" && "result" in res ? res.result : "";
  console.log("anchor answer:", answer);
  check(hooks.some((h) => h.event === "SessionStart" && h.detail === "compact"), "e. SessionStart(compact) fired after compaction");
  check(answer.includes(ANCHOR_WORD), "e. its additionalContext reached the model");

  for (const h of hooks) console.log(`[hook ${h.event}] ${h.detail.slice(0, 300).replace(/\n/g, " ")}`);
  const cost = seen.filter((m) => m.type === "result").reduce((s, m) => s + (m.type === "result" ? m.total_cost_usd : 0), 0);
  console.log(`total_cost_usd (sum of results): ${cost.toFixed(4)}`);
  prompt.end();
  q.close();
}

main().then(
  () => { rmSync(dir, { recursive: true, force: true }); process.exit(0); },
  (err: Error) => { console.error(err.message); rmSync(dir, { recursive: true, force: true }); process.exit(1); },
);
