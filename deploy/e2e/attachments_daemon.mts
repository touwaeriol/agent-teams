// A real node daemon whose AI is the test stand-in (node/test/helpers/fake-query.ts), for
// run_attachments.mjs. E2E_NODE_SRC picks the daemon's source tree (default: this repository's
// node/; an older checkout's node/ plays the old daemon). Run with that tree's tsx:
//   E2E_NODE_SRC=<tree>/node E2E_PROMPT_LOG=<file> <tree>/node/node_modules/.bin/tsx attachments_daemon.mts run --console … --token …
// Every user message the runtime gets is logged to E2E_PROMPT_LOG (session cwd, text, the content
// blocks' types and sizes). Lines of the message drive the stand-in:
//   @@sh <command>         runs in the session's working directory first (output logged)
//   @@use <tool> <json>    calls the platform tool (the reply is "tool ok: …" / "tool error: …")
import { execSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const src = process.env.E2E_NODE_SRC ?? join(dirname(fileURLToPath(import.meta.url)), "..", "..", "node");
const logFile = process.env.E2E_PROMPT_LOG;
const { loadConfig } = await import(join(src, "src/config.ts"));
const { Daemon } = await import(join(src, "src/daemon.ts"));
const { createLogger } = await import(join(src, "src/util/log.ts"));
const { fakeQueryFactory } = await import(join(src, "test/helpers/fake-query.ts"));

type Block = { type: string; text?: string; source?: { media_type?: string; data?: string } };
type UserMessage = { message: { content: string | Block[] } } & Record<string, unknown>;

const fake = fakeQueryFactory({ options: [], prompts: [], closed: 0, interrupts: 0, modes: [] });

function summary(content: string | Block[]): { text: string; blocks: Array<{ type: string; media_type?: string; bytes?: number }> } {
  if (typeof content === "string") return { text: content, blocks: [] };
  const text = content.filter((b) => b.type === "text").map((b) => b.text ?? "").join("\n");
  const blocks = content.filter((b) => b.type !== "text").map((b) => ({
    type: b.type, media_type: b.source?.media_type, bytes: b.source?.data ? Buffer.from(b.source.data, "base64").length : undefined,
  }));
  return { text, blocks };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function queryFn(params: { prompt: AsyncIterable<UserMessage>; options: { cwd?: string } & Record<string, unknown> }): any {
  const cwd = params.options.cwd ?? process.cwd();
  async function* prompt(): AsyncIterable<UserMessage> {
    for await (const msg of params.prompt) {
      const s = summary(msg.message.content);
      const sh: Array<{ cmd: string; out: string }> = [];
      for (const m of s.text.matchAll(/^@@sh (.+)$/gm)) {
        let out: string;
        try {
          out = execSync(m[1]!, { cwd, encoding: "utf8", shell: "/bin/bash", timeout: 600_000 });
        } catch (e) {
          out = `ERROR ${(e as Error).message}`;
        }
        sh.push({ cmd: m[1]!, out: out.slice(0, 2000) });
      }
      if (logFile) appendFileSync(logFile, JSON.stringify({ at: new Date().toISOString(), cwd, ...s, sh }) + "\n");
      const use = /^@@use (\w+) (.+)$/m.exec(s.text);
      yield use ? { ...msg, message: { ...msg.message, content: `use ${use[1]} ${use[2]}` } } : msg;
    }
  }
  const q = fake({ ...params, prompt: prompt() });
  // A logged-in subscription, so the runtime check reports Claude ready and sessions are scheduled here.
  const init = q.initializationResult.bind(q);
  q.initializationResult = async () => ({ ...(await init()), account: { subscriptionType: "max" } });
  return q;
}

const config = loadConfig(process.argv.slice(3));
const log = createLogger(config.logLevel);
const daemon = new Daemon({ ...config, url: config.wsUrl, log, nodeCmd: [process.execPath, "agent-teams-node"], probeRuntimes: false, runtimeMonitor: { intervalMs: 0 }, isolation: "none", queryFn });
const stop = () => void daemon.stop().finally(() => process.exit(0));
process.once("SIGTERM", stop);
process.once("SIGINT", stop);
await daemon.start();
log.info({ console: config.consoleUrl, root: config.root, src }, "e2e attachments daemon started");
