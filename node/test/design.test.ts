import { mkdirSync, mkdtempSync, readFileSync, existsSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { describe, expect, it, vi } from "vitest";
import { buildClaudeOptions } from "../src/sessions/claude/options.js";
import { materializeSkills, SKILLS_PLUGIN } from "../src/sessions/skills.js";
import { ToolBroker } from "../src/toolhost/broker.js";
import { CHUNK_BYTES, MAX_BYTES, prepareUpload, UploadError } from "../src/toolhost/upload.js";
import type { SkillBundle } from "../src/protocol.js";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import bundle from "../../protocol/gen/bundle.schema.json" with { type: "json" };

const ajv = new Ajv2020({ strict: false });
addFormats(ajv);
ajv.addSchema({ ...bundle, $id: "bundle" });
const schemaOf: Record<string, string> = { "file.chunk": "FileChunk", "tool.call": "ToolCall" };
function validateBody(type: string, body: unknown): string | null {
  const v = ajv.getSchema(`bundle#/$defs/${schemaOf[type]}`)!;
  return v(body) ? null : JSON.stringify(v.errors);
}

const employee = { name: "designer-1", tier: "member" as const, project_id: "p1", thread_no: 3 };
const skill = (name: string, files: Record<string, string>): SkillBundle => ({ name, files: Object.entries(files).map(([path, content]) => ({ path, content })) as SkillBundle["files"] });

describe("skills", () => {
  it("writes the role's skills as a local plugin outside the worktree", () => {
    const root = mkdtempSync(join(tmpdir(), "at-skills-"));
    const m = materializeSkills(join(root, "agent-skills"), { employee, skills: [skill("design", { "SKILL.md": "---\nname: design\n---\n", "ref/a.md": "A" })] })!;
    expect(m.pluginDir).toBe(join(root, "agent-skills", "p1", "3", "designer-1"));
    expect(m.names).toEqual([`${SKILLS_PLUGIN}:design`]);
    expect(JSON.parse(readFileSync(join(m.pluginDir, ".claude-plugin", "plugin.json"), "utf8")).name).toBe(SKILLS_PLUGIN);
    expect(readFileSync(join(m.pluginDir, "skills", "design", "ref", "a.md"), "utf8")).toBe("A");
    // Reopening rebuilds the directory: removed skills disappear.
    materializeSkills(join(root, "agent-skills"), { employee, skills: [skill("docs", { "SKILL.md": "x" })] });
    expect(existsSync(join(m.pluginDir, "skills", "design"))).toBe(false);
    expect(materializeSkills(join(root, "agent-skills"), { employee, skills: [] })).toBeNull();
  });

  it("puts a project member's skills in <project>/project/<name>, not under its current thread", () => {
    const root = mkdtempSync(join(tmpdir(), "at-skills-"));
    const m = materializeSkills(root, { employee: { ...employee, scope: "project" }, skills: [skill("design", { "SKILL.md": "x" })] })!;
    expect(m.pluginDir).toBe(join(root, "p1", "project", "designer-1"));
    // Without a scope (an older console) the thread number still decides.
    expect(materializeSkills(root, { employee, skills: [skill("design", { "SKILL.md": "x" })] })!.pluginDir).toBe(join(root, "p1", "3", "designer-1"));
  });

  it("refuses paths that escape the skill directory", () => {
    const root = mkdtempSync(join(tmpdir(), "at-skills-"));
    expect(() => materializeSkills(root, { employee, skills: [skill("evil", { "../../x.md": "x" })] })).toThrow();
    expect(() => materializeSkills(root, { employee, skills: [skill("evil", { ".claude-plugin/hooks.json": "x" })] })).toThrow();
    expect(() => materializeSkills(root, { employee, skills: [skill("../evil", { "SKILL.md": "x" })] })).toThrow();
    // An odd employee name never climbs out of (or wipes) its own directory.
    const m = materializeSkills(root, { employee: { ...employee, name: ".." }, skills: [skill("ok", { "SKILL.md": "x" })] })!;
    expect(m.pluginDir).toBe(join(root, "p1", "3", "_.."));
  });

  it("passes the plugin and the qualified skill names to the SDK", () => {
    const base = { root: "/r", cwd: "/r/p", env: {}, sessionId: "u1", resume: false, server: {} as never, canUseTool: (async () => ({ behavior: "deny", message: "" })) as never, hooks: {}, secretPaths: [] };
    const claude = { model: "claude-opus-5-5", tools: ["Read", "Skill"], allowed_tools: ["mcp__platform__*", "Skill"], permission_mode: "dontAsk" as const, setting_sources: [] as [] };
    const o = buildClaudeOptions({ ...base, claude, skills: { pluginDir: "/r/agent-skills/p1/3/designer-1", names: ["agent-teams-skills:design"] } });
    expect(o.plugins).toEqual([{ type: "local", path: "/r/agent-skills/p1/3/designer-1", skipMcpDiscovery: true }]);
    expect(o.skills).toEqual(["agent-teams-skills:design"]);
    expect(o.settingSources).toEqual([]);
    const none = buildClaudeOptions({ ...base, claude });
    expect(none.plugins).toBeUndefined();
    expect(none.skills).toBeUndefined();
  });
});

function workdir(): string {
  const dir = mkdtempSync(join(tmpdir(), "at-upload-"));
  mkdirSync(join(dir, "design", "assets"), { recursive: true });
  writeFileSync(join(dir, "design", "index.html"), "<!doctype html><link rel=stylesheet href=s.css>");
  writeFileSync(join(dir, "design", "s.css"), "body{}");
  writeFileSync(join(dir, "design", "assets", "logo.svg"), "<svg/>");
  writeFileSync(join(dir, "design", ".secret.json"), "{}");
  mkdirSync(join(dir, "design", "node_modules"));
  writeFileSync(join(dir, "design", "node_modules", "x.js"), "x");
  writeFileSync(join(dir, "spec.md"), "# Spec");
  return dir;
}

describe("publish_file upload", () => {
  it("reads one file", async () => {
    const dir = workdir();
    const up = await prepareUpload(dir, "spec.md");
    expect(up.upload.files).toEqual([{ path: "spec.md", size: 6, sha256: createHash("sha256").update("# Spec").digest("hex"), content_type: "text/markdown; charset=utf-8" }]);
    expect(Buffer.from(up.chunks.join(""), "base64").toString()).toBe("# Spec");
    expect(up.upload.chunks).toBe(1);
    expect(up.upload.upload_id).toMatch(/^up_[0-9A-Za-z]{10,40}$/);
  });

  it("reads a file in the shared folder through its link only when the folder is an allowed root", async () => {
    const dir = workdir();
    const vol = mkdtempSync(join(tmpdir(), "at-vol-"));
    writeFileSync(join(vol, "report.md"), "# R");
    symlinkSync(vol, join(dir, ".project-files"));
    await expect(prepareUpload(dir, ".project-files/report.md")).rejects.toThrow(/inside your working directory/);
    const up = await prepareUpload(dir, ".project-files/report.md", [vol]);
    expect(up.upload.files.map((f) => f.path)).toEqual(["report.md"]);
    await expect(prepareUpload(dir, ".project-files", [vol])).rejects.toBeInstanceOf(UploadError);
  });

  it("bundles a site directory, skipping hidden files and node_modules", async () => {
    const up = await prepareUpload(workdir(), "design");
    expect(up.upload.files.map((f) => f.path)).toEqual(["assets/logo.svg", "index.html", "s.css"]);
  });

  it("refuses paths outside the workdir, symlink escapes, unknown types and sites without index.html", async () => {
    const dir = workdir();
    const outside = mkdtempSync(join(tmpdir(), "at-outside-"));
    writeFileSync(join(outside, "x.md"), "secret");
    symlinkSync(join(outside, "x.md"), join(dir, "link.md"));
    writeFileSync(join(dir, "run.sh"), "x");
    mkdirSync(join(dir, "noindex"));
    writeFileSync(join(dir, "noindex", "a.html"), "a");
    for (const p of ["../x.md", join(outside, "x.md"), "link.md", "run.sh", "noindex", "missing.md", ".", ""]) {
      await expect(prepareUpload(dir, p), p).rejects.toBeInstanceOf(UploadError);
    }
  });

  it("refuses a FIFO or socket instead of blocking on it, alone or inside a site", async () => {
    const dir = workdir();
    execFileSync("mkfifo", [join(dir, "pipe.html")]);
    await expect(prepareUpload(dir, "pipe.html")).rejects.toThrow(/regular file/);
    execFileSync("mkfifo", [join(dir, "design", "pipe.css")]);
    const up = await prepareUpload(dir, "design");
    expect(up.upload.files.map((f) => f.path)).not.toContain("pipe.css");
  });

  it("enforces the size limit and chunks large files", async () => {
    const dir = workdir();
    writeFileSync(join(dir, "big.png"), Buffer.alloc(MAX_BYTES + 1));
    await expect(prepareUpload(dir, "big.png")).rejects.toThrow(/limit/);
    writeFileSync(join(dir, "mid.png"), Buffer.alloc(CHUNK_BYTES * 2 + 5));
    const up = await prepareUpload(dir, "mid.png");
    expect(up.chunks.length).toBe(3);
    expect(Buffer.from(up.chunks[2]!, "base64").length).toBe(5);
  });

  it("broker sends valid file.chunk frames before the tool.call, or fails locally", async () => {
    const dir = workdir();
    const sent: { type: string; body: Record<string, unknown> }[] = [];
    const broker = new ToolBroker((type, body) => sent.push({ type, body: body as Record<string, unknown> }));
    const pending = broker.call({ sessionId: "s_1", openGen: 2, tool: "publish_file", args: { path: "design", kind: "design", title: "T" }, cwd: dir });
    await vi.waitFor(() => expect(sent.map((s) => s.type)).toEqual(["file.chunk", "tool.call"]), { timeout: 5000 });
    for (const s of sent) expect(validateBody(s.type, s.body), s.type).toBeNull();
    const call = sent[1]!.body as { call_id: string; upload: { upload_id: string; files: unknown[] } };
    expect(call.upload.upload_id).toBe(sent[0]!.body.upload_id);
    expect(call.upload.files).toHaveLength(3);
    broker.resolve({ session_id: "s_1", call_id: call.call_id, ok: true, content: [{ type: "text", text: "ok" }] });
    expect((await pending).isError).toBeUndefined();

    const bad = await broker.call({ sessionId: "s_1", tool: "publish_file", args: { path: "../etc/passwd", kind: "doc", title: "x" }, cwd: dir });
    expect(bad.isError).toBe(true);
    expect(sent).toHaveLength(2);
  });
});
