import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { isMarkdown, stripToolGrants } from "../src/sessions/frontmatter.js";
import { materializePlugins } from "../src/sessions/plugins.js";
import { materializeSkills } from "../src/sessions/skills.js";

const employee = { name: "dev-1", tier: "member" as const, project_id: "p1", thread_no: 3 };

describe("stripToolGrants", () => {
  it("drops allowed-tools in every spelling with its list or indented value", () => {
    const text = "---\nname: probe\nallowed-tools: Bash(echo:*)\ndescription: d\nAllowed_Tools:\n  - Bash\n  - Write\n\"allowed-tools\": Edit\nmodel: haiku\n---\nbody\nallowed-tools: stays in the body\n";
    expect(stripToolGrants(text)).toBe("---\nname: probe\ndescription: d\nmodel: haiku\n---\nbody\nallowed-tools: stays in the body\n");
  });

  it("handles a BOM and CRLF line endings", () => {
    expect(stripToolGrants("﻿---\r\nname: x\r\nallowed-tools: Bash\r\n---\r\nbody")).toBe("﻿---\r\nname: x\r\n---\r\nbody");
  });

  it("leaves text without closed front matter alone", () => {
    expect(stripToolGrants("allowed-tools: Bash\n")).toBe("allowed-tools: Bash\n");
    expect(stripToolGrants("---\nallowed-tools: Bash\n")).toBe("---\nallowed-tools: Bash\n");
  });

  it("matches every Markdown file, any case", () => {
    expect(["skills/a/SKILL.md", "skills/a/b/skill.MD", "commands/x.md", "README.md"].every(isMarkdown)).toBe(true);
    expect(isMarkdown("hooks/run.sh")).toBe(false);
  });
});

describe("materialised skills and plugins carry no tool grants", () => {
  const grant = "---\nname: probe\ndescription: d\nallowed-tools: Bash(echo:*)\n---\nRun echo.\n";
  const clean = "---\nname: probe\ndescription: d\n---\nRun echo.\n";

  it("strips plugin skills, commands and nested or binary-sent Markdown", () => {
    const root = mkdtempSync(join(tmpdir(), "at-grants-"));
    const [p] = materializePlugins(root, { employee, plugins: [{
      name: "probeplug", version: 1, skills: ["probeplug:probe", "probeplug:go"], agents: [],
      files: [
        { path: ".claude-plugin/plugin.json", content: '{"name":"probeplug"}' },
        { path: "skills/probe/SKILL.md", content: grant },
        { path: "commands/go.md", content: grant },
        { path: "skills/probe/deep/SKILL.md", content: Buffer.from(grant).toString("base64"), encoding: "base64" },
        { path: "hooks/run.sh", content: grant },
      ],
    }] });
    for (const f of ["skills/probe/SKILL.md", "commands/go.md", "skills/probe/deep/SKILL.md"]) expect(readFileSync(join(p!.dir, f), "utf8")).toBe(clean);
    expect(readFileSync(join(p!.dir, "hooks/run.sh"), "utf8")).toBe(grant);
  });

  it("strips role skills", () => {
    const root = mkdtempSync(join(tmpdir(), "at-grants-"));
    const m = materializeSkills(root, { employee, skills: [{ name: "probe", files: [{ path: "SKILL.md", content: grant }] }] })!;
    expect(readFileSync(join(m.pluginDir, "skills", "probe", "SKILL.md"), "utf8")).toBe(clean);
  });
});
