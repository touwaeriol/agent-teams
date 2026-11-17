import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CredBroker } from "../src/cred/broker.js";
import { ghMain, ghPlan, repoForGh, repoSlugPath } from "../src/cred/helper.js";
import { CredServer } from "../src/cred/server.js";
import { downloadGh, GhInstaller, ghOnPath, parseChecksums, type GhRelease } from "../src/git/gh-install.js";
import { installPlatformFiles } from "../src/git/platform.js";
import { decideTool, type ToolPolicy } from "../src/sessions/policy.js";
import { silentLogger } from "../src/util/log.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

let dir: string;
beforeEach(() => (dir = mkdtempSync(join(tmpdir(), "at-gh-"))));
afterEach(() => rmSync(dir, { recursive: true, force: true }));

const FAKE_GH = `#!/bin/sh
printf 'TOKEN=%s GITHUB=%s CFG=%s HOST=%s SECRET=%s ARGS=%s\\n' "$GH_TOKEN" "$GITHUB_TOKEN" "$GH_CONFIG_DIR" "$GH_HOST" "\${AT_CRED_SECRET:-none}" "$*" > "$GH_OUT"
`;

/** A release tarball gh_<v>_linux_amd64.tar.gz with bin/gh = FAKE_GH, and its checksums file. */
function fakeRelease(version: string): { files: Record<string, Buffer>; release: GhRelease } {
  const name = `gh_${version}_linux_amd64`;
  const src = join(dir, "build", name, "bin");
  mkdirSync(src, { recursive: true });
  writeFileSync(join(src, "gh"), FAKE_GH, { mode: 0o755 });
  const tgz = join(dir, "build", `${name}.tar.gz`);
  execFileSync("tar", ["-czf", tgz, "-C", join(dir, "build"), name]);
  const data = readFileSync(tgz);
  const sha = createHash("sha256").update(data).digest("hex");
  return {
    files: { [`${name}.tar.gz`]: data, [`gh_${version}_checksums.txt`]: Buffer.from(`${"0".repeat(64)}  gh_${version}_linux_arm64.tar.gz\n${sha}  ${name}.tar.gz\n`) },
    release: { version, assets: { "linux-x64": { name: `${name}.tar.gz`, sha256: sha } } },
  };
}

async function serve(files: Record<string, Buffer>): Promise<{ url: string; server: Server; hits: string[] }> {
  const hits: string[] = [];
  const server = createServer((req, res) => {
    hits.push(req.url ?? "");
    const name = (req.url ?? "").split("/").pop()!;
    const body = files[name];
    res.statusCode = body ? 200 : 404;
    res.end(body ?? "not found");
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", () => r()));
  const addr = server.address() as { port: number };
  return { url: `http://127.0.0.1:${addr.port}/download`, server, hits };
}

describe("gh download", () => {
  it("downloads the pinned release, checks it against the pinned hash and the checksums file, and reuses it", async () => {
    const { files, release } = fakeRelease("9.9.9");
    const srv = await serve(files);
    try {
      const toolsDir = join(dir, "tools");
      const installer = new GhInstaller({ toolsDir, release, baseUrl: srv.url, platform: "linux", arch: "x64", path: "" });
      const [a, b] = await Promise.all([installer.resolve(), installer.resolve()]);
      expect(a).toBe(join(toolsDir, "gh-9.9.9", "bin", "gh"));
      expect(b).toBe(a);
      expect(statSync(a).mode & 0o111).not.toBe(0);
      expect(srv.hits).toEqual(["/download/v9.9.9/gh_9.9.9_checksums.txt", "/download/v9.9.9/gh_9.9.9_linux_amd64.tar.gz"]);
      expect(await installer.resolve()).toBe(a);
      expect(srv.hits.length).toBe(2);
      expect(readdirSync(toolsDir)).toEqual(["gh-9.9.9"]);
    } finally {
      srv.server.close();
    }
  });

  it("refuses a tarball whose hash differs from the pin or the checksums file", async () => {
    const { files, release } = fakeRelease("9.9.8");
    const srv = await serve(files);
    try {
      const pinned = { ...release, assets: { "linux-x64": { ...release.assets["linux-x64"]!, sha256: "f".repeat(64) } } };
      await expect(downloadGh({ toolsDir: join(dir, "t1"), release: pinned, baseUrl: srv.url, platform: "linux", arch: "x64" })).rejects.toThrow(/sha256 mismatch/);
      files["gh_9.9.8_checksums.txt"] = Buffer.from(`${"e".repeat(64)}  gh_9.9.8_linux_amd64.tar.gz\n`);
      await expect(downloadGh({ toolsDir: join(dir, "t2"), release, baseUrl: srv.url, platform: "linux", arch: "x64" })).rejects.toThrow(/sha256 mismatch/);
      await expect(downloadGh({ toolsDir: join(dir, "t3"), release, baseUrl: srv.url, platform: "freebsd", arch: "x64" })).rejects.toThrow(/no gh release/);
      expect(existsSync(join(dir, "t1", "gh-9.9.8")) || existsSync(join(dir, "t2", "gh-9.9.8"))).toBe(false);
    } finally {
      srv.server.close();
    }
  });

  it("prefers a gh already on PATH, skipping the platform wrapper directory", async () => {
    const wrapper = join(dir, "wrapper");
    const real = join(dir, "usr-bin");
    for (const d of [wrapper, real]) {
      mkdirSync(d);
      writeFileSync(join(d, "gh"), "#!/bin/sh\n", { mode: 0o755 });
    }
    expect(ghOnPath(`${wrapper}:${real}`, [wrapper])).toBe(join(real, "gh"));
    expect(await new GhInstaller({ toolsDir: join(dir, "tools"), path: `${wrapper}:${real}`, skip: [wrapper] }).resolve()).toBe(join(real, "gh"));
    expect(parseChecksums("abc  x\n" + "a".repeat(64) + "  gh.tgz\n").get("gh.tgz")).toBe("a".repeat(64));
  });
});

describe("gh wrapper", () => {
  function repoWithOrigin(path: string, url: string): void {
    mkdirSync(path, { recursive: true });
    execFileSync("git", ["init", "-q", path]);
    execFileSync("git", ["-C", path, "remote", "add", "origin", url]);
  }

  it("runs the real gh with a per-session token only in the child's environment", async () => {
    const realGh = join(dir, "tools", "gh");
    mkdirSync(join(dir, "tools"));
    writeFileSync(realGh, FAKE_GH, { mode: 0o755 });
    chmodSync(realGh, 0o755);
    const requests: Array<Record<string, any>> = [];
    let broker: CredBroker;
    broker = new CredBroker((_t, body) => {
      requests.push(body);
      const b = body as { req_id: string; path: string };
      const ok = b.path === "acme/app.git";
      setTimeout(() => broker.resolve(ok ? { req_id: b.req_id, ok: true, username: "x-access-token", password: "ghs_SECRET_42", expires_at: new Date(Date.now() + 3600_000).toISOString() } : { req_id: b.req_id, ok: false, error: "repo_not_in_project" }), 5);
    });
    const configRoot = join(dir, "data", "gh-config");
    const server = new CredServer(join(dir, "run", "cred.sock"), broker, silentLogger, { gh: async () => realGh, ghConfigRoot: configRoot });
    await server.start();
    const work = join(dir, "work", "dev-1");
    repoWithOrigin(join(work, "acme__app"), "https://github.com/acme/app.git");
    try {
      const secret = server.register("s_1", 2);
      const out = join(dir, "gh.out");
      const env = { PATH: process.env.PATH, GH_OUT: out, AT_CRED_SOCK: server.socketPath, AT_CRED_SESSION: "s_1", AT_CRED_SECRET: secret };
      // From the employee directory: the only checkout is picked.
      expect(await ghMain(["pr", "list"], env, work)).toBe(0);
      const cfg = join(configRoot, "s_1");
      expect(readFileSync(out, "utf8").trim()).toBe(`TOKEN=ghs_SECRET_42 GITHUB=ghs_SECRET_42 CFG=${cfg} HOST=github.com SECRET=none ARGS=pr list`);
      expect(requests[0]).toMatchObject({ session_id: "s_1", open_gen: 2, path: "acme/app.git", purpose: "read" });
      // Another repository: no token, gh still runs.
      const plan = await ghPlan(["issue", "list", "-R", "evil/repo"], env, work);
      expect(plan).toMatchObject({ bin: realGh, note: expect.stringContaining("repo_not_in_project") });
      expect((plan as { env: NodeJS.ProcessEnv }).env.GH_TOKEN).toBeUndefined();
      // Wrong secret: nothing.
      expect(await ghPlan(["pr", "list"], { ...env, AT_CRED_SECRET: "nope" }, work)).toEqual({ error: "unknown session" });
      // The token is in no file: workdir, config dir, socket dir.
      rmSync(out);
      for (const root of [join(dir, "work"), join(dir, "data"), join(dir, "run")]) expect(filesContaining(root, "ghs_SECRET_42")).toEqual([]);
      server.unregister("s_1");
      expect(existsSync(cfg)).toBe(false);
    } finally {
      await server.stop();
    }
  });

  it("reports a node without gh clearly", async () => {
    const server = new CredServer(join(dir, "run", "cred.sock"), new CredBroker(() => {}), silentLogger, { gh: async () => { throw new Error("offline"); } });
    await server.start();
    try {
      const secret = server.register("s_2", 1);
      expect(await ghPlan(["pr", "list"], { AT_CRED_SOCK: server.socketPath, AT_CRED_SESSION: "s_2", AT_CRED_SECRET: secret }, dir)).toEqual({ error: "gh is not available on this node: offline" });
    } finally {
      await server.stop();
    }
  });

  it("finds the repository from -R, GH_REPO or the checkout", () => {
    repoWithOrigin(join(dir, "a"), "git@github.com:acme/a.git");
    expect(repoForGh(["pr", "view", "-R", "acme/x"], {}, dir)).toBe("acme/x.git");
    expect(repoForGh(["pr", "view", "--repo=github.com/acme/y"], {}, dir)).toBe("acme/y.git");
    expect(repoForGh(["pr", "view"], { GH_REPO: "acme/z" }, dir)).toBe("acme/z.git");
    expect(repoForGh(["pr", "view"], {}, join(dir, "a"))).toBe("acme/a.git");
    expect(repoForGh(["pr", "view"], {}, dir)).toBe("acme/a.git");
    repoWithOrigin(join(dir, "b"), "https://github.com/acme/b.git");
    expect(repoForGh(["pr", "view"], {}, dir)).toBeNull();
    expect(repoSlugPath("https://github.com/acme/q")).toBe("acme/q.git");
  });
});

function filesContaining(root: string, needle: string): string[] {
  if (!existsSync(root)) return [];
  const hits: string[] = [];
  for (const name of readdirSync(root, { recursive: true }) as string[]) {
    const p = join(root, name);
    try {
      if (statSync(p).isFile() && readFileSync(p).includes(needle)) hits.push(p);
    } catch {
      // sockets and vanished files
    }
  }
  return hits;
}

describe("pre-push hook", () => {
  const git = (cwd: string, env: Record<string, string>, ...args: string[]) =>
    execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t", ...env } });
  const tryPush = (cwd: string, env: Record<string, string>, ref: string): string => {
    try {
      git(cwd, env, "push", "origin", `HEAD:${ref}`);
      return "ok";
    } catch (e) {
      return String((e as { stderr?: string }).stderr ?? e);
    }
  };

  it("lets an employee push its own and new branches but not protected, other thread branches or tags", () => {
    const platform = installPlatformFiles(join(dir, "platform"), ["node", "cli"]);
    const bare = join(dir, "remote.git");
    execFileSync("git", ["init", "-q", "--bare", "-b", "main", bare]);
    const wt = join(dir, "wt");
    execFileSync("git", ["clone", "-q", bare, wt], { stdio: "ignore" });
    git(wt, {}, "checkout", "-q", "-b", "thread/12/dev-2");
    writeFileSync(join(wt, "f.txt"), "x\n");
    git(wt, {}, "add", ".");
    git(wt, {}, "commit", "-q", "-m", "work");
    git(wt, {}, "config", "core.hooksPath", platform.hooksDir);
    const env = { AT_PROTECTED_REFS: "thread/12-comms develop" };
    expect(tryPush(wt, env, "refs/heads/thread/12/dev-2")).toBe("ok");
    expect(tryPush(wt, env, "refs/heads/feature/login")).toBe("ok");
    for (const ref of ["refs/heads/main", "refs/heads/master", "refs/heads/develop", "refs/heads/thread/12-comms"]) expect(tryPush(wt, env, ref)).toContain("protected branch");
    expect(tryPush(wt, env, "refs/heads/thread/12/dev-3")).toContain("thread/* branches belong to the platform");
    expect(tryPush(wt, env, "refs/tags/v1")).toContain("only branches");
    expect(tryPush(wt, {}, "refs/heads/main")).toContain("protected branch");
  });
});

describe("gh and git push policy", () => {
  const policy: ToolPolicy = { denyNonPlatform: false, readOnly: false, bash: null, readRoots: [], secretPaths: [] };
  const bash = (command: string) => decideTool(policy, "Bash", { command }, "/w");
  it("blocks hook bypass, merging with gh and printing tokens; allows gh and git push", () => {
    expect(bash("git push --no-verify origin HEAD:main")).toMatchObject({ deny: expect.stringContaining("no-verify") });
    expect(bash("git -c core.hooksPath=/dev/null push origin HEAD:main")).toMatchObject({ deny: expect.stringContaining("hooks") });
    expect(bash("gh pr merge 12 --squash")).toMatchObject({ deny: expect.stringContaining("merge queue") });
    expect(bash("gh api -X PUT repos/a/b/pulls/3/merge")).toMatchObject({ deny: expect.stringContaining("merge queue") });
    expect(bash("gh auth token")).toMatchObject({ deny: expect.stringContaining("tokens") });
    expect(bash("printf 'host=github.com\\n' | git credential fill")).toMatchObject({ deny: expect.stringContaining("tokens") });
    expect(bash("gh pr create --title x --body y")).toBeNull();
    expect(bash("git push -u origin HEAD")).toBeNull();
  });
});

describe("session environment", () => {
  let h: Harness;
  afterEach(async () => h && (await h.close()));

  it("puts the gh wrapper first on PATH for employees with credentials and passes the protected refs", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_G", 1, { git_credentials: true, env: { AT_PROTECTED_REFS: "thread/12-comms main" } }));
    await h.console.waitType("session.opened");
    const env = h.log.options[0]!.env!;
    expect(env.PATH!.split(":")[0]).toBe(join(h.opts.platformDir, "bin"));
    expect(env.AT_PROTECTED_REFS).toBe("thread/12-comms main");
    expect(env.AT_CRED_SOCK).toBeTruthy();
    expect(Object.values(env).some((v) => /gh[sp]_/.test(v ?? ""))).toBe(false);
    expect(readFileSync(join(h.opts.platformDir, "bin", "gh"), "utf8")).toContain(" gh \"$@\"");
  });

  it("gives the coordinator neither the wrapper nor credentials", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_C", 1, { git_credentials: true, employee: { name: "coordinator", role: "coordinator", tier: "coordinator", project_id: "p1" }, cwd: "projects/p1/coordinator" }));
    await h.console.waitType("session.opened");
    const env = h.log.options[0]!.env!;
    expect(env.PATH!.split(":")[0]).not.toBe(join(h.opts.platformDir, "bin"));
    expect(env.AT_CRED_SOCK).toBeUndefined();
  });
});
