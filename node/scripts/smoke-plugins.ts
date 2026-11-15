// Real smoke test: a role's plugin reaches a Claude session through the daemon's own code
// (materializePlugins + buildClaudeOptions on SDK 0.3.325), using this machine's Claude Code
// login. The fixture plugin (scripts/fixtures/tinyplug) has a skill, a command, a subagent, a
// UserPromptSubmit hook and a stdio MCP server. One cheap call.
//   npx tsx scripts/smoke-plugins.ts
import { existsSync, mkdtempSync, readdirSync, readFileSync, statSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, relative } from "node:path";
import { query, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import type { PluginBundle, PluginFile } from "../src/protocol.js";
import { buildClaudeOptions } from "../src/sessions/claude/options.js";
import { materializePlugins } from "../src/sessions/plugins.js";
import { platformSdkServer } from "../src/toolhost/sdk-server.js";
import { AsyncQueue } from "../src/util/async.js";

const fixture = new URL("./fixtures/tinyplug", import.meta.url).pathname;
const dir = mkdtempSync(join(tmpdir(), "at-smoke-plugins-"));

function check(cond: unknown, what: string): void {
  if (!cond) throw new Error(`smoke failed: ${what}`);
  console.log(`ok - ${what}`);
}

function filesOf(root: string, at = root): PluginFile[] {
  return readdirSync(at).flatMap((name) => {
    const p = join(at, name);
    return statSync(p).isDirectory() ? filesOf(root, p) : [{ path: relative(root, p), content: readFileSync(p, "utf8") }];
  });
}

// What the console sends for this plugin (server/internal/plugins parses the same layout).
const bundle: PluginBundle = {
  name: "tinyplug", version: 1, files: filesOf(fixture) as PluginBundle["files"],
  skills: ["tinyplug:greet", "tinyplug:shout"], agents: ["tinyplug:echoer"],
  mcp_servers: { calc: { type: "stdio", command: "node", args: ["${CLAUDE_PLUGIN_ROOT}/mcp/server.mjs"] } },
};

async function main(): Promise<void> {
  const plugins = materializePlugins(join(dir, "agent-plugins"), { employee: { name: "dev-1", tier: "member", project_id: "smoke", thread_no: 1 }, plugins: [bundle] });
  check(plugins.length === 1 && existsSync(join(plugins[0]!.dir, ".claude-plugin", "plugin.json")), "plugin written at its exact version");
  const asked: string[] = [];
  const prompt = new AsyncQueue<SDKUserMessage>();
  const options = buildClaudeOptions({
    claude: { model: "claude-opus-5-5", tools: ["Read", "Skill", "Agent"], allowed_tools: ["mcp__platform__*", "Skill"], permission_mode: "default", setting_sources: [], max_turns: 8 },
    root: dir, cwd: dir, env: { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: homedir() }, sessionId: crypto.randomUUID(), resume: false,
    server: platformSdkServer([], async () => ({ content: [] })), hooks: {}, secretPaths: [], plugins,
    canUseTool: async (tool, input) => {
      asked.push(tool);
      return { behavior: "allow", updatedInput: input };
    },
  });
  const q = query({ prompt, options });
  prompt.push({ type: "user", session_id: "", parent_tool_use_id: null, message: { role: "user", content: "Do these four things, each with a tool, then report the four code words on one line: (1) load the tinyplug:greet skill with the Skill tool; (2) run the tinyplug:shout command with the Skill tool; (3) ask the tinyplug:echoer subagent with the Agent tool; (4) call the fruit MCP tool. Be terse." } });
  let text = "";
  const used: string[] = [];
  for await (const m of q) {
    if (m.type === "system" && m.subtype === "init") console.log("init tools:", JSON.stringify(m.tools.filter((t) => t !== "Read")), "plugins:", JSON.stringify((m as { plugins?: unknown }).plugins));
    if (m.type === "assistant") {
      for (const b of m.message.content) {
        if (b.type === "text") text += b.text;
        if (b.type === "tool_use") used.push(`${b.name} ${JSON.stringify(b.input)}`);
      }
    }
    if (m.type === "result") {
      console.log("cost usd:", m.total_cost_usd);
      break;
    }
  }
  prompt.end();
  q.close();
  console.log("tool uses:", used);
  console.log("canUseTool asked for:", asked);
  console.log("reply:", text);
  check(/PINEAPPLE-42/.test(text), "plugin skill loaded through the Skill tool");
  check(/MANGO-7/.test(text), "plugin command run through the Skill tool");
  check(/KIWI-9/.test(text) && used.some((u) => u.includes("tinyplug:echoer")), "plugin subagent run through the Agent tool");
  check(/BANANA-3/.test(text) && asked.includes("mcp__plugin_tinyplug_calc__fruit"), "plugin MCP tool called after a canUseTool approval");
  check(existsSync(join(plugins[0]!.dir, "hook-ran")), "plugin hook fired");
}

main().then(() => process.exit(0), (err) => {
  console.error(err);
  process.exit(1);
});
