import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SharedFs, platformKey, type SharedFsOptions } from "../src/fs/shared.js";
import { ensureMemoryDirs, linkSharedDirs, memoryFolderPrompt, safeAttachmentDir, sharedFolderPrompt, unlinkSharedDirs, withSharedDirs } from "../src/fs/session.js";
import type { FsMount, FsStatus, SessionOpen } from "../src/protocol.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";
import { silentLogger } from "../src/util/log.js";

const dirs: string[] = [];
function tmp(): string {
  const d = mkdtempSync(join(tmpdir(), "at-fs-"));
  dirs.push(d);
  return d;
}

/** A juicefs release tarball holding a fake binary. */
function tarball(): { data: Buffer; sha: string } {
  const d = tmp();
  writeFileSync(join(d, "juicefs"), "#!/bin/sh\nexit 0\n");
  spawnSync("tar", ["-czf", join(d, "j.tgz"), "-C", d, "juicefs"]);
  const data = readFileSync(join(d, "j.tgz"));
  return { data, sha: createHash("sha256").update(data).digest("hex") };
}

function spec(sha: string, generation = 1): FsMount {
  return {
    volume: "at-0123456789abcdef", generation, meta_url: "postgres://jfs_x@db:5432/at?search_path=jfs_x", meta_password: "pw",
    client: { version: "1.4.2", assets: [{ platform: "linux-amd64", url: "https://dl/juicefs.tgz", sha256: sha }] },
  };
}

interface Rig { fs: SharedFs; sent: FsStatus[]; calls: { cmd: string; args: string[]; env?: NodeJS.ProcessEnv }[]; data: string; fetches: number; sha: string }

function rig(over: Partial<SharedFsOptions> = {}, data = tmp()): Rig {
  // One tarball per rig: two builds can differ (gzip stores a timestamp), so its own sha is used.
  const { data: tgz, sha } = tarball();
  const r: Rig = { sent: [], calls: [], data, fetches: 0, sha } as unknown as Rig;
  r.fs = new SharedFs({
    dataDir: r.data, log: silentLogger, send: (_t, b) => r.sent.push(b), fuse: () => true, platform: "linux-amd64", mountWaitMs: 1000,
    fetchFn: (async () => (r.fetches++, new Response(tgz))) as unknown as typeof fetch,
    run: async (cmd, args, opts) => {
      r.calls.push({ cmd, args, ...(opts?.env ? { env: opts.env } : {}) });
      if (args[0] === "mount") writeFileSync(join(args.at(-1)!, ".config"), "{}");
      if (cmd === "fusermount3") rmSync(join(args.at(-1)!, ".config"), { force: true });
      return { code: 0, stdout: "", stderr: "" };
    },
    ...over,
  });
  return r;
}

afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

describe("SharedFs", () => {
  it("downloads the pinned client, mounts with the password in the env only, and keeps a healthy mount", async () => {
    const r = rig();
    const sha = r.sha;
    await r.fs.mount(spec(sha));
    expect(r.sent.map((s) => s.state)).toEqual(["mounting", "mounted"]);
    const mount = r.calls.find((c) => c.args[0] === "mount")!;
    expect(mount.cmd).toBe(join(r.data, "tools", "juicefs-1.4.2", "juicefs"));
    expect(mount.args).not.toContain("pw");
    expect(mount.env?.META_PASSWORD).toBe("pw");
    expect(mount.args.at(-1)).toBe(join(r.data, "mnt", "at-0123456789abcdef"));
    expect(r.fs.root()).toBe(join(r.data, "mnt", "at-0123456789abcdef"));

    await r.fs.mount(spec(sha));
    expect(r.calls.filter((c) => c.args[0] === "mount")).toHaveLength(1);
    expect(r.fetches).toBe(1);

    await r.fs.mount(spec(sha, 2));
    expect(r.calls.filter((c) => c.args[0] === "mount")).toHaveLength(2);
    expect(r.calls.some((c) => c.cmd === "fusermount3")).toBe(true);
    expect(r.sent.at(-1)).toEqual({ state: "mounted", mode: "daemon", volume: "at-0123456789abcdef", generation: 2 });
  });

  it("ignores an fs.mount with an older generation than the mounted one", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha, 3));
    const sent = r.sent.length;
    await r.fs.mount(spec(r.sha, 2));
    expect(r.calls.filter((c) => c.args[0] === "mount")).toHaveLength(1);
    expect(r.calls.some((c) => c.cmd === "fusermount3")).toBe(false);
    expect(r.sent).toHaveLength(sent);
    expect(r.fs.root()).not.toBeNull();
  });

  it("adopts the mount of a previous daemon process instead of remounting it", async () => {
    const first = rig();
    await first.fs.mount(spec(first.sha, 2));
    expect(readFileSync(join(first.data, "fs-mount.json"), "utf8")).not.toContain("pw");

    const r = rig({}, first.data);
    expect(r.fs.root()).toBeNull();
    await r.fs.mount(spec(r.sha, 1));
    expect(r.calls).toEqual([]);
    await r.fs.mount(spec(r.sha, 2));
    expect(r.calls).toEqual([]);
    expect(r.sent.at(-1)).toEqual({ state: "mounted", mode: "daemon", volume: "at-0123456789abcdef", generation: 2 });
    expect(r.fs.root()).toBe(join(r.data, "mnt", "at-0123456789abcdef"));
  });

  it("unmounts a mount left by a previous daemon process", async () => {
    const first = rig();
    await first.fs.mount(spec(first.sha));
    const r = rig({}, first.data);
    await r.fs.unmount({ volume: "at-0123456789abcdef" });
    expect(r.calls.map((c) => c.cmd)).toEqual(["fusermount3"]);
    expect(existsSync(join(r.data, "fs-mount.json"))).toBe(false);
    expect(r.sent.at(-1)).toEqual({ state: "unmounted", mode: "daemon" });
  });

  it("has no root once the mount point is no longer a JuiceFS mount", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha));
    rmSync(join(r.data, "mnt", "at-0123456789abcdef", ".config"));
    expect(r.fs.root()).toBeNull();
    expect(r.fs.sessionDirs({ project: "projects/p1", shared: "shared" })).toBeNull();
    expect(existsSync(join(r.data, "mnt", "at-0123456789abcdef", "projects"))).toBe(false);
  });

  it("refuses a client whose sha256 does not match", async () => {
    const r = rig();
    await r.fs.mount(spec("0".repeat(64)));
    expect(r.sent.at(-1)).toMatchObject({ state: "error", error: expect.stringContaining("sha256 mismatch") });
    expect(r.calls.some((c) => c.args[0] === "mount")).toBe(false);
  });

  it("reports an error without FUSE and does not advertise fs_mount", async () => {
    const r = rig({ fuse: () => false });
    expect(r.fs.features()).toEqual([]);
    await r.fs.mount(spec("x"));
    expect(r.sent.at(-1)).toMatchObject({ state: "error", error: expect.stringContaining("FUSE") });
  });

  it("unmounts and forgets on fs.unmount", async () => {
    const r = rig();
    const sha = r.sha;
    await r.fs.mount(spec(sha));
    await r.fs.unmount({ volume: "at-0123456789abcdef" });
    expect(r.sent.at(-1)).toEqual({ state: "unmounted", mode: "daemon" });
    expect(r.fs.root()).toBeNull();
  });

  it("creates the session directories and refuses paths outside the volume", async () => {
    const r = rig();
    const sha = r.sha;
    expect(r.fs.sessionDirs({ project: "projects/p1", shared: "shared" })).toBeNull();
    await r.fs.mount(spec(sha));
    const d = r.fs.sessionDirs({ project: "projects/p1", shared: "shared" })!;
    expect(existsSync(d.project) && existsSync(d.shared)).toBe(true);
    expect(r.fs.sessionDirs({ project: "../../etc", shared: "shared" })).toBeNull();
  });

  it("uses an external mount without mounting anything", async () => {
    const ext = tmp();
    writeFileSync(join(ext, ".config"), "{}");
    const r = rig({ externalDir: ext });
    expect(r.fs.features()).toEqual(["fs_external"]);
    r.fs.report();
    expect(r.sent.at(-1)).toEqual({ state: "mounted", mode: "external" });
    await r.fs.mount(spec("x"));
    expect(r.calls).toEqual([]);
    expect(r.fs.root()).toBe(ext);
  });

  it("exits when an external mount seen healthy goes away, but not when it never was there", async () => {
    const ext = tmp();
    let lost = 0;
    const never = rig({ externalDir: ext, checkMs: 10, onExternalLost: () => lost++ });
    never.fs.start();
    await new Promise((r) => setTimeout(r, 50));
    never.fs.stop();
    expect(lost).toBe(0);

    writeFileSync(join(ext, ".config"), "{}");
    const r = rig({ externalDir: ext, checkMs: 10, onExternalLost: () => lost++ });
    r.fs.start();
    await new Promise((res) => setTimeout(res, 30));
    rmSync(join(ext, ".config"));
    await new Promise((res) => setTimeout(res, 50));
    r.fs.stop();
    expect(lost).toBe(1);
    expect(r.sent.at(-1)).toMatchObject({ state: "error", mode: "external" });
  });

  it("names platforms like the console", () => {
    expect(platformKey("linux", "x64")).toBe("linux-amd64");
    expect(platformKey("darwin", "arm64")).toBe("darwin-arm64");
  });
});

describe("withSharedDirs", () => {
  const dirsOf = { project: "/v/projects/p1", shared: "/v/shared" };
  it("adds the folders to the Claude sandbox and Codex writable roots", () => {
    const claude = withSharedDirs({ claude: { sandbox: { enabled: true, extra_write_paths: ["x"] } } } as unknown as SessionOpen, dirsOf);
    expect(claude.claude!.sandbox!.extra_write_paths).toEqual(["x", "/v/projects/p1", "/v/shared"]);
    const codex = withSharedDirs({ codex: { sandbox: "workspace-write", config: { sandbox_workspace_write: { writable_roots: ["a"] } } } } as unknown as SessionOpen, dirsOf);
    expect((codex.codex!.config as { sandbox_workspace_write: { writable_roots: string[] } }).sandbox_workspace_write.writable_roots).toEqual(["a", "/v/projects/p1", "/v/shared"]);
  });
});

describe("linkSharedDirs", () => {
  it("links the working directory to the folder, replacing an old .attachments folder and leaving other files alone", () => {
    const cwd = tmp();
    const vol = tmp();
    const dirs = { project: join(vol, "projects/p1"), shared: join(vol, "shared") };
    mkdirSync(dirs.project, { recursive: true });
    mkdirSync(dirs.shared);
    mkdirSync(join(cwd, ".attachments"));
    writeFileSync(join(cwd, ".attachments", "old.png"), "x");
    writeFileSync(join(cwd, ".user-files"), "mine");
    expect(linkSharedDirs(cwd, dirs)).toEqual([`${join(cwd, ".user-files")} exists and is not a link`]);
    expect(readlinkSync(join(cwd, ".project-files"))).toBe(dirs.project);
    expect(readlinkSync(join(cwd, ".attachments"))).toBe(join(dirs.project, "attachments"));
    expect(existsSync(join(dirs.project, "attachments"))).toBe(true);
    expect(lstatSync(join(cwd, ".user-files")).isFile()).toBe(true);
    expect(linkSharedDirs(cwd, dirs)).toHaveLength(1);
  });
});

describe("unlinkSharedDirs", () => {
  it("removes only the platform's links, never an employee's files", () => {
    const cwd = tmp();
    const vol = tmp();
    symlinkSync(vol, join(cwd, ".project-files"));
    symlinkSync(vol, join(cwd, ".attachments"));
    symlinkSync(vol, join(cwd, "other"));
    writeFileSync(join(cwd, ".user-files"), "mine");
    unlinkSharedDirs(cwd);
    expect(existsSync(join(cwd, ".project-files")) || existsSync(join(cwd, ".attachments"))).toBe(false);
    expect(lstatSync(join(cwd, "other")).isSymbolicLink()).toBe(true);
    expect(lstatSync(join(cwd, ".user-files")).isFile()).toBe(true);
  });
});

describe("safeAttachmentDir", () => {
  it("accepts a real directory inside the volume and refuses a symlink or a path outside", () => {
    const vol = tmp();
    const out = tmp();
    mkdirSync(join(vol, "p1", "attachments"), { recursive: true });
    expect(safeAttachmentDir(join(vol, "p1", "attachments"), vol)).toBe(true);
    mkdirSync(join(vol, "p2"));
    symlinkSync(out, join(vol, "p2", "attachments"));
    expect(safeAttachmentDir(join(vol, "p2", "attachments"), vol)).toBe(false);
    symlinkSync(out, join(vol, "p3"));
    mkdirSync(join(out, "attachments"));
    expect(safeAttachmentDir(join(vol, "p3", "attachments"), vol)).toBe(false);
    expect(safeAttachmentDir(join(vol, "missing"), vol)).toBe(false);
  });
});

describe("sharedFolderPrompt", () => {
  it("names the three links and keeps employees out of the deliverables' folder", () => {
    const p = sharedFolderPrompt({ project: "/mnt/fs/projects/p1", shared: "/mnt/fs/shared" });
    for (const s of [".project-files/ → /mnt/fs/projects/p1", ".user-files/ → /mnt/fs/shared", ".attachments/ → /mnt/fs/projects/p1/attachments", ".deliverables", "never write"]) expect(p).toContain(s);
  });
});

describe("memory directories", () => {
  it("are created under the project's and the user's folder and named in the coordinator's prompt", () => {
    const vol = tmp();
    const dirs = { project: join(vol, "projects/p1"), shared: join(vol, "shared") };
    expect(ensureMemoryDirs(dirs)).toEqual([join(vol, "projects/p1/.memory"), join(vol, "shared/.memory")]);
    expect(existsSync(join(vol, "shared/.memory"))).toBe(true);
    const p = memoryFolderPrompt(dirs);
    for (const s of [".project-files/.memory/", ".user-files/.memory/", join(vol, "shared/.memory"), "nowhere else", "as path"]) expect(p).toContain(s);
  });

  it("refuse a .memory that is a link to elsewhere", () => {
    const vol = tmp();
    mkdirSync(join(vol, "projects/p1"), { recursive: true });
    mkdirSync(join(vol, "shared"));
    symlinkSync("/", join(vol, "projects/p1/.memory"), "dir");
    expect(() => ensureMemoryDirs({ project: join(vol, "projects/p1"), shared: join(vol, "shared") })).toThrow(/link/);
  });
});

describe("daemon shared folder", () => {
  let h: Harness;
  afterEach(async () => {
    const invalid = [...h.console.invalid];
    await h.close();
    expect(invalid).toEqual([]);
  });

  it("advertises fs_external, reports status and gives sessions the folders", async () => {
    const ext = tmp();
    writeFileSync(join(ext, ".config"), "{}");
    mkdirSync(join(ext, "shared"));
    h = await startHarness({ fsDir: ext });
    expect(h.console.ofType("hello")[0]!.body.features).toContain("fs_external");
    expect((await h.console.waitType("fs.status")).body).toEqual({ state: "mounted", mode: "external" });

    h.console.command("session.open", claudeOpen("s_F", 1, { fs: { project: "projects/p1", shared: "shared" } }));
    await h.console.waitType("session.opened", (b) => b.session_id === "s_F");
    const o = h.log.options.at(-1)!;
    expect(o.additionalDirectories).toEqual([join(ext, "projects/p1"), join(ext, "shared")]);
    expect(o.env?.AGENT_TEAMS_PROJECT_DIR).toBe(join(ext, "projects/p1"));
    expect(existsSync(join(ext, "projects/p1"))).toBe(true);
    expect(JSON.stringify(o.systemPrompt)).toContain(".project-files/");
    expect(readlinkSync(join(o.cwd!, ".project-files"))).toBe(join(ext, "projects/p1"));
    expect(readlinkSync(join(o.cwd!, ".attachments"))).toBe(join(ext, "projects/p1/attachments"));

    // Reopened without the folder (switched off, not mounted): the stale links go.
    h.console.command("session.close", { session_id: "s_F", open_gen: 1 });
    await h.console.waitType("session.closed", (b) => b.session_id === "s_F");
    h.console.command("session.open", claudeOpen("s_F", 2));
    await h.console.waitType("session.opened", (b) => b.session_id === "s_F" && b.open_gen === 2);
    for (const name of [".project-files", ".user-files", ".attachments"]) expect(existsSync(join(o.cwd!, name))).toBe(false);
  });

  it("gives a coordinator with memory_files only the memory directories, and none without it", async () => {
    const ext = tmp();
    writeFileSync(join(ext, ".config"), "{}");
    mkdirSync(join(ext, "shared"));
    h = await startHarness({ fsDir: ext });
    await h.console.waitType("fs.status");
    const coordinator = (id: string, memory: boolean) =>
      claudeOpen(id, 1, {
        employee: { name: "coordinator", role: "coordinator", tier: "coordinator", project_id: "p1" },
        cwd: `projects/p1/${id}`,
        fs: { project: "projects/p1", shared: "shared" },
        claude: { model: "claude-opus-5-5", tools: [], allowed_tools: ["mcp__platform__*"], permission_mode: "default", setting_sources: [], memory_files: memory },
      });

    h.console.command("session.open", coordinator("s_C", true));
    await h.console.waitType("session.opened", (b) => b.session_id === "s_C");
    const o = h.log.options.at(-1)!;
    expect(o.additionalDirectories).toEqual([join(ext, "projects/p1/.memory"), join(ext, "shared/.memory")]);
    expect(o.env?.AGENT_TEAMS_PROJECT_DIR).toBeUndefined();
    expect(JSON.stringify(o.systemPrompt)).toContain("## Memory files");
    expect(readlinkSync(join(o.cwd!, ".project-files"))).toBe(join(ext, "projects/p1"));

    h.console.command("session.open", coordinator("s_D", false));
    await h.console.waitType("session.opened", (b) => b.session_id === "s_D");
    const d = h.log.options.at(-1)!;
    expect(d.additionalDirectories).toBeUndefined();
    expect(existsSync(join(d.cwd!, ".project-files"))).toBe(false);
  });
});
