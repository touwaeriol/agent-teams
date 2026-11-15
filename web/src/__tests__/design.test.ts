import { describe, expect, it } from "vitest";
import type { RoleView } from "@/api/views";
import { fileUrl, formatBytes, frameSandbox, isHosted, markdownUrl, renderLabel } from "@/lib/artifacts";
import { canHaveSkills, fromPreset, newRole, roleProblems, toggleSkill } from "@/lib/roles";
import { deliverableHref } from "@/pages/Deliverables";

describe("hosted deliverables", () => {
  it("never lets a page served from the console keep its origin", () => {
    expect(frameSandbox(false)).not.toContain("allow-same-origin");
    expect(frameSandbox(false)).toContain("allow-scripts");
    expect(frameSandbox(true)).toContain("allow-same-origin");
  });
  it("opens hosted files in the viewer", () => {
    const hosted = { id: "d", kind: "doc", url: "", detail: { hosted: true } };
    expect(isHosted(hosted)).toBe(true);
    expect(deliverableHref("p", hosted)).toEqual({ to: "/projects/p/deliverables/d" });
    expect(deliverableHref("p", { id: "d", kind: "doc", url: "https://x.test/a", detail: {} })).toEqual({ href: "https://x.test/a" });
  });
  it("builds file URLs and rewrites Markdown links safely", () => {
    const base = "/files/dl_1.2.99.abc/";
    expect(fileUrl(base, "assets/my logo.svg")).toBe("/files/dl_1.2.99.abc/assets/my%20logo.svg");
    const md = markdownUrl(base);
    expect(md("img/a.png")).toBe("/files/dl_1.2.99.abc/img/a.png");
    expect(md("../../api/me")).toBe("");
    expect(md("https://example.com/x")).toBe("https://example.com/x");
    expect(md("javascript:alert(1)")).toBe("");
    expect(md("/api/me")).toBe("");
    expect(md("#intro")).toBe("#intro");
    expect(markdownUrl("https://files.example.net/files/t/")("a.png")).toBe("https://files.example.net/files/t/a.png");
  });
  it("formats sizes and labels", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MB");
    expect(renderLabel("markdown")).toBe("Markdown 文档");
    expect(renderLabel(undefined)).toBe("文件");
  });
});

describe("role skills", () => {
  const designer: RoleView = { ...newRole([], "#000"), role: "designer", title: "设计师", skills: ["design", "docs"], id: "preset" };
  it("toggles skills and keeps them off the coordinator", () => {
    expect(toggleSkill(["design"], "docs")).toEqual(["design", "docs"]);
    expect(toggleSkill(["design", "docs"], "design")).toEqual(["docs"]);
    expect(toggleSkill(null, "design")).toEqual(["design"]);
    expect(canHaveSkills({ tier: "coordinator" })).toBe(false);
    const coord = { ...newRole([], "#000"), tier: "coordinator" as const, role: "coordinator", skills: ["design"] };
    expect(roleProblems([coord]).join()).toContain("不能加载技能");
  });
  it("adds a preset with a free key", () => {
    const first = fromPreset([], designer);
    expect([first.id, first.role, first.skills]).toEqual(["", "designer", ["design", "docs"]]);
    const second = fromPreset([first], designer);
    expect([second.role, second.sort]).toEqual(["designer-2", 1]);
  });
});

describe("skill uploads", () => {
  it("drops hidden files of a picked folder and encodes zips", async () => {
    const { pickedSkillFiles, toBase64 } = await import("@/components/app/SkillLibrary");
    expect(pickedSkillFiles([{ path: "pitch/SKILL.md", text: "x" }, { path: "pitch/.DS_Store", text: "" }, { path: "pitch/.git/HEAD", text: "" }])).toEqual([
      { path: "pitch/SKILL.md", content: "x" },
    ]);
    expect(toBase64(new TextEncoder().encode("PK\u0003\u0004"))).toBe("UEsDBA==");
    const big = new Uint8Array(100_000).fill(65);
    expect(atob(toBase64(big)).length).toBe(100_000);
  });
});
