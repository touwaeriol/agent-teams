import { chmodSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { PluginBundle, PluginFile, PluginMcpServer, SessionOpen } from "../protocol.js";
import { isInside, resolveInRoot } from "../util/paths.js";
import { isMarkdown, stripToolGrants } from "./frontmatter.js";
import type { AcpMcpServer } from "./antigravity/driver.js";
import { employeeDir } from "./skills.js";

const SAFE_NAME = /^[a-z0-9][a-z0-9-]{0,63}$/;
const ROOT_VAR = /\$\{CLAUDE_PLUGIN_ROOT\}/g;

/** One plugin written to disk, with what the runtimes need to load it. */
export interface MaterializedPlugin {
  name: string;
  version: number;
  /** <pluginsRoot>/<project>/<thread>/<employee>/<name>@<version> */
  dir: string;
  /** Qualified skill and command names for SDK options.skills. */
  skills: string[];
  /** MCP servers keyed plugin_<plugin>_<server>, ${CLAUDE_PLUGIN_ROOT} replaced by dir. */
  mcpServers: Record<string, PluginMcpServer>;
}

/**
 * Writes the session's plugins (session.open.plugins), each at its exact version, into a
 * per-employee directory under <pluginsRoot> — outside every worktree — and returns what the
 * SDK needs (architecture.md §6.5). The directory is rebuilt on each open, so a removed or
 * re-versioned plugin never lingers. The coordinator never gets plugins.
 */
export function materializePlugins(pluginsRoot: string, spec: Pick<SessionOpen, "employee" | "plugins">): MaterializedPlugin[] {
  // The coordinator only has platform tools; plugins are ignored even if a console sent them.
  const plugins = spec.employee.tier === "coordinator" ? [] : (spec.plugins ?? []);
  const base = employeeDir(pluginsRoot, spec.employee);
  rmSync(base, { recursive: true, force: true });
  if (plugins.length === 0) return [];
  mkdirSync(base, { recursive: true, mode: 0o700 });
  return plugins.map((p) => writePlugin(base, p));
}

function writePlugin(base: string, p: PluginBundle): MaterializedPlugin {
  if (!SAFE_NAME.test(p.name) || !Number.isInteger(p.version) || p.version < 1) throw new Error(`bad plugin ${p.name}@${p.version}`);
  const dir = join(base, `${p.name}@${p.version}`);
  for (const f of p.files) writePluginFile(dir, f);
  return { name: p.name, version: p.version, dir, skills: p.skills ?? [], mcpServers: pluginServers(p, dir) };
}

function writePluginFile(dir: string, f: PluginFile): void {
  if (f.path.split("/").some((s) => s === "." || s === ".." || s === "")) throw new Error(`bad plugin file ${f.path}`);
  const abs = resolveInRoot(dir, f.path);
  if (!isInside(dir, abs)) throw new Error(`bad plugin file ${f.path}`);
  mkdirSync(dirname(abs), { recursive: true });
  writeFileSync(abs, fileBytes(f));
  chmodSync(abs, f.executable ? 0o755 : 0o644);
}

/** File content as written: base64 decoded; Markdown (even if sent as binary) as text without tool grants. */
function fileBytes(f: PluginFile): Buffer | string {
  const raw = f.encoding === "base64" ? Buffer.from(f.content, "base64") : f.content;
  return isMarkdown(f.path) ? stripToolGrants(raw.toString()) : raw;
}

function pluginServers(p: PluginBundle, dir: string): Record<string, PluginMcpServer> {
  const out: Record<string, PluginMcpServer> = {};
  for (const [name, s] of Object.entries(p.mcp_servers ?? {})) out[`plugin_${p.name}_${name}`] = withRoot(s, dir);
  return out;
}

/** Replaces ${CLAUDE_PLUGIN_ROOT} everywhere the CLI would (the host owns these servers). */
export function withRoot(s: PluginMcpServer, dir: string): PluginMcpServer {
  const sub = (v: string) => v.replace(ROOT_VAR, dir);
  const map = (m?: Record<string, string>) => (m ? Object.fromEntries(Object.entries(m).map(([k, v]) => [k, sub(v)])) : undefined);
  const out: PluginMcpServer = { type: s.type };
  if (s.command) out.command = sub(s.command);
  if (s.args) out.args = s.args.map(sub);
  if (s.env) out.env = map(s.env)!;
  if (s.url) out.url = sub(s.url);
  if (s.headers) out.headers = map(s.headers)!;
  return out;
}

/** All plugin MCP servers of a session, ready for SDK options.mcpServers. */
export function sdkMcpServers(plugins: MaterializedPlugin[]): Record<string, SdkServer> {
  const out: Record<string, SdkServer> = {};
  for (const p of plugins) for (const [k, s] of Object.entries(p.mcpServers)) out[k] = toSdk(s);
  return out;
}

export type SdkServer =
  | { type: "stdio"; command: string; args?: string[]; env?: Record<string, string> }
  | { type: "http" | "sse"; url: string; headers?: Record<string, string> };

function toSdk(s: PluginMcpServer): SdkServer {
  if (s.type === "stdio") return { type: "stdio", command: s.command ?? "", ...(s.args ? { args: s.args } : {}), ...(s.env ? { env: s.env } : {}) };
  return { type: s.type, url: s.url ?? "", ...(s.headers ? { headers: s.headers } : {}) };
}

/** Plugin MCP servers in Codex config form; Codex has no SSE transport, so those are left out. */
export function codexMcpServers(plugins: MaterializedPlugin[]): Record<string, Record<string, unknown>> {
  const out: Record<string, Record<string, unknown>> = {};
  for (const p of plugins) {
    for (const [k, s] of Object.entries(p.mcpServers)) {
      if (s.type === "stdio") out[k] = { command: s.command, ...(s.args ? { args: s.args } : {}), ...(s.env ? { env: s.env } : {}) };
      if (s.type === "http") out[k] = { url: s.url, ...(s.headers ? { http_headers: s.headers } : {}) };
    }
  }
  return out;
}

/** Plugin MCP servers in ACP session/new form (Antigravity): stdio, http and sse. */
export function acpMcpServers(plugins: MaterializedPlugin[]): AcpMcpServer[] {
  const out: AcpMcpServer[] = [];
  const pairs = (m?: Record<string, string>) => Object.entries(m ?? {}).map(([name, value]) => ({ name, value }));
  for (const p of plugins) {
    for (const [name, s] of Object.entries(p.mcpServers)) {
      if (s.type === "stdio") out.push({ name, command: s.command ?? "", args: s.args ?? [], env: pairs(s.env) });
      else out.push({ type: s.type, name, url: s.url ?? "", headers: pairs(s.headers) });
    }
  }
  return out;
}
