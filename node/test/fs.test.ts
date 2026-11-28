import { createHash } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SharedFs, geesefsArgs, isMountPoint, platformKey, type SharedFsOptions } from "../src/fs/shared.js";
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

/** A fake geesefs release binary. */
function binary(): { data: Buffer; sha: string } {
  const data = Buffer.from("#!/bin/sh\nexit 0\n");
  return { data, sha: createHash("sha256").update(data).digest("hex") };
}

/** Tests stand for a mount point with a marker file in it. */
const marker = (d: string): boolean => existsSync(join(d, ".mounted"));

const NAME = "atu-0123456789abcdef";

function spec(sha: string, generation = 1): FsMount {
  return {
    name: NAME, bucket: "agent-teams-fs", prefix: "users/0123456789abcdef/", generation, endpoint: "https://s3.example", region: "us-west-004",
    access_key: "kid", secret_key: "sk-secret",
    client: { tool: "geesefs", version: "0.43.9", assets: [{ platform: "linux-amd64", url: "https://dl/geesefs-linux-amd64", sha256: sha }] },
  };
}

interface Rig { fs: SharedFs; sent: FsStatus[]; calls: { cmd: string; args: string[]; env?: NodeJS.ProcessEnv }[]; data: string; fetches: number; sha: string; procs: Map<string, number> }

function rig(over: Partial<SharedFsOptions> = {}, data = tmp()): Rig {
  const { data: bin, sha } = binary();
  const r: Rig = { sent: [], calls: [], data, fetches: 0, sha, procs: new Map() } as unknown as Rig;
  r.fs = new SharedFs({
    dataDir: r.data, preinstalledDir: join(r.data, "no-image"), log: silentLogger, send: (_t, b) => r.sent.push(b), fuse: () => true, platform: "linux-amd64", mountWaitMs: 1000, exitWaitMs: 1000,
    mounted: marker, procs: (d) => (r.procs.has(d) ? [r.procs.get(d)!] : []),
    fetchFn: (async () => (r.fetches++, new Response(bin))) as unknown as typeof fetch,
    run: async (cmd, args, opts) => {
      r.calls.push({ cmd, args, ...(opts?.env ? { env: opts.env } : {}) });
      const dir = args.at(-1)!;
      if (/\/geesefs[^/]*$/.test(cmd)) {
        writeFileSync(join(dir, ".mounted"), "");
        r.procs.set(dir, 4242);
      }
      if (cmd === "fusermount3") {
        rmSync(join(dir, ".mounted"), { force: true });
        // geesefs uploads what it holds, then exits a little later.
        setTimeout(() => r.procs.delete(dir), 50);
      }
      return { code: 0, stdout: "", stderr: "" };
    },
    ...over,
  });
  return r;
}

const mounts = (r: Rig) => r.calls.filter((c) => /\/geesefs[^/]*$/.test(c.cmd));

afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

describe("SharedFs", () => {
  it("downloads the pinned geesefs, mounts bucket:prefix with the key in the env only, and keeps a healthy mount", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha));
    expect(r.sent.map((s) => s.state)).toEqual(["mounting", "mounted"]);
    const mount = mounts(r)[0]!;
    expect(mount.cmd).toBe(join(r.data, "tools", "geesefs-0.43.9", "geesefs"));
    expect(readFileSync(mount.cmd, "utf8")).toContain("exit 0");
    expect(mount.args.join(" ")).not.toMatch(/sk-secret|kid/);
    expect(mount.env?.AWS_ACCESS_KEY_ID).toBe("kid");
    expect(mount.env?.AWS_SECRET_ACCESS_KEY).toBe("sk-secret");
    expect(mount.args.slice(-2)).toEqual(["agent-teams-fs:users/0123456789abcdef/", join(r.data, "mnt", NAME)]);
    expect(mount.args).toEqual(expect.arrayContaining(["--endpoint", "https://s3.example", "--region", "us-west-004", "--fsync-on-close"]));
    expect(r.fs.root()).toBe(join(r.data, "mnt", NAME));

    await r.fs.mount(spec(r.sha));
    expect(mounts(r)).toHaveLength(1);
    expect(r.fetches).toBe(1);

    await r.fs.mount(spec(r.sha, 2));
    expect(mounts(r)).toHaveLength(2);
    expect(r.calls.some((c) => c.cmd === "fusermount3")).toBe(true);
    expect(r.sent.at(-1)).toEqual({ state: "mounted", mode: "daemon", name: NAME, generation: 2 });
  });

  it("uses the image's geesefs when its sha256 matches, and downloads otherwise", async () => {
    const pre = tmp();
    writeFileSync(join(pre, "geesefs-0.43.9"), binary().data);
    const r = rig({ preinstalledDir: pre });
    await r.fs.mount(spec(r.sha));
    expect(mounts(r)[0]!.cmd).toBe(join(pre, "geesefs-0.43.9"));
    expect(r.fetches).toBe(0);

    writeFileSync(join(pre, "geesefs-0.43.9"), "tampered");
    const other = rig({ preinstalledDir: pre });
    await other.fs.mount(spec(other.sha));
    expect(mounts(other)[0]!.cmd).toBe(join(other.data, "tools", "geesefs-0.43.9", "geesefs"));
    expect(other.fetches).toBe(1);
  });

  it("mounts at AGENT_TEAMS_FS_MOUNT_DIR when set", async () => {
    const at = tmp();
    const r = rig({ mountDir: at });
    await r.fs.mount(spec(r.sha));
    expect(mounts(r)[0]!.args.at(-1)).toBe(at);
    expect(r.fs.root()).toBe(at);
  });

  it("ignores an fs.mount with an older generation than the mounted one", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha, 3));
    const sent = r.sent.length;
    await r.fs.mount(spec(r.sha, 2));
    expect(mounts(r)).toHaveLength(1);
    expect(r.calls.some((c) => c.cmd === "fusermount3")).toBe(false);
    expect(r.sent).toHaveLength(sent);
    expect(r.fs.root()).not.toBeNull();
  });

  it("adopts the mount of a previous daemon process instead of remounting it", async () => {
    const first = rig();
    await first.fs.mount(spec(first.sha, 2));
    const saved = readFileSync(join(first.data, "fs-mount.json"), "utf8");
    expect(saved).not.toContain("sk-secret");
    expect(JSON.parse(saved)).toEqual({ name: NAME, generation: 2 });

    const r = rig({}, first.data);
    expect(r.fs.root()).toBeNull();
    await r.fs.mount(spec(r.sha, 1));
    expect(r.calls).toEqual([]);
    await r.fs.mount(spec(r.sha, 2));
    expect(r.calls).toEqual([]);
    expect(r.sent.at(-1)).toEqual({ state: "mounted", mode: "daemon", name: NAME, generation: 2 });
    expect(r.fs.root()).toBe(join(r.data, "mnt", NAME));
  });

  it("unmounts a mount left by a previous daemon process", async () => {
    const first = rig();
    await first.fs.mount(spec(first.sha));
    const r = rig({}, first.data);
    await r.fs.unmount({ name: NAME });
    expect(r.calls.map((c) => c.cmd)).toEqual(["fusermount3"]);
    expect(existsSync(join(r.data, "fs-mount.json"))).toBe(false);
    expect(r.sent.at(-1)).toEqual({ state: "unmounted", mode: "daemon" });
  });

  it("ignores fs.unmount of another folder", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha));
    await r.fs.unmount({ name: "atu-ffffffffffffffff" });
    expect(r.calls.some((c) => c.cmd === "fusermount3")).toBe(false);
    expect(r.fs.root()).not.toBeNull();
  });

  it("has no root once the mount point is no longer mounted", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha));
    rmSync(join(r.data, "mnt", NAME, ".mounted"));
    expect(r.fs.root()).toBeNull();
    expect(r.fs.sessionDirs({ project: "projects/p1", shared: "shared" })).toBeNull();
    expect(existsSync(join(r.data, "mnt", NAME, "projects"))).toBe(false);
  });

  it("mounts again when the health check finds the mount gone", async () => {
    const r = rig({ checkMs: 20 });
    await r.fs.mount(spec(r.sha));
    r.fs.start();
    rmSync(join(r.data, "mnt", NAME, ".mounted"));
    await new Promise((res) => setTimeout(res, 300));
    r.fs.stop();
    expect(mounts(r).length).toBeGreaterThanOrEqual(2);
    expect(r.fs.root()).not.toBeNull();
  });

  it("waits for geesefs to exit on shutdown, so writes it holds are uploaded", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha));
    const dir = join(r.data, "mnt", NAME);
    expect(r.procs.has(dir)).toBe(true);
    await r.fs.shutdown();
    expect(r.calls.at(-1)!.cmd).toBe("fusermount3");
    expect(r.procs.has(dir)).toBe(false);
  });

  it("detaches a busy mount lazily", async () => {
    const calls: string[][] = [];
    const r = rig();
    await r.fs.mount(spec(r.sha));
    const busy = rig({
      run: async (cmd, args) => {
        calls.push([cmd, ...args]);
        if (args.includes("-z")) rmSync(join(args.at(-1)!, ".mounted"), { force: true });
        return { code: args.includes("-z") ? 0 : 1, stdout: "", stderr: "Device or resource busy" };
      },
    }, r.data);
    await busy.fs.unmount({ name: NAME });
    expect(calls.map((c) => c.slice(0, 3).join(" "))).toEqual(["fusermount3 -u " + join(r.data, "mnt", NAME), "fusermount3 -u -z"]);
  });

  it("refuses a client whose sha256 does not match", async () => {
    const r = rig();
    await r.fs.mount(spec("0".repeat(64)));
    expect(r.sent.at(-1)).toMatchObject({ state: "error", name: NAME, error: expect.stringContaining("sha256 mismatch") });
    expect(mounts(r)).toHaveLength(0);
  });

  it("refuses a mount client other than geesefs", async () => {
    const r = rig();
    const m = spec(r.sha);
    await r.fs.mount({ ...m, client: { ...m.client, tool: "rclone" } });
    expect(r.sent.at(-1)).toMatchObject({ state: "error", error: expect.stringContaining("geesefs only") });
  });

  it("reports a failed geesefs start", async () => {
    const r = rig({ run: async () => ({ code: 1, stdout: "", stderr: "starting\nAccess Denied" }) });
    await r.fs.mount(spec(r.sha));
    expect(r.sent.at(-1)).toMatchObject({ state: "error", error: "Access Denied" });
    expect(r.fs.root()).toBeNull();
  });

  it("reports an error without FUSE and does not advertise fs_s3_mount", async () => {
    const r = rig({ fuse: () => false });
    expect(r.fs.features()).toEqual([]);
    await r.fs.mount(spec("x"));
    expect(r.sent.at(-1)).toMatchObject({ state: "error", error: expect.stringContaining("FUSE") });
  });

  it("advertises fs_s3_mount with FUSE", () => {
    expect(rig().fs.features()).toEqual(["fs_s3_mount"]);
  });

  it("unmounts and forgets on fs.unmount", async () => {
    const r = rig();
    await r.fs.mount(spec(r.sha));
    await r.fs.unmount({ name: NAME });
    expect(r.sent.at(-1)).toEqual({ state: "unmounted", mode: "daemon" });
    expect(r.fs.root()).toBeNull();
  });

  it("creates the session directories and refuses paths outside the folder", async () => {
    const r = rig();
    expect(r.fs.sessionDirs({ project: "projects/p1", shared: "shared" })).toBeNull();
    await r.fs.mount(spec(r.sha));
    const d = r.fs.sessionDirs({ project: "projects/p1", shared: "shared" })!;
    expect(existsSync(d.project) && existsSync(d.shared)).toBe(true);
    expect(r.fs.sessionDirs({ project: "../../etc", shared: "shared" })).toBeNull();
  });

  it("uses an external mount without mounting anything", async () => {
    const ext = tmp();
    writeFileSync(join(ext, ".mounted"), "");
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

    writeFileSync(join(ext, ".mounted"), "");
    const r = rig({ externalDir: ext, checkMs: 10, onExternalLost: () => lost++ });
    r.fs.start();
    await new Promise((res) => setTimeout(res, 30));
    rmSync(join(ext, ".mounted"));
    await new Promise((res) => setTimeout(res, 50));
    r.fs.stop();
    expect(lost).toBe(1);
    expect(r.sent.at(-1)).toMatchObject({ state: "error", mode: "external" });
  });

  it("names platforms like the console", () => {
    expect(platformKey("linux", "x64")).toBe("linux-amd64");
    expect(platformKey("darwin", "arm64")).toBe("darwin-arm64");
  });

  it("mounts a whole bucket when the folder has no prefix", () => {
    expect(geesefsArgs({ ...spec("x"), bucket: NAME, prefix: "" }, "/m", "/log").slice(-2)).toEqual([NAME, "/m"]);
  });

  it("geesefs arguments never carry the key", () => {
    const args = geesefsArgs(spec("x"), "/m", "/log");
    expect(args.join(" ")).not.toContain("sk-secret");
    expect(args).toEqual(expect.arrayContaining(["--stat-cache-ttl", "5s", "--memory-limit", "1000", "--log-file", "/log"]));
  });

  it("knows a real mount point from a plain directory", () => {
    expect(isMountPoint(tmp())).toBe(false);
    expect(isMountPoint("/proc")).toBe(process.platform === "linux");
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
    for (const s of [".project-files/ → /mnt/fs/projects/p1", ".user-files/ → /mnt/fs/shared", ".attachments/ → /mnt/fs/projects/p1/attachments", ".deliverables", "do not write"]) expect(p).toContain(s);
  });

  it("explains how the folder works so the employee can decide what to keep there", () => {
    const p = sharedFolderPrompt({ project: "/mnt/fs/projects/p1", shared: "/mnt/fs/shared" });
    for (const s of ["geesefs over S3 object storage", "no file locks", "no hard links", "no file locks", "PostgreSQL", "SQLite", "local disk", "snapshot", "decide"]) expect(p).toContain(s);
    expect(p).not.toContain("one session at a time");
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
    writeFileSync(join(ext, ".mounted"), "");
    mkdirSync(join(ext, "shared"));
    h = await startHarness({ fsDir: ext, sharedFs: { mounted: marker } });
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
    writeFileSync(join(ext, ".mounted"), "");
    mkdirSync(join(ext, "shared"));
    h = await startHarness({ fsDir: ext, sharedFs: { mounted: marker } });
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
