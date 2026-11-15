import { isAbsolute, resolve } from "node:path";
import type { CanUseTool, HookCallbackMatcher, HookEvent, McpSdkServerConfigWithInstance, Options, PermissionMode, SettingSource } from "@anthropic-ai/claude-agent-sdk";
import type { ClaudeSessionOptions, SandboxPolicy } from "../../protocol.js";

export interface OptionInputs {
  claude: ClaudeSessionOptions;
  root: string;
  cwd: string;
  env: Record<string, string>;
  sessionId: string;
  resume: boolean;
  server: McpSdkServerConfigWithInstance;
  canUseTool: CanUseTool;
  hooks: Partial<Record<HookEvent, HookCallbackMatcher[]>>;
  secretPaths: string[];
  stderr?: (data: string) => void;
  /** The role's skills, written as a local plugin (sessions/skills.ts). */
  skills?: { pluginDir: string; names: string[] } | null;
}

const WRITE_TOOLS = ["Edit", "Write", "NotebookEdit"];

/** SDK options for one employee session (architecture.md §6.1, protocol.md §4.1 step 2). */
export function buildClaudeOptions(i: OptionInputs): Options {
  const c = i.claude;
  return {
    cwd: i.cwd,
    model: c.model,
    ...(c.effort ? { effort: c.effort as Options["effort"] } : {}),
    tools: c.tools,
    allowedTools: c.allowed_tools,
    disallowedTools: disallowed(c),
    permissionMode: c.permission_mode as PermissionMode,
    allowDangerouslySkipPermissions: c.allow_dangerously_skip_permissions === true,
    settingSources: [] as SettingSource[],
    strictMcpConfig: true,
    mcpServers: { platform: i.server },
    includeHookEvents: true,
    ...(i.resume ? { resume: i.sessionId } : { sessionId: i.sessionId }),
    ...(c.max_turns ? { maxTurns: c.max_turns } : {}),
    ...(c.max_budget_usd ? { maxBudgetUsd: c.max_budget_usd } : {}),
    systemPrompt: { type: "preset", preset: "claude_code", append: c.system_append ?? "" },
    env: i.env,
    settings: buildSettings(c, i.root, i.cwd, i.secretPaths),
    canUseTool: i.canUseTool,
    hooks: i.hooks,
    ...(i.stderr ? { stderr: i.stderr } : {}),
    ...skillOptions(i.skills),
  };
}

/**
 * Skills load from a local plugin (SDK 0.3.325: options.plugins + options.skills; verified by
 * scripts/smoke-skills.ts). settingSources stays [] so nothing is read from the worktree, and
 * skills lists exactly this role's skills so bundled ones are not offered. The Skill tool
 * itself comes from session.open (tools/allowed_tools).
 */
function skillOptions(skills: OptionInputs["skills"]): Partial<Options> {
  if (!skills || skills.names.length === 0) return {};
  return { plugins: [{ type: "local", path: skills.pluginDir, skipMcpDiscovery: true }], skills: skills.names };
}

function disallowed(c: ClaudeSessionOptions): string[] {
  const list = [...(c.disallowed_tools ?? [])];
  if (c.read_only) for (const t of WRITE_TOOLS) if (!list.includes(t)) list.push(t);
  return list;
}

/** Inline flag settings: no claude.ai connectors, Bash sandbox, Read/Edit deny rules for secrets. */
export function buildSettings(c: ClaudeSessionOptions, root: string, cwd: string, secretPaths: string[]): Record<string, unknown> {
  const settings: Record<string, unknown> = { disableClaudeAiConnectors: true };
  if (c.sandbox?.enabled) settings.sandbox = sandboxSettings(c.sandbox, c.read_only === true, root, cwd, secretPaths);
  if (secretPaths.length) settings.permissions = { deny: secretPaths.flatMap((p) => [`Read(/${p}/**)`, `Edit(/${p}/**)`]) };
  return settings;
}

function sandboxSettings(s: SandboxPolicy, readOnly: boolean, root: string, cwd: string, secretPaths: string[]): Record<string, unknown> {
  const write = readOnly ? [] : [...(s.write_paths ?? ["."]), ...(s.extra_write_paths ?? [])].map((p) => absPath(p, root, cwd)).filter(Boolean);
  const denyRead = [...(s.deny_read ?? []).map((p) => absPath(p, root, cwd)).filter(Boolean), ...secretPaths];
  return { enabled: true, allowUnsandboxedCommands: false, filesystem: { allowWrite: write, denyRead } };
}

/** "." is the cwd, other relative paths are root-relative; placeholders like <shared> are dropped. */
export function absPath(p: string, root: string, cwd: string): string {
  if (p.includes("<")) return "";
  if (p === ".") return cwd;
  return isAbsolute(p) ? resolve(p) : resolve(root, p);
}
