import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SharedFs, platformKey, type SharedFsOptions } from "../src/fs/shared.js";
import { linkSharedDirs, withSharedDirs } from "../src/fs/session.js";
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

function rig(over: Partial<SharedFsOptions> = {}): Rig {
  // One tarball per rig: two builds can differ (gzip stores a timestamp), so its own sha is used.
  const { data: tgz, sha } = tarball();
  const r: Rig = { sent: [], calls: [], data: tmp(), fetches: 0, sha } as unknown as Rig;
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
  });
});
