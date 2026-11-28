// Custom slash commands (pure): which modes a role may use, local validation mirroring the
// server (composer.ValidateCommands), the platform command toggles and the editor's texts.
import { $t } from "@/i18n";
import type { components } from "@/api/schema";
import { PLATFORM_COMMANDS, platformCommands, type Scope } from "@/components/app/composer/assist";
import { allSlash, promptRuntime, runtimeSpec, type Runtime } from "@/lib/runtimes";

export type CustomCommand = components["schemas"]["CustomCommand"];
type RoleLike = { tier: "coordinator" | "lead" | "member"; runtime: Runtime; runtimes?: Runtime[] | null };

/** Names custom commands may not take: the platform commands, plus "model" (the server keeps it reserved; there is no /model any more). */
const RESERVED = ["model", ...PLATFORM_COMMANDS.map((p) => p.name)];

export const COMMAND_NAME = /^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$/;
export const PROMPT_MAX = 8000;

/** Roles with a runtime without slash commands (Codex over app-server) and members (their messages arrive wrapped) get prompt templates only. */
export const promptOnly = (r: RoleLike) => !allSlash(r) || r.tier === "member";

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

export const modeLabel = (c: Pick<CustomCommand, "mode">) => (c.mode === "passthrough" ? $t("透传") : $t("提示词模板"));

/** Where the role's commands take effect, shown under the editor. */
export function commandsHint(r: RoleLike): string {
  if (r.tier === "member" && !allSlash(r))
    return $t("{runtime} 没有斜杠命令，成员也没有聊天框：组长 send_message 或你直接发给这位成员的消息以 /名字 开头时，平台把提示词模板（{args} 换成后面的文字）展开后转给它。", { runtime: runtimeSpec(promptRuntime(r)).label, args: "{{args}}" });
  if (r.tier === "member") return $t("成员没有聊天框：组长 send_message 或你直接发给这位成员的消息以 /名字 开头时，平台把提示词模板展开后转给它。");
  return $t("在聊天框输入 / 时列出。提示词模板由平台展开后发送；透传把 /名字 原样交给 Claude Code（技能、插件命令或无界面可用的内置命令）。");
}

/** The first problem with a command (""= fine). `taken` are the other commands' names of the role. */
export function commandProblem(c: CustomCommand, taken: string[], onlyPrompt: boolean): string {
  const name = c.name.trim().replace(/^\//, "");
  const prompt = (c.prompt ?? "").trim();
  if (!COMMAND_NAME.test(name)) return $t("命令名用小写字母开头的小写字母、数字、- 和 _，最多 32 个字符");
  if (RESERVED.includes(name)) return $t("/{name} 是平台保留的命令名，换个名字", { name });
  if (taken.includes(name)) return $t("已经有 /{name} 了", { name });
  if (onlyPrompt && c.mode === "passthrough") return $t("这个岗位只能用提示词模板");
  if (c.mode !== "passthrough" && !prompt) return $t("提示词模板不能为空");
  if (c.mode !== "passthrough" && prompt.startsWith("/")) return $t("提示词模板不能以 / 开头（要运行斜杠命令请用透传）");
  if (prompt.length > PROMPT_MAX) return $t("提示词模板最多 {PROMPT_MAX} 字", { PROMPT_MAX });
  if ((c.description ?? "").length > 200) return $t("说明最多 200 字");
  return "";
}

/** The command as saved: trimmed, no slash, passthrough without a template. */
export function cleanCommand(c: CustomCommand): CustomCommand {
  const mode = c.mode === "passthrough" ? "passthrough" : "prompt";
  return { name: c.name.trim().replace(/^\//, ""), description: (c.description ?? "").trim(), mode, ...(mode === "prompt" ? { prompt: (c.prompt ?? "").trim() } : {}) };
}

/** Commands of a role that no longer fit after its runtime changed (passthrough on a prompt-only role). */
export const unfit = (r: RoleLike & { commands?: CustomCommand[] | null }) => (promptOnly(r) ? (r.commands ?? []).filter((c) => c.mode === "passthrough") : []);
