import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { AGY_NOT_INSTALLED, AntigravityDriver, promptBlocks } from "../src/sessions/antigravity/driver.js";
import { agySettingsFile, detectInstalled, detectLogin, detectLoginAcp, detectVersion, probeAntigravity, writeAgySettings } from "../src/sessions/antigravity/detect.js";
import { isPlatformTool, planEvent, promptError, toolName, toolStarted, turnEnded } from "../src/sessions/antigravity/mapper.js";
import { decidePermission, outcome } from "../src/sessions/antigravity/permissions.js";
import { acpMcpServers } from "../src/sessions/plugins.js";
import type { ToolPolicy } from "../src/sessions/policy.js";
import type { DriverContext } from "../src/sessions/types.js";
import { silentLogger } from "../src/util/log.js";
import { startHarness, type Harness } from "./helpers/daemon.js";

const FAKE = join(import.meta.dirname, "helpers", "fake-agy.mjs");
const AGY = [process.execPath, FAKE];

let h: Harness | undefined;
let logFile = "";
const tmp: string[] = [];
afterEach(async () => {
  delete process.env.FAKE_AGY_AUTH;
  delete process.env.FAKE_AGY_NO_RESUME;
  for (const d of tmp.splice(0)) rmSync(d, { recursive: true, force: true });
  if (!h) return;
  const invalid = [...h.console.invalid];
  await h.close();
  h = undefined;
  rmSync(logFile, { force: true });
  expect(invalid).toEqual([]);
});

function scratch(): string {
  const d = mkdtempSync(join(tmpdir(), "agy-t-"));
  tmp.push(d);
  return d;
}

const NAMES = ["send_message", "complete_task"];
const RO_BASH = { mode: "argv" as const, allow: ["git status", "git diff", "ls"], deny_metachar: true };

function agyOpen(id: string, opts: Record<string, unknown> = {}, extra: Record<string, unknown> = {}) {
  return {
    session_id: id, open_gen: 1, employee: { name: "rev-1", role: "reviewer", tier: "member", project_id: "p1", thread_no: 3 },
    runtime: "antigravity", cwd: `projects/p1/threads/3/${id}`, resume: null,
    antigravity: { model: "", read_only: false, approval: "ask", system_append: "be careful", ...opts },
    platform_tools: [{ name: "complete_task", description: "Finish", blocking: false, input_schema: { type: "object", required: ["task_id", "summary"], properties: { task_id: { type: "string" }, summary: { type: "string" } } } }],
    env: { LANG: "C.UTF-8" },
    ...extra,
  };
}

type Call = { method: string; params: Record<string, any>; argv?: string[]; env?: Record<string, string>; cwd?: string };

async function startAgy() {
  logFile = join(process.env.TMPDIR ?? tmpdir(), `fake-agy-${process.pid}-${Date.now()}.jsonl`);
  process.env.FAKE_AGY_LOG = logFile;
  h = await startHarness({ agyCommand: AGY, passEnv: ["FAKE_AGY_LOG", "FAKE_AGY_AUTH", "FAKE_AGY_NO_RESUME"] });
  return () => (existsSync(logFile) ? readFileSync(logFile, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l) as Call) : []);
}

async function openAndRelease(id: string, opts: Record<string, unknown> = {}, extra: Record<string, unknown> = {}) {
  h!.console.command("session.open", agyOpen(id, opts, extra));
  const opened = await h!.console.waitType("session.opened", (b) => b.session_id === id);
  h!.console.command("session.release", { session_id: id, open_gen: 1 });
  return opened;
}

async function say(id: string, msgId: string, text: string) {
  h!.console.command("session.send", { session_id: id, msg_id: msgId, text });
  return h!.console.waitType("session.delivered", (b) => b.msg_id === msgId);
}

const finalText = (id: string) => h!.console.waitType("session.event", (b) => b.session_id === id && b.kind === "text" && b.final);

describe("antigravity sessions over ACP", () => {
  it("starts agy acp per session with its own HOME, the platform MCP server and the system prompt", async () => {
    const calls = await startAgy();
    const plugins = [{ name: "tinyplug", version: 2, files: [{ path: ".claude-plugin/plugin.json", content: '{"name":"tinyplug"}' }, { path: "s.mjs", content: "//" }],
      mcp_servers: { calc: { type: "stdio", command: "node", args: ["${CLAUDE_PLUGIN_ROOT}/s.mjs"], env: { K: "v" } } } }];
    const opened = await openAndRelease("s_A1", {}, { plugins, git_credentials: true });
    expect(opened.body).toMatchObject({ sdk_session_id: "agy-sess-1", init: { tools: ["mcp__platform__complete_task"], permission_mode: "acp/ask", model: "gemini-default", mcp_servers: [{ name: "platform", status: "connected" }] } });
    const start = calls().find((c) => c.method === "__start")!;
    expect(start.argv).toEqual(["acp", "--process-per-session"]);
    expect(start.env!.HOME).toBe(join(h!.dir, "agy-home"));
    expect(start.env!.AGY_CLI_DISABLE_AUTO_UPDATE).toBe("true");
    expect(start.env!.AT_CRED_SOCK).toBeTruthy(); // git / gh credentials reach agy's shell like other runtimes
    expect(start.env!.GIT_AUTHOR_NAME).toBe("rev-1");
    expect(start.cwd).toBe(join(h!.opts.root, "projects/p1/threads/3/s_A1"));
    const init = calls().find((c) => c.method === "initialize")!.params;
    expect(init.clientCapabilities).toEqual({ fs: { readTextFile: false, writeTextFile: false }, terminal: false });
    const p = calls().find((c) => c.method === "session/new")!.params;
    expect(p.cwd).toBe(start.cwd);
    expect(p.mcpServers[0]).toMatchObject({ type: "http", name: "platform", headers: [] });
    expect(p.mcpServers[0].url).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/mcp\/s_A1\/[A-Za-z0-9_-]{43}$/);
    const dir = join(h!.opts.root, "agent-plugins", "p1", "3", "rev-1", "tinyplug@2");
    expect(p.mcpServers[1]).toEqual({ name: "plugin_tinyplug_calc", command: "node", args: [join(dir, "s.mjs")], env: [{ name: "K", value: "v" }] });
    expect(p._meta).toEqual({ antigravity: { systemPrompt: { append: "be careful" } } });
    expect(h!.console.ofType("hello")[0]!.body.runtimes.antigravity).toEqual({ cli: "", auth: "none", installed: false });
    // the session that opened re-checks the state the next hello reports (no timer)
    expect(h!.daemon.runtimes.get("antigravity").info()).toMatchObject({ installed: true, auth: "oauth" });
  });

  it("gives agy remote servers: AT_SSH_CONFIG in its environment and the remote-server prompt", async () => {
    const calls = await startAgy();
    await openAndRelease("s_S1", {}, { git_credentials: true, ssh_servers: [{ name: "prod" }] });
    const start = calls().find((c) => c.method === "__start")!;
    if (!h!.console.ofType("hello")[0]!.body.ssh_client) return expect(start.env!.AT_SSH_CONFIG).toBeUndefined(); // no OpenSSH on this machine
    expect(start.env!.AT_SSH_CONFIG).toMatch(/s_S1\/ssh_config$/);
    const append = calls().find((c) => c.method === "session/new")!.params._meta.antigravity.systemPrompt.append as string;
    expect(append).toMatch(/^be careful\n\n## Remote servers\n.*: prod\./);
  });

  it("reports spawn_failed at once when the agy executable cannot be started", async () => {
    logFile = join(tmpdir(), `fake-agy-none-${process.pid}.jsonl`);
    h = await startHarness({ agyCommand: ["/nonexistent/agy"] });
    const t0 = Date.now();
    h.console.command("session.open", agyOpen("s_N2"));
    const fatal = await h.console.waitType("session.event", (b) => b.kind === "error" && b.code === "spawn_failed");
    expect(fatal.body.message).toMatch(/cannot start agy/);
    expect(Date.now() - t0).toBeLessThan(5000);
  });

  it("maps a turn: thinking, streamed text, plan as todos, final text and turn_ended", async () => {
    await startAgy();
    await openAndRelease("s_A2");
    const d = await say("s_A2", "m_1", "hello plan");
    expect(d.body.turn_id).toMatch(/^t_/);
    const text = await finalText("s_A2");
    expect(text.body.text).toBe("echo: hello plan");
    const kinds = h!.console.ofType("session.event").filter((f) => f.body.session_id === "s_A2").map((f) => f.body.kind);
    expect(kinds).toEqual(["turn_started", "thinking", "todos", "text", "turn_ended"]);
    const todos = h!.console.ofType("session.event").find((f) => f.body.kind === "todos")!.body.todos;
    expect(todos).toEqual([{ content: "read the code", status: "completed" }, { content: "write tests", status: "in_progress" }]);
    const ended = await h!.console.waitType("session.event", (b) => b.kind === "turn_ended");
    expect(ended.body).toMatchObject({ result_subtype: "success", is_error: false, turn_id: d.body.turn_id });
    expect(h!.console.ofType("stream.delta").length).toBe(0); // not watched
  });

  it("runs platform tools through the loopback MCP server", async () => {
    await startAgy();
    await openAndRelease("s_A3");
    await say("s_A3", "m_1", "use the tool");
    const call = await h!.console.waitType("tool.call");
    expect(call.body).toMatchObject({ session_id: "s_A3", tool: "complete_task", args: { task_id: "t3", summary: "agy done" } });
    h!.console.command("tool.result", { session_id: "s_A3", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "recorded" }] });
    expect((await finalText("s_A3")).body.text).toBe("tool: recorded");
    const started = h!.console.ofType("session.event").find((f) => f.body.kind === "tool_started")!.body;
    expect(started).toMatchObject({ tool: "mcp__platform__complete_task", category: "platform", tool_use_id: "call_p" });
  });

  it("asks the console for permission requests of ask sessions", async () => {
    await startAgy();
    await openAndRelease("s_A4");
    await say("s_A4", "m_1", "please write");
    const req = await h!.console.waitType("approval.request");
    expect(req.body).toMatchObject({ source: "antigravity_permission", tool_name: "Edit", tool_use_id: "call_w", available: ["allow", "deny"], description: "Write notes.txt" });
    expect(req.body.input.file_path).toMatch(/notes\.txt$/);
    h!.console.command("approval.resolve", { approval_id: req.body.approval_id, decision: "allow_always", decided_by: "user:1" });
    // agy's own "always" is never picked: its scope is unverified and agy's HOME is shared by the node
    expect((await finalText("s_A4")).body.text).toBe("permission: allow-once");
    const ended = h!.console.ofType("session.event").find((f) => f.body.kind === "tool_ended")!.body;
    expect(ended).toMatchObject({ tool_use_id: "call_w", ok: true });
  });

  it("allows permission requests of auto (bypass) sessions without a card", async () => {
    await startAgy();
    await openAndRelease("s_A5", { approval: "auto" });
    await say("s_A5", "m_1", "please write");
    expect((await finalText("s_A5")).body.text).toBe("permission: allow-once");
    expect(h!.console.ofType("approval.request")).toEqual([]);
  });

  it("names command permissions Bash, so the console's shell denylist applies", async () => {
    await startAgy();
    await openAndRelease("s_A0");
    await say("s_A0", "m_1", "run make test");
    const req = await h!.console.waitType("approval.request");
    expect(req.body).toMatchObject({ source: "antigravity_permission", tool_name: "Bash", input: { command: "make test" } });
    h!.console.command("approval.resolve", { approval_id: req.body.approval_id, decision: "deny", decided_by: "user:1" });
    expect((await finalText("s_A0")).body.text).toBe("permission: reject-once");
  });

  it("refuses writes and commands outside bash_policy in read-only sessions, without cards", async () => {
    await startAgy();
    await openAndRelease("s_A6", { read_only: true, bash_policy: RO_BASH });
    await say("s_A6", "m_1", "please write");
    expect((await finalText("s_A6")).body.text).toBe("permission: reject-once");
    await say("s_A6", "m_2", "run git status");
    await h!.console.waitFor((f) => f.type === "session.event" && f.body.kind === "text" && f.body.final && f.body.text === "permission: allow-once");
    await say("s_A6", "m_3", "run rm -rf build");
    await h!.console.waitFor((f) => f.type === "session.event" && f.body.kind === "text" && f.body.final && f.body.text === "permission: reject-once" && f.body.turn_id !== undefined, 10_000);
    expect(h!.console.ofType("approval.request")).toEqual([]);
    const opened = h!.console.ofType("session.opened")[0]!.body;
    expect(opened.init.permission_mode).toBe("acp/ask/read_only");
  });

  it("cancels the turn when a read-only session runs a writing tool it never asked for", async () => {
    const calls = await startAgy();
    await openAndRelease("s_A7", { read_only: true, bash_policy: RO_BASH });
    await say("s_A7", "m_1", "be sneaky");
    const err = await h!.console.waitType("session.event", (b) => b.kind === "error");
    expect(err.body).toMatchObject({ code: "protocol", fatal: false });
    expect(err.body.message).toMatch(/read-only session: agy ran a edit tool/);
    const ended = await h!.console.waitType("session.event", (b) => b.kind === "turn_ended");
    expect(ended.body.result_subtype).toBe("cancelled");
    expect(calls().some((c) => c.method === "session/cancel")).toBe(true);
  });

  it("interrupts with session/cancel and withdraws the open approval card", async () => {
    const calls = await startAgy();
    await openAndRelease("s_A8");
    await say("s_A8", "m_1", "please write");
    const req = await h!.console.waitType("approval.request");
    h!.console.command("session.interrupt", { session_id: "s_A8", reason: "user_stop" });
    const cancel = await h!.console.waitType("approval.cancel");
    expect(cancel.body).toMatchObject({ approval_id: req.body.approval_id, reason: "interrupted" });
    await h!.console.waitType("session.interrupted");
    expect((await finalText("s_A8")).body.text).toBe("permission: cancelled");
    expect(calls().filter((c) => c.method === "session/cancel").map((c) => c.params)).toEqual([{ sessionId: "agy-sess-1" }]);
  });

  it("queues a message sent during a turn and runs it after", async () => {
    await startAgy();
    await openAndRelease("s_A9");
    const first = await say("s_A9", "m_1", "hold on");
    const second = await say("s_A9", "m_2", "second");
    expect(second.body.turn_id).toBe(first.body.turn_id);
    h!.console.command("session.interrupt", { session_id: "s_A9", reason: "user_stop" });
    const text = await finalText("s_A9");
    expect(text.body.text).toBe("echo: second");
  });

  it("resumes the stored session with session/resume and fresh MCP servers", async () => {
    const calls = await startAgy();
    const opened = await openAndRelease("s_B1", {}, { resume: { sdk_session_id: "agy-sess-9" } });
    expect(opened.body.sdk_session_id).toBe("agy-sess-9");
    const p = calls().find((c) => c.method === "session/resume")!.params;
    expect(p).toMatchObject({ sessionId: "agy-sess-9", _meta: { antigravity: { systemPrompt: { append: "be careful" } } } });
    expect(p.mcpServers[0].url).toMatch(/\/mcp\/s_B1\//);
    expect(calls().some((c) => c.method === "session/new")).toBe(false);
  });

  it("falls back to session/load and drops the replayed history", async () => {
    process.env.FAKE_AGY_NO_RESUME = "1";
    const calls = await startAgy();
    await openAndRelease("s_B2", {}, { resume: { sdk_session_id: "agy-sess-7" } });
    expect(calls().find((c) => c.method === "session/load")!.params.sessionId).toBe("agy-sess-7");
    await say("s_B2", "m_1", "after load");
    expect((await finalText("s_B2")).body.text).toBe("echo: after load");
    const texts = h!.console.ofType("session.event").filter((f) => f.body.kind === "text").map((f) => f.body.text);
    expect(texts).toEqual(["echo: after load"]);
  });

  it("reports resume_failed when agy no longer has the session", async () => {
    await startAgy();
    h!.console.command("session.open", agyOpen("s_B3", {}, { resume: { sdk_session_id: "agy-missing" } }));
    const opened = await h!.console.waitType("session.opened");
    expect(opened.body).toMatchObject({ resume_failed: true });
  });

  it("reports an agy that is not logged in", async () => {
    process.env.FAKE_AGY_AUTH = "none";
    await startAgy();
    h!.console.command("session.open", agyOpen("s_B4"));
    const auth = await h!.console.waitType("session.event", (b) => b.kind === "error" && b.code === "auth_failed");
    expect(auth.body.message).toMatch(/agy acp --login/);
    const fatal = await h!.console.waitType("session.event", (b) => b.kind === "error" && b.code === "spawn_failed");
    expect(fatal.body.message).toMatch(/not logged in/);
    expect(h!.daemon.runtimes.get("antigravity").info()).toMatchObject({ auth: "none" });
  });

  it("sets the role's model with session/set_model and reports a refused one", async () => {
    const calls = await startAgy();
    const opened = await openAndRelease("s_B5", { model: "gemini-4-neon-high" });
    expect(opened.body.init.model).toBe("gemini-4-neon-high");
    expect(calls().find((c) => c.method === "session/set_model")!.params).toEqual({ sessionId: "agy-sess-1", modelId: "gemini-4-neon-high" });
    const bad = await openAndRelease("s_B6", { model: "bad-model" });
    expect(bad.body.init.model).toBe("gemini-default");
    const err = await h!.console.waitType("session.event", (b) => b.session_id === "s_B6" && b.kind === "error");
    expect(err.body.message).toMatch(/bad-model/);
  });

  it("turns a quota error into rate_limited", async () => {
    await startAgy();
    await openAndRelease("s_B7");
    await say("s_B7", "m_1", "quota please");
    const rl = await h!.console.waitType("session.event", (b) => b.kind === "rate_limited");
    expect(rl.body.detail).toMatch(/RESOURCE_EXHAUSTED/);
    const ended = await h!.console.waitType("session.event", (b) => b.kind === "turn_ended");
    expect(ended.body.is_error).toBe(true);
  });

  it("compacts with /compact as a prompt, without showing it as a turn", async () => {
    const calls = await startAgy();
    await openAndRelease("s_B8");
    h!.console.command("session.compact", { session_id: "s_B8", open_gen: 1, compact_id: "cp_1", instructions: "keep the task" });
    const done = await h!.console.waitType("session.compacted");
    expect(done.body).toMatchObject({ compact_id: "cp_1", ok: true });
    const prompt = calls().find((c) => c.method === "session/prompt")!.params.prompt[0].text;
    expect(prompt).toBe("/compact keep the task");
    expect(h!.console.ofType("session.event").filter((f) => f.body.kind === "turn_started")).toEqual([]);
  });

  it("closes the ACP session on session.close", async () => {
    const calls = await startAgy();
    await openAndRelease("s_B9");
    h!.console.command("session.close", { session_id: "s_B9", reason: "thread_done" });
    await h!.console.waitType("session.closed");
    expect(calls().find((c) => c.method === "session/close")!.params).toEqual({ sessionId: "agy-sess-1" });
  });
});

describe("antigravity driver without agy", () => {
  it("fails to start with a clear message when agy is not installed", async () => {
    const ctx = { spec: agyOpen("s_N1"), absCwd: scratch(), env: {}, policy: policy(), sink: {} as never, log: silentLogger } as unknown as DriverContext;
    const d = new AntigravityDriver(ctx, { toolhost: {} as never, command: () => null });
    const seen: string[] = [];
    const d2 = new AntigravityDriver(ctx, { toolhost: {} as never, command: () => null, onStatus: (s) => seen.push(s) });
    const t0 = Date.now();
    await expect(d.start()).rejects.toThrow(AGY_NOT_INSTALLED);
    await expect(d2.start()).rejects.toThrow(AGY_NOT_INSTALLED);
    expect(Date.now() - t0).toBeLessThan(500); // fails at once, nothing is spawned
    expect(seen).toEqual(["not_installed"]);
  });
});

function policy(o: Partial<ToolPolicy> = {}): ToolPolicy {
  return { denyNonPlatform: false, readOnly: false, bash: null, readRoots: ["/w"], secretPaths: ["/secret"], ...o };
}

describe("antigravity permissions", () => {
  const edit = { toolCallId: "c1", kind: "edit", title: "Edit a.go", locations: [{ path: "/w/a.go" }] };
  it("asks or allows by approval mode outside read-only sessions", () => {
    expect(decidePermission(policy(), "ask", edit, "/w", NAMES)).toEqual({ action: "ask" });
    expect(decidePermission(policy(), "auto", edit, "/w", NAMES)).toEqual({ action: "allow" });
  });
  it("refuses secret paths and dangerous shell whatever the mode", () => {
    expect(decidePermission(policy(), "auto", { ...edit, locations: [{ path: "/secret/token" }] }, "/w", NAMES).action).toBe("deny");
    expect(decidePermission(policy(), "auto", { toolCallId: "c", kind: "execute", rawInput: { command: "gh auth token" } }, "/w", NAMES).action).toBe("deny");
  });
  it("read-only: refuses every writing kind, runs only allowed commands, reads inside the worktree", () => {
    const ro = policy({ readOnly: true, bash: RO_BASH });
    for (const kind of ["edit", "delete", "move", "other", "fetch", "switch_mode"]) expect(decidePermission(ro, "auto", { ...edit, kind }, "/w", NAMES).action).toBe("deny");
    expect(decidePermission(ro, "ask", { toolCallId: "c", kind: "execute", rawInput: { command: "git status" } }, "/w", NAMES)).toEqual({ action: "allow" });
    expect(decidePermission(ro, "ask", { toolCallId: "c", kind: "execute", rawInput: { command: ["git", "diff"] } }, "/w", NAMES)).toEqual({ action: "allow" });
    expect(decidePermission(ro, "auto", { toolCallId: "c", kind: "execute", rawInput: { command: "rm -rf /" } }, "/w", NAMES).action).toBe("deny");
    expect(decidePermission(ro, "auto", { toolCallId: "c", kind: "execute", title: "?" }, "/w", NAMES).action).toBe("deny");
    expect(decidePermission(ro, "ask", { toolCallId: "c", kind: "read", locations: [{ path: "/w/x" }] }, "/w", NAMES)).toEqual({ action: "allow" });
    expect(decidePermission(ro, "ask", { toolCallId: "c", kind: "read", locations: [{ path: "/etc/passwd" }] }, "/w", NAMES).action).toBe("deny");
  });
  it("always allows platform tools", () => {
    expect(decidePermission(policy({ readOnly: true }), "ask", { toolCallId: "c", kind: "other", title: "platform/send_message" }, "/w", NAMES)).toEqual({ action: "allow" });
    expect(decidePermission(policy({ readOnly: true }), "ask", { toolCallId: "c", kind: "edit", title: "platform/unknown_tool" }, "/w", NAMES).action).toBe("deny");
    expect(decidePermission(policy({ readOnly: true }), "ask", { toolCallId: "c", title: "mcp_platform_complete_task" }, "/w", NAMES)).toEqual({ action: "allow" });
  });
  it("never takes a call of a writing or reading kind for a platform tool, whatever its title", () => {
    const cmd = { toolCallId: "c", kind: "execute", title: "platform/send_message", rawInput: { command: "platform/send_message" } };
    expect(decidePermission(policy({ readOnly: true, bash: RO_BASH }), "ask", cmd, "/w", NAMES).action).toBe("deny");
    expect(decidePermission(policy(), "ask", cmd, "/w", NAMES)).toEqual({ action: "ask" });
    for (const kind of ["edit", "delete", "move", "fetch", "read", "search"]) expect(isPlatformTool({ title: "platform/send_message", kind }, NAMES)).toBe(false);
  });
  it("refuses a read-only read whose path is not recognised", () => {
    expect(decidePermission(policy({ readOnly: true }), "ask", { toolCallId: "c", kind: "read", title: "Read", rawInput: { target: "/etc/passwd" } }, "/w", NAMES).action).toBe("deny");
  });
  it("picks options by kind, cancelled when there is none", () => {
    const opts = [{ optionId: "a", kind: "allow_once" }, { optionId: "r", kind: "reject_once" }];
    expect(outcome(opts, ["allow_always", "allow_once"])).toEqual({ outcome: { outcome: "selected", optionId: "a" } });
    expect(outcome([], ["reject_once"])).toEqual({ outcome: { outcome: "cancelled" } });
  });
});

describe("antigravity mapping", () => {
  it("names tools, categories and summaries", () => {
    expect(isPlatformTool({ title: "platform/send_message" })).toBe(true);
    expect(isPlatformTool({ title: "mcp_platform_send_message" })).toBe(true);
    expect(isPlatformTool({ title: "Edit platform.go" })).toBe(false);
    expect(isPlatformTool({ title: "platform.go" })).toBe(false);
    expect(isPlatformTool({ title: "mcp(platform/send_message)" }, NAMES)).toBe(true);
    expect(isPlatformTool({ title: "platform/rm_everything" }, NAMES)).toBe(false);
    expect(toolName({ title: "platform/send_message", kind: "other" })).toBe("mcp__platform__send_message");
    expect(toolStarted({ toolCallId: "x", kind: "execute", title: "Run", rawInput: { command: "go test ./..." } })).toMatchObject({ kind: "tool_started", tool: "execute", category: "shell", summary: "go test ./..." });
    expect(toolStarted({ toolCallId: "y", kind: "edit", title: "Edit a.go" })).toMatchObject({ category: "edit", summary: "Edit a.go" });
  });
  it("turns plans, stop reasons and errors into events", () => {
    expect(planEvent([{ content: "a", status: "weird" }])).toEqual({ kind: "todos", todos: [{ content: "a", status: "pending" }] });
    expect(turnEnded("max_tokens", undefined)).toMatchObject({ result_subtype: "max_tokens", is_error: false });
    expect(turnEnded("refusal", undefined)).toMatchObject({ is_error: true });
    expect(promptError(-32000, "Authentication required")).toMatchObject({ kind: "error", code: "auth_failed" });
    expect(promptError(-32603, "429 Too Many Requests")).toMatchObject({ kind: "rate_limited" });
    expect(promptError(-32603, "boom")).toMatchObject({ kind: "error", code: "protocol" });
  });
  it("builds prompt blocks with images and notes PDFs", () => {
    const blocks = promptBlocks("hi", [
      { id: "a", media_type: "image/png", data: Buffer.from("png"), path: "/x/a.png" },
      { id: "b", media_type: "application/pdf", data: Buffer.from("pdf"), path: "/x/b.pdf" },
    ]);
    expect(blocks[0]!.text).toMatch(/^hi\n\n\[附件 PDF .*\/x\/b\.pdf\]$/);
    expect(blocks[1]).toEqual({ type: "image", mimeType: "image/png", data: Buffer.from("png").toString("base64") });
  });
  it("converts plugin MCP servers to ACP form", () => {
    const servers = acpMcpServers([{ name: "p", version: 1, dir: "/d", skills: [], mcpServers: {
      plugin_p_a: { type: "stdio", command: "node", args: ["s.mjs"], env: { K: "v" } },
      plugin_p_b: { type: "http", url: "http://x/mcp", headers: { Authorization: "Bearer t" } },
    } }]);
    expect(servers).toEqual([
      { name: "plugin_p_a", command: "node", args: ["s.mjs"], env: [{ name: "K", value: "v" }] },
      { type: "http", name: "plugin_p_b", url: "http://x/mcp", headers: [{ name: "Authorization", value: "Bearer t" }] },
    ]);
  });
});

describe("antigravity detection", () => {
  it("finds agy explicitly, on PATH or in ~/.local/bin, and only when executable", () => {
    const d = scratch();
    const bin = join(d, "bin");
    mkdirSync(bin);
    expect(detectInstalled({ path: bin, homes: [d] })).toBeNull();
    writeFileSync(join(bin, "agy"), "#!/bin/sh\n");
    expect(detectInstalled({ path: bin, homes: [d] })).toBeNull(); // not executable
    chmodSync(join(bin, "agy"), 0o755);
    expect(detectInstalled({ path: bin, homes: [d] })).toBe(join(bin, "agy"));
    mkdirSync(join(d, ".local", "bin"), { recursive: true });
    writeFileSync(join(d, ".local", "bin", "agy"), "#!/bin/sh\n", { mode: 0o755 });
    expect(detectInstalled({ path: "", homes: [d] })).toBe(join(d, ".local", "bin", "agy"));
    expect(detectInstalled({ explicit: join(bin, "agy"), path: "", homes: [] })).toBe(join(bin, "agy"));
  });

  it("reads the version from agy --version", async () => {
    expect(await detectVersion(AGY, {})).toBe("1.5.14");
    expect(await detectVersion(AGY, { FAKE_AGY_NO_VERSION: "1" })).toBeNull();
    expect(await detectVersion(["/nonexistent/agy"], {})).toBeNull();
  });

  it("tells the login from env names and token files only", () => {
    const home = scratch();
    expect(detectLogin(home, {})).toBeNull();
    expect(detectLogin(home, { GEMINI_API_KEY: "k" })).toBe("api_key");
    expect(detectLogin(home, { AGY_ADC_AUTH: "true" })).toBe("adc");
    expect(detectLogin(home, { AGY_ADC_AUTH: "false" })).toBeNull();
    mkdirSync(join(home, ".gemini", "antigravity-cli"), { recursive: true });
    writeFileSync(join(home, ".gemini", "antigravity-cli", "oauth_creds.json"), "");
    expect(detectLogin(home, {})).toBeNull(); // empty file
    writeFileSync(join(home, ".gemini", "antigravity-cli", "oauth_creds.json"), "{}");
    expect(detectLogin(home, {})).toBe("oauth");
  });

  it("falls back to an ACP handshake, closing the session it opened", async () => {
    const log = join(scratch(), "log.jsonl");
    const cwd = scratch();
    expect(await detectLoginAcp(AGY, { FAKE_AGY_LOG: log, PATH: process.env.PATH ?? "" }, cwd, silentLogger)).toBe("oauth");
    const methods = readFileSync(log, "utf8").trim().split("\n").map((l) => JSON.parse(l).method);
    expect(methods).toEqual(["__start", "initialize", "session/new", "session/close"]);
    expect(await detectLoginAcp(AGY, { FAKE_AGY_AUTH: "none", PATH: process.env.PATH ?? "" }, cwd, silentLogger)).toBe("none");
  });

  it("reports installed, version and login for hello, also when agy is missing", async () => {
    const home = scratch();
    expect(await probeAntigravity({ home, env: {}, command: null, log: silentLogger })).toEqual({ cli: "", auth: "none", installed: false });
    expect(await probeAntigravity({ home, env: { GEMINI_API_KEY: "k" }, command: AGY, log: silentLogger })).toEqual({ cli: "1.5.14", auth: "api_key", installed: true });
    expect(await probeAntigravity({ home, env: { FAKE_AGY_AUTH: "none", PATH: process.env.PATH ?? "" }, command: AGY, log: silentLogger })).toEqual({ cli: "1.5.14", auth: "none", installed: true });
  });

  it("pins the daemon's settings and keeps the admin's own", () => {
    const home = scratch();
    mkdirSync(join(home, ".gemini", "antigravity-cli"), { recursive: true });
    writeFileSync(agySettingsFile(home), JSON.stringify({ theme: "dark", toolPermission: "always-proceed", permissions: { allow: ["command(ls)"], deny: ["write_file(/etc)"] } }));
    writeAgySettings(home, { GEMINI_API_KEY: "k" });
    const s = JSON.parse(readFileSync(agySettingsFile(home), "utf8"));
    expect(s).toEqual({
      theme: "dark", toolPermission: "request-review", agentMode: "", allowNonWorkspaceAccess: false, showFeedbackSurvey: false, modelProvider: "gemini",
      permissions: { allow: ["command(ls)", "mcp(platform/*)"], deny: ["write_file(/etc)"] },
    });
    writeAgySettings(home, {});
    expect(JSON.parse(readFileSync(agySettingsFile(home), "utf8")).permissions.allow).toEqual(["command(ls)", "mcp(platform/*)"]);
  });

  it("leaves a settings.json it cannot parse alone", () => {
    const home = scratch();
    mkdirSync(join(home, ".gemini", "antigravity-cli"), { recursive: true });
    const text = '{ // admin notes\n "theme": "dark" }';
    writeFileSync(agySettingsFile(home), text);
    expect(writeAgySettings(home, {})).toBe(false);
    expect(readFileSync(agySettingsFile(home), "utf8")).toBe(text);
  });
});
