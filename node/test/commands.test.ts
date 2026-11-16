import { describe, expect, it } from "vitest";
import { qualifySlash, sessionCommands } from "../src/sessions/claude/commands.js";

describe("session slash commands", () => {
  it("keeps the session's own commands and drops Claude Code's built-ins", () => {
    expect(sessionCommands([
      { name: "model", description: "Set the model", argumentHint: "[model]", builtin: true },
      { name: "agent-teams-skills:design", description: "Design things", argumentHint: "<what>" },
      { name: "review:check", description: "", argumentHint: "" },
    ])).toEqual([
      { name: "agent-teams-skills:design", description: "Design things", argument_hint: "<what>" },
      { name: "review:check" },
    ]);
    expect(sessionCommands(undefined)).toEqual([]);
  });

  it("qualifies a bare name that matches one session command", () => {
    const names = ["agent-teams-skills:design", "review:check", "other:check", "agent-teams-skills:check", "tidy"];
    expect(qualifySlash("/design 做个登录页", names)).toBe("/agent-teams-skills:design 做个登录页");
    expect(qualifySlash("/design", names)).toBe("/agent-teams-skills:design");
    expect(qualifySlash("/check x", names)).toBe("/agent-teams-skills:check x");
    expect(qualifySlash("/tidy now", names)).toBe("/tidy now");
    expect(qualifySlash("/review:check", names)).toBe("/review:check");
    expect(qualifySlash("/compact", names)).toBe("/compact");
    expect(qualifySlash("design /design", names)).toBe("design /design");
    expect(qualifySlash("/designer", names)).toBe("/designer");
    expect(qualifySlash("/check", ["a:check", "b:check"])).toBe("/check");
  });
});
