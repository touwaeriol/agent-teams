// A model switch that leaves the effort in force behind (#53 t45): the new model has no such level,
// so the session goes back to the model's default instead of reporting (and resuming with) the old one.
import { describe, expect, it } from "vitest";
import type { ModelOption, SessionOpen } from "../src/protocol.js";
import { currentMode, dropsEffort, rememberMode } from "../src/sessions/manager.js";

const models: ModelOption[] = [
  { id: "opus", efforts: ["low", "medium", "high"] },
  { id: "haiku", efforts: [] },
  { id: "mini", efforts: ["low", "medium"] },
];

describe("dropsEffort", () => {
  it("drops a level the new model lacks", () => {
    expect(dropsEffort(models, { model: "haiku" }, "high")).toBe(true);
    expect(dropsEffort(models, { model: "mini" }, "high")).toBe(true);
  });
  it("keeps a level the new model has, a picked level, no level, and unknown models", () => {
    expect(dropsEffort(models, { model: "mini" }, "low")).toBe(false);
    expect(dropsEffort(models, { model: "mini", effort: "medium" }, "high")).toBe(false);
    expect(dropsEffort(models, { model: "haiku" }, undefined)).toBe(false);
    expect(dropsEffort(models, { model: "other" }, "high")).toBe(false);
    expect(dropsEffort(undefined, { model: "haiku" }, "high")).toBe(false);
    expect(dropsEffort(models, { mode: "plan" }, "high")).toBe(false);
  });
  it("rememberMode leaves the dropped level out of the open spec (Claude and Codex)", () => {
    const claude = { claude: { model: "opus", effort: "high", permission_mode: "default" } } as unknown as SessionOpen;
    rememberMode(claude, { model: "haiku", dropEffort: true });
    expect(currentMode(claude)).toEqual({ permission_mode: "default", model: "haiku" });
    const codex = { codex: { model: "gpt", effort: "xhigh", sandbox: "workspace-write" } } as unknown as SessionOpen;
    rememberMode(codex, { model: "gpt-mini", dropEffort: true });
    expect(currentMode(codex)).toEqual({ permission_mode: "default", model: "gpt-mini" });
  });
});
