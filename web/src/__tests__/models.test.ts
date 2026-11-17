// Model lists and custom commands (pure parts): choices per runtime, the user's default, list
// problems, command validation, the role card hints and the /model toast.
import { describe, expect, it } from "vitest";
import { modelAppliedText } from "@/components/app/composer/panels";
import { chatScope, cleanCommand, commandProblem, commandsHint, promptOnly, toggleHidden, togglable, unfit } from "@/lib/commands";
import { defaultModel, MODEL_ID, modelChoices, modelName, modelProblems } from "@/lib/models";
import { newRole, roleProblems } from "@/lib/roles";

const list = [
  { id: "m1", runtime: "claude" as const, model: "claude-haiku-x", label: "Haiku", default: false, mine: true },
  { id: "m2", runtime: "codex" as const, model: "gpt-x", label: "", default: true, mine: true },
  { id: "m3", runtime: "claude" as const, model: "claude-opus-5-5", label: "Opus 5.5", default: true, mine: true },
  { id: "m4", runtime: "claude" as const, model: "claude-opus-5-5", label: "again", default: false, mine: true },
];

describe("model lists", () => {
  it("lists one runtime's models once, in the user's order, and keeps a typed id", () => {
    expect(modelChoices(list, "claude").map((c) => [c.value, c.label, c.isDefault, c.custom])).toEqual([
      ["claude-haiku-x", "Haiku", false, false],
      ["claude-opus-5-5", "Opus 5.5", true, false],
    ]);
    expect(modelChoices(list, "claude", "claude-opus-5-5").map((c) => c.value)).toEqual(["claude-haiku-x", "claude-opus-5-5"]);
    expect(modelChoices(list, "codex", "gpt-y").map((c) => [c.value, c.custom])).toEqual([["gpt-x", false], ["gpt-y", true]]);
    expect(modelName(list, "claude-haiku-x")).toBe("Haiku");
    expect(modelName(list, "gpt-x")).toBe("gpt-x");
    expect(defaultModel(list, "claude")).toBe("claude-opus-5-5");
    expect(defaultModel(list, "codex")).toBe("gpt-x");
    expect(defaultModel([], "claude")).toBe("");
  });

  it("checks an edited list like the server", () => {
    expect(MODEL_ID.test("us.anthropic.claude-x:0[1m]")).toBe(true);
    expect(MODEL_ID.test("a b")).toBe(false);
    expect(modelProblems([{ runtime: "claude", model: "a", default: true }, { runtime: "claude", model: "a" }, { runtime: "claude", model: "b", default: true }])).toEqual([
      "a 重复了",
      "Claude Code 只能有一个默认模型",
    ]);
    expect(modelProblems([{ runtime: "claude", model: "a", default: true }, { runtime: "codex", model: "a", default: true }])).toEqual([]);
    expect(modelProblems([])).toEqual(["至少保留一个模型"]);
    expect(modelProblems(Array.from({ length: 51 }, (_, i) => ({ runtime: "claude" as const, model: `m${i}` })))).toEqual(["最多 50 个模型"]);
  });

  it("words the /model result: live, next session, skipped", () => {
    const name = (id: string) => modelName(list, id);
    expect(modelAppliedText({ applied: [{ employee: "lead-1", model: "claude-haiku-x", live: true }], skipped: [] }, name)).toBe("lead-1 已切换到 Haiku，正在运行的会话从下一轮起生效");
    expect(modelAppliedText({ applied: [{ employee: "项目统筹", model: "claude-opus-5-5", live: false }], skipped: null }, name)).toBe("项目统筹 改用 Opus 5.5，下次打开会话时生效");
    expect(
      modelAppliedText({ applied: [{ employee: "lead-1", model: "claude-haiku-x", live: true }, { employee: "dev-1", model: "claude-haiku-x", live: false }], skipped: ["codex-1"] }, name),
    ).toBe("2 位员工 改用 Haiku：1 个运行中的会话从下一轮起生效，其余下次打开会话时生效；codex-1 是另一种类型，模型没变");
  });
});

describe("custom commands", () => {
  const lead = { tier: "lead" as const, runtime: "claude" as const };
  const codex = { tier: "member" as const, runtime: "codex" as const };
  it("validates names, clashes, modes and templates", () => {
    const ok = { name: "plan", description: "", mode: "prompt" as const, prompt: "计划：{{args}}" };
    expect(commandProblem(ok, [], false)).toBe("");
    expect(commandProblem({ ...ok, name: "model" }, [], false)).toBe("/model 是平台命令，换个名字");
    expect(commandProblem({ ...ok, name: "Plan" }, [], false)).toContain("小写字母开头");
    expect(commandProblem(ok, ["plan"], false)).toBe("已经有 /plan 了");
    expect(commandProblem({ ...ok, prompt: " " }, [], false)).toBe("提示词模板不能为空");
    expect(commandProblem({ ...ok, prompt: "/compact" }, [], false)).toContain("不能以 / 开头");
    expect(commandProblem({ name: "compact", mode: "passthrough" }, [], false)).toBe("");
    expect(commandProblem({ name: "compact", mode: "passthrough" }, [], true)).toBe("这个岗位只能用提示词模板");
    expect(cleanCommand({ name: " /plan ", description: " 说明 ", mode: "prompt", prompt: " x " })).toEqual({ name: "plan", description: "说明", mode: "prompt", prompt: "x" });
    expect(cleanCommand({ name: "c", mode: "passthrough", prompt: "dropped" })).toEqual({ name: "c", description: "", mode: "passthrough" });
  });

  it("knows which roles get platform toggles and passthrough", () => {
    expect(promptOnly(lead)).toBe(false);
    expect(promptOnly(codex)).toBe(true);
    expect(promptOnly({ tier: "member", runtime: "claude" })).toBe(true);
    expect(chatScope({ tier: "member" })).toBeNull();
    expect(togglable({ tier: "coordinator" }).some((c) => c.name === "effort")).toBe(false);
    expect(togglable({ tier: "lead" }).map((c) => c.name)).toContain("effort");
    expect(togglable({ tier: "member" })).toEqual([]);
    expect(toggleHidden(["usage"], "usage")).toEqual([]);
    expect(toggleHidden(null, "memory")).toEqual(["memory"]);
    expect(commandsHint(codex)).toContain("Codex 没有斜杠命令");
    expect(commandsHint(lead)).toContain("透传");
    const bad = { ...newRole([], "#4C7BD0"), runtime: "codex" as const, commands: [{ name: "c", mode: "passthrough" as const }] };
    expect(unfit(bad).map((c) => c.name)).toEqual(["c"]);
    expect(roleProblems([bad])).toContain("新岗位 只能用提示词模板命令");
  });
});
