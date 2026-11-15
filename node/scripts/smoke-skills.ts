// Real smoke test: a role's skills reach a Claude session through the daemon's own code
// (materializeSkills + buildClaudeOptions: options.plugins + options.skills on SDK 0.3.325),
// using this machine's Claude Code login. One cheap call.
//   npx tsx scripts/smoke-skills.ts
import { mkdtempSync, readFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { query, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import { buildClaudeOptions } from "../src/sessions/claude/options.js";
import { materializeSkills } from "../src/sessions/skills.js";
import { platformSdkServer } from "../src/toolhost/sdk-server.js";
import { AsyncQueue } from "../src/util/async.js";

const dir = mkdtempSync(join(tmpdir(), "at-smoke-skills-"));
const design = readFileSync(new URL("../../server/internal/skills/builtin/design/SKILL.md", import.meta.url), "utf8");
const docs = readFileSync(new URL("../../server/internal/skills/builtin/docs/SKILL.md", import.meta.url), "utf8");

function check(cond: unknown, what: string): void {
  if (!cond) throw new Error(`smoke failed: ${what}`);
  console.log(`ok - ${what}`);
}

async function main(): Promise<void> {
  const skills = materializeSkills(join(dir, "agent-skills"), {
    employee: { name: "designer-1", tier: "member", project_id: "smoke", thread_no: 1 },
    skills: [{ name: "design", files: [{ path: "SKILL.md", content: design }] }, { name: "docs", files: [{ path: "SKILL.md", content: docs }] }],
  });
  check(skills?.names.length === 2, "skills written as a local plugin");
  const prompt = new AsyncQueue<SDKUserMessage>();
  const options = buildClaudeOptions({
    claude: { model: "claude-opus-5-5", tools: ["Read", "Skill"], allowed_tools: ["mcp__platform__*", "Skill"], permission_mode: "dontAsk", setting_sources: [], max_turns: 4 },
    root: dir, cwd: dir, env: { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: homedir() }, sessionId: crypto.randomUUID(), resume: false,
    server: platformSdkServer([], async () => ({ content: [] })), canUseTool: async () => ({ behavior: "deny", message: "no" }), hooks: {}, secretPaths: [], skills,
  });
  const q = query({ prompt, options });
  prompt.push({ type: "user", session_id: "", parent_tool_use_id: null, message: { role: "user", content: "List the exact names of the skills in your skill listing. Then load the design skill with the Skill tool and quote the heading of its section 2. Be terse." } });
  let text = "";
  let loaded = "";
  for await (const m of q) {
    if (m.type === "system" && m.subtype === "init") console.log("init skills:", JSON.stringify(m.skills), "tools:", JSON.stringify(m.tools));
    if (m.type === "assistant") {
      for (const b of m.message.content) {
        if (b.type === "text") text += b.text;
        if (b.type === "tool_use" && b.name === "Skill") loaded = JSON.stringify(b.input);
      }
    }
    if (m.type === "result") break;
  }
  prompt.end();
  q.close();
  console.log("reply:", text);
  check(/agent-teams-skills:design/.test(text) && /agent-teams-skills:docs/.test(text), "the model lists the role's skills");
  check(!/dataviz|claude-api|code-review/.test(text), "bundled skills are filtered out");
  check(loaded.includes("design"), "the model loaded the design skill with the Skill tool");
  check(/Build rules/i.test(text), "the skill's content reached the model");
}

main().then(() => process.exit(0), (err) => {
  console.error(err);
  process.exit(1);
});
