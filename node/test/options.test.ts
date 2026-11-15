import { describe, expect, it } from "vitest";
import { buildClaudeOptions, buildSettings } from "../src/sessions/claude/options.js";
import { sessionScoped, toPermissionResult } from "../src/sessions/claude/permissions.js";
import { buildEnv, gitIdentity } from "../src/sessions/env.js";
import { mapClaudeMessage, describeTool } from "../src/sessions/claude/mapper.js";
import type { ClaudeSessionOptions } from "../src/protocol.js";

const claude = (o: Partial<ClaudeSessionOptions> = {}): ClaudeSessionOptions => ({ model: "claude-opus-5-5", tools: ["Read", "Glob", "Grep", "Bash"], allowed_tools: ["mcp__platform__*"], permission_mode: "dontAsk", setting_sources: [], ...o });

describe("Claude options", () => {
  const base = { root: "/r", cwd: "/r/p/lead", env: { PATH: "/bin" }, sessionId: "u1", resume: false, server: {} as never, canUseTool: (async () => ({ behavior: "deny", message: "" })) as never, hooks: {}, secretPaths: ["/home/u/.config/agent-teams"] };

  it("fixes settingSources, explicit permission mode and read-only tools", () => {
    const o = buildClaudeOptions({ ...base, claude: claude({ read_only: true, disallowed_tools: ["WebFetch"] }) });
    expect(o.settingSources).toEqual([]);
    expect(o.permissionMode).toBe("dontAsk");
    expect(o.disallowedTools).toEqual(["WebFetch", "Edit", "Write", "NotebookEdit"]);
    expect(o.sessionId).toBe("u1");
    expect(o.resume).toBeUndefined();
    expect(o.systemPrompt).toEqual({ type: "preset", preset: "claude_code", append: "" });
    expect(buildClaudeOptions({ ...base, resume: true, claude: claude() }).resume).toBe("u1");
  });

  it("builds sandbox settings with no writable paths for read-only sessions", () => {
    const s = buildSettings(claude({ read_only: true, sandbox: { enabled: true, write_paths: ["."], extra_write_paths: ["projects/p/repo/.git"] } }), "/r", "/r/p/lead", ["/s"]);
    expect(s).toMatchObject({ disableClaudeAiConnectors: true, sandbox: { enabled: true, allowUnsandboxedCommands: false, filesystem: { allowWrite: [], denyRead: ["/s"] } } });
    const w = buildSettings(claude({ sandbox: { enabled: true, write_paths: ["."], extra_write_paths: ["projects/p/repo/.git", "<shared>/.git"] } }), "/r", "/r/t/dev", []);
    expect((w.sandbox as { filesystem: { allowWrite: string[] } }).filesystem.allowWrite).toEqual(["/r/t/dev", "/r/projects/p/repo/.git"]);
    expect(s.permissions).toEqual({ deny: ["Read(//s/**)", "Edit(//s/**)"] });
  });

  it("maps approval decisions and rewrites suggestion destinations to session", () => {
    const sugg = [{ type: "addRules", rules: [{ toolName: "Bash", ruleContent: "pnpm add:*" }], behavior: "allow", destination: "localSettings" }] as never;
    expect(toPermissionResult({ approval_id: "a", decision: "deny", decided_by: "u", message: "no" }, {}, sugg)).toEqual({ behavior: "deny", message: "no" });
    expect(toPermissionResult({ approval_id: "a", decision: "allow", decided_by: "u" }, { c: 1 }, sugg)).toEqual({ behavior: "allow", updatedInput: { c: 1 } });
    const always = toPermissionResult({ approval_id: "a", decision: "allow_always", decided_by: "u", updated_input: { c: 2 } }, { c: 1 }, sugg);
    expect(always).toMatchObject({ behavior: "allow", updatedInput: { c: 2 }, updatedPermissions: [{ destination: "session" }] });
    expect(sessionScoped(sugg)[0]).toMatchObject({ destination: "session", type: "addRules" });
  });
});

describe("session env", () => {
  it("builds env item by item and never passes AGENT_TEAMS_*", () => {
    const env = buildEnv({
      base: { PATH: "/usr/bin", HOME: "/home/u", AGENT_TEAMS_NODE_TOKEN: "secret", ANTHROPIC_API_KEY: "k", AWS_REGION: "x", RANDOM: "no" },
      passList: ["ANTHROPIC_API_KEY", "AWS_*", "AGENT_TEAMS_NODE_TOKEN"], home: "/r/agent-home", extra: { LANG: "C.UTF-8", AGENT_TEAMS_X: "y" }, fixed: { MCP_TOOL_TIMEOUT: "86400000" }, pathPrefix: ["/p/bin"],
    });
    expect(env).toEqual({ PATH: "/p/bin:/usr/bin", HOME: "/r/agent-home", LANG: "C.UTF-8", ANTHROPIC_API_KEY: "k", AWS_REGION: "x", MCP_TOOL_TIMEOUT: "86400000" });
  });
});

describe("Claude message mapping", () => {
  it("maps assistant, result, rate limits and status messages", () => {
    expect(mapClaudeMessage({ type: "assistant", message: { content: [{ type: "thinking" }, { type: "text", text: "hi" }] } } as never)).toEqual([{ kind: "thinking" }, { kind: "text", text: "hi", final: false }]);
    expect(mapClaudeMessage({ type: "assistant", error: "authentication_failed", message: { content: [] } } as never)).toEqual([{ kind: "error", code: "auth_failed", message: "authentication_failed", fatal: false }]);
    const ended = mapClaudeMessage({ type: "result", subtype: "success", result: "ok", is_error: false, num_turns: 2, duration_ms: 9, total_cost_usd: 0.1, usage: { input_tokens: 1 } } as never);
    expect(ended).toEqual([{ kind: "text", text: "ok", final: true }, { kind: "turn_ended", result_subtype: "success", is_error: false, num_turns: 2, duration_ms: 9, cost_usd: 0.1, usage: { input_tokens: 1 } }]);
    expect(mapClaudeMessage({ type: "rate_limit_event", rate_limit_info: { status: "rejected", resetsAt: 1800000000, rateLimitType: "five_hour" } } as never)).toEqual([{ kind: "rate_limited", detail: "five_hour", retry_at: new Date(1800000000 * 1000).toISOString() }]);
    expect(mapClaudeMessage({ type: "rate_limit_event", rate_limit_info: { status: "allowed" } } as never)).toEqual([]);
    expect(mapClaudeMessage({ type: "system", subtype: "status", status: "compacting" } as never)).toEqual([{ kind: "compacting", on: true }]);
    expect(mapClaudeMessage({ type: "system", subtype: "api_retry", attempt: 1, max_retries: 3, retry_delay_ms: 1000, error: "rate_limit" } as never)[0]!.kind).toBe("rate_limited");
  });

  it("summarizes tools without secrets", () => {
    expect(describeTool("Bash", { command: "GH_TOKEN=ghs_abcdefghijklmnop pytest -q tests" })).toEqual({ category: "shell", subcategory: "test", summary: "GH_TOKEN=*** pytest -q tests" });
    expect(describeTool("mcp__platform__complete_task", { task_id: "t1" })).toMatchObject({ category: "platform", summary: "complete_task" });
    expect(describeTool("Read", { file_path: "/a/b.ts" })).toMatchObject({ category: "read", summary: "/a/b.ts" });
  });
});

describe("gitIdentity", () => {
  it("names commits after the employee and lets spec.env override it", () => {
    const id = gitIdentity("dev-1");
    expect(id).toEqual({ GIT_AUTHOR_NAME: "dev-1", GIT_AUTHOR_EMAIL: "dev-1@agent-teams.local", GIT_COMMITTER_NAME: "dev-1", GIT_COMMITTER_EMAIL: "dev-1@agent-teams.local" });
    expect(gitIdentity("前端 1").GIT_AUTHOR_EMAIL).toBe("---1@agent-teams.local");
    const env = buildEnv({ base: {}, passList: [], home: "/h", extra: { ...id, GIT_AUTHOR_NAME: "custom" } });
    expect(env.GIT_AUTHOR_NAME).toBe("custom");
    expect(env.GIT_COMMITTER_NAME).toBe("dev-1");
  });
});
