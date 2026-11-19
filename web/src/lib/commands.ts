// Custom slash commands (pure): which modes a role may use, local validation mirroring the
// server (composer.ValidateCommands), the platform command toggles and the editor's texts.
import type { components } from "@/api/schema";
import { PLATFORM_COMMANDS, platformCommands, type Scope } from "@/components/app/composer/assist";

export type CustomCommand = components["schemas"]["CustomCommand"];
type RoleLike = { tier: "coordinator" | "lead" | "member"; runtime: "claude" | "codex" };

/** Names custom commands may not take: the platform commands, plus "model" (the server keeps it reserved; there is no /model any more). */
const RESERVED = ["model", ...PLATFORM_COMMANDS.map((p) => p.name)];

export const COMMAND_NAME = /^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$/;
export const PROMPT_MAX = 8000;

/** Codex (no slash commands over app-server) and members (their messages arrive wrapped) get prompt templates only. */
export const promptOnly = (r: RoleLike) => r.runtime === "codex" || r.tier === "member";

/** The chat scope of a role's employee; members have no chat box (no platform commands). */
export const chatScope = (r: Pick<RoleLike, "tier">): Scope | null => (r.tier === "coordinator" ? "coordinator" : r.tier === "lead" ? "lead" : null);

/** The platform commands a role's chat can show (for the toggles). */
export const togglable = (r: Pick<RoleLike, "tier">) => {
  const scope = chatScope(r);
  return scope ? platformCommands(scope) : [];
};

export const toggleHidden = (hidden: string[] | null | undefined, name: string): string[] => {
  const list = hidden ?? [];
  return list.includes(name) ? list.filter((n) => n !== name) : [...list, name];
};

export const modeLabel = (c: Pick<CustomCommand, "mode">) => (c.mode === "passthrough" ? "透传" : "提示词模板");

/** Where the role's commands take effect, shown under the editor. */
export function commandsHint(r: RoleLike): string {
  if (r.tier === "member" && r.runtime === "codex")
    return "Codex 没有斜杠命令，成员也没有聊天框：组长 send_message 或你直接发给这位成员的消息以 /名字 开头时，平台把提示词模板（{{args}} 换成后面的文字）展开后转给它。";
  if (r.tier === "member") return "成员没有聊天框：组长 send_message 或你直接发给这位成员的消息以 /名字 开头时，平台把提示词模板展开后转给它。";
  return "在聊天框输入 / 时列出。提示词模板由平台展开后发送；透传把 /名字 原样交给 Claude Code（技能、插件命令或无界面可用的内置命令）。";
}

/** The first problem with a command (""= fine). `taken` are the other commands' names of the role. */
export function commandProblem(c: CustomCommand, taken: string[], onlyPrompt: boolean): string {
  const name = c.name.trim().replace(/^\//, "");
  const prompt = (c.prompt ?? "").trim();
  if (!COMMAND_NAME.test(name)) return "命令名用小写字母开头的小写字母、数字、- 和 _，最多 32 个字符";
  if (RESERVED.includes(name)) return `/${name} 是平台保留的命令名，换个名字`;
  if (taken.includes(name)) return `已经有 /${name} 了`;
  if (onlyPrompt && c.mode === "passthrough") return "这个岗位只能用提示词模板";
  if (c.mode !== "passthrough" && !prompt) return "提示词模板不能为空";
  if (c.mode !== "passthrough" && prompt.startsWith("/")) return "提示词模板不能以 / 开头（要运行斜杠命令请用透传）";
  if (prompt.length > PROMPT_MAX) return `提示词模板最多 ${PROMPT_MAX} 字`;
  if ((c.description ?? "").length > 200) return "说明最多 200 字";
  return "";
}

/** The command as saved: trimmed, no slash, passthrough without a template. */
export function cleanCommand(c: CustomCommand): CustomCommand {
  const mode = c.mode === "passthrough" ? "passthrough" : "prompt";
  return { name: c.name.trim().replace(/^\//, ""), description: (c.description ?? "").trim(), mode, ...(mode === "prompt" ? { prompt: (c.prompt ?? "").trim() } : {}) };
}

/** Commands of a role that no longer fit after its runtime changed (passthrough on a prompt-only role). */
export const unfit = (r: RoleLike & { commands?: CustomCommand[] | null }) => (promptOnly(r) ? (r.commands ?? []).filter((c) => c.mode === "passthrough") : []);
