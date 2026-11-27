import { isAbsolute, resolve } from "node:path";
import type { CanUseTool, HookCallbackMatcher, HookEvent, McpSdkServerConfigWithInstance, Options, PermissionMode, SettingSource } from "@anthropic-ai/claude-agent-sdk";
import type { ClaudeSessionOptions, SandboxPolicy, SessionOpen } from "../../protocol.js";
import { sdkMcpServers, type MaterializedPlugin } from "../plugins.js";
import { claudeCompactEnv } from "./compact.js";

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
  /** The role's plugins at their exact versions (sessions/plugins.ts). */
  plugins?: MaterializedPlugin[];
  /** The role's context management (session.open context): sets the autocompact window and threshold. */
  context?: SessionOpen["context"];
  /** Platform text appended after system_append (DriverContext.systemExtra). */
  systemExtra?: string;
  /** Directories outside cwd the file tools may use (the shared folder). */
  additionalDirectories?: string[];
}

const WRITE_TOOLS = ["Edit", "Write", "NotebookEdit"];
const MEMORY_WRITE_TOOLS = ["Edit", "Write"];

/** SDK options for one employee session (architecture.md §6.1, protocol.md §4.1 step 2). */
export function buildClaudeOptions(i: OptionInputs): Options {
  const c = i.claude;
  return {
    cwd: i.cwd,
    ...(i.additionalDirectories?.length ? { additionalDirectories: i.additionalDirectories } : {}),
    model: c.model,
    ...(c.effort ? { effort: c.effort as Options["effort"] } : {}),
    tools: c.tools,
    allowedTools: c.allowed_tools,
    disallowedTools: disallowed(c),
    // auto opens as default: the driver switches once the model says it supports it (ClaudeDriver.preferAuto).
    permissionMode: (c.permission_mode === "auto" ? "default" : c.permission_mode) as PermissionMode,
    allowDangerouslySkipPermissions: c.allow_dangerously_skip_permissions === true || c.permission_mode === "bypassPermissions",
    settingSources: [] as SettingSource[],
    strictMcpConfig: true,
    mcpServers: { ...(sdkMcpServers(i.plugins ?? []) as Options["mcpServers"]), platform: i.server },
    includeHookEvents: true,
    ...(i.resume ? { resume: i.sessionId } : { sessionId: i.sessionId }),
    ...(c.max_turns ? { maxTurns: c.max_turns } : {}),
    ...(c.max_budget_usd ? { maxBudgetUsd: c.max_budget_usd } : {}),
    systemPrompt: { type: "preset", preset: "claude_code", append: [c.system_append, i.systemExtra].filter(Boolean).join("\n\n") },
    env: { ...i.env, ...claudeCompactEnv(i.context) },
    settings: buildSettings(c, i.root, i.cwd, i.secretPaths),
    canUseTool: i.canUseTool,
    hooks: i.hooks,
    ...(i.stderr ? { stderr: i.stderr } : {}),
    ...pluginOptions(i.skills, i.plugins ?? []),
  };
}

/**
 * Skills and plugins load as local plugins (SDK 0.3.325; verified by scripts/smoke-skills.ts
 * and scripts/smoke-plugins.ts). settingSources stays [] so nothing is read from the worktree.
 * - skills lists exactly the role's skills plus each plugin's skills and commands (plugin
 *   commands are invoked through the Skill tool and are refused unless listed), so bundled
 *   and user-level skills are not offered.
 * - Plugins load with skipMcpDiscovery: strictMcpConfig (kept on) would ignore a plugin's
 *   .mcp.json anyway, so the daemon passes the plugin's MCP servers itself in mcpServers
 *   (plugin_<plugin>_<server>); their tools are not pre-approved and go through canUseTool.
 * - Plugin agents run through the Agent tool and hooks fire from the plugin's hooks.json.
 * The Skill / Agent tools themselves come from session.open (tools/allowed_tools).
 */
function pluginOptions(skills: OptionInputs["skills"], plugins: MaterializedPlugin[]): Partial<Options> {
  const dirs = [...(skills && skills.names.length ? [skills.pluginDir] : []), ...plugins.map((p) => p.dir)];
  if (dirs.length === 0) return {};
  const names = [...(skills?.names ?? []), ...plugins.flatMap((p) => p.skills)];
  return { plugins: dirs.map((path) => ({ type: "local" as const, path, skipMcpDiscovery: true })), skills: names };
}

function disallowed(c: ClaudeSessionOptions): string[] {
  const list = [...(c.disallowed_tools ?? [])];
  // memory_files keeps Edit / Write: the PreToolUse hook holds them inside the memory directories.
  const write = c.memory_files ? WRITE_TOOLS.filter((t) => !MEMORY_WRITE_TOOLS.includes(t)) : WRITE_TOOLS;
  if (c.read_only) for (const t of write) if (!list.includes(t)) list.push(t);
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
