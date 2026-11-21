// Models and custom commands (pure parts): model suggestions and format, project model status,
// command validation and the role card hints.
import { describe, expect, it } from "vitest";
import { chatScope, cleanCommand, commandProblem, commandsHint, promptOnly, toggleHidden, togglable, unfit } from "@/lib/commands";
import { BUILTIN_MODEL, emptyRoleModel, MODEL_ID, modelName, modelSuggestions, offersCodex, projectModelStatus, validModel } from "@/lib/models";
import { newRole, roleProblems } from "@/lib/roles";

describe("models (no list: typed ids)", () => {
  it("suggests the roles' models of the runtime, then the common ids, each once", () => {
    const roles = [
      { tier: "coordinator", runtime: "claude", model: "claude-coord-x" },
      { tier: "lead", runtime: "claude", model: "" },
      { tier: "member", runtime: "claude", model: "claude-sonnet-5-5" },
      { tier: "member", runtime: "codex", model: "gpt-x" },
    ];
    expect(modelSuggestions(roles, "claude")).toEqual(["claude-coord-x", "claude-sonnet-5-5", "claude-opus-5-5", "claude-fable-5-2"]);
    expect(modelSuggestions(roles, "codex")).toEqual(["gpt-x"]);
    expect(modelSuggestions([], "claude")).toEqual(["claude-opus-5-5", "claude-fable-5-2", "claude-sonnet-5-5"]);
    expect(modelSuggestions([], "codex", "gpt-typed")).toEqual(["gpt-typed"]);
  });

  it("accepts '' or any id in the server's format", () => {
    expect(MODEL_ID.test("us.anthropic.claude-x:0[1m]")).toBe(true);
    expect(validModel("")).toBe(true);
    expect(validModel("claude-anything")).toBe(true);
    expect(validModel("a b")).toBe(false);
    expect(validModel("-x")).toBe(false);
    expect(validModel("x".repeat(101))).toBe(false);
  });

  it("names common ids, keeps others as typed, and explains an empty role model", () => {
    expect(modelName("claude-opus-5-5")).toBe("Opus 5.5");
    expect(modelName("claude-fable-5-2")).toBe("Fable 5.2");
    expect(modelName("gpt-x")).toBe("gpt-x");
    expect(emptyRoleModel("claude")).toBe(`留空 = ${BUILTIN_MODEL}`);
    expect(emptyRoleModel("codex")).toBe("留空 = 默认");
  });

  it("says what a project model field puts in effect", () => {
    const roles = [
      { tier: "coordinator", runtime: "claude", model: "" },
      { tier: "lead", runtime: "claude", model: "claude-haiku-x" },
      { tier: "member", runtime: "claude", model: "" },
      { tier: "member", runtime: "claude", model: "claude-haiku-x" },
    ];
    // The thread fields cover leads and members of their type; coordinator_model covers the coordinator.
    expect(projectModelStatus("", roles, "claude")).toBe("跟随岗位配置（岗位：claude-haiku-x、内置默认 Opus 5.5）");
    expect(projectModelStatus("", roles, "coordinator")).toBe("跟随岗位配置（岗位：内置默认 Opus 5.5）");
    expect(projectModelStatus("", roles, "codex")).toBe("跟随岗位配置（没有这类岗位）");
    expect(projectModelStatus("", [{ tier: "member", runtime: "codex", model: "" }], "codex")).toBe("跟随岗位配置（岗位：Codex 默认）");
    expect(projectModelStatus("claude-sonnet-5-5", roles, "claude")).toBe("项目指定：Sonnet 5.5");
    expect(projectModelStatus("gone", roles, "coordinator")).toBe("项目指定：gone");
    expect(offersCodex(roles)).toBe(false);
    expect(offersCodex([{ tier: "member", runtime: "codex" }])).toBe(true);
    expect(offersCodex([{ tier: "lead", runtime: "codex" }])).toBe(true);
    expect(offersCodex([], "gpt-x")).toBe(true);
  });
});

describe("custom commands", () => {
  const lead = { tier: "lead" as const, runtime: "claude" as const };
  const codex = { tier: "member" as const, runtime: "codex" as const };
  it("validates names, clashes, modes and templates", () => {
    const ok = { name: "plan", description: "", mode: "prompt" as const, prompt: "计划：{{args}}" };
    expect(commandProblem(ok, [], false)).toBe("");
    expect(commandProblem({ ...ok, name: "model" }, [], false)).toBe("/model 是平台保留的命令名，换个名字");
    expect(commandProblem({ ...ok, name: "status" }, [], false)).toBe("/status 是平台保留的命令名，换个名字");
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
    // /model is gone: no toggle for it (a saved hidden list that still names it is kept as is).
    expect(togglable({ tier: "coordinator" }).some((c) => c.name === "model")).toBe(false);
    expect(togglable({ tier: "lead" }).some((c) => c.name === "model")).toBe(false);
    expect(toggleHidden(["model"], "status")).toEqual(["model", "status"]);
    expect(toggleHidden(["usage"], "usage")).toEqual([]);
    expect(toggleHidden(null, "memory")).toEqual(["memory"]);
    expect(commandsHint(codex)).toContain("Codex 没有斜杠命令");
    expect(commandsHint(lead)).toContain("透传");
    const bad = { ...newRole([], "#4C7BD0"), runtime: "codex" as const, commands: [{ name: "c", mode: "passthrough" as const }] };
    expect(unfit(bad).map((c) => c.name)).toEqual(["c"]);
    expect(roleProblems([bad])).toContain("新岗位 只能用提示词模板命令");
  });
});
