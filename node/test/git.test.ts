import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { startHarness, type Harness, claudeOpen } from "./helpers/daemon.js";
import { driverOf, disableFilters } from "../src/git/safe.js";
import { gitEnv } from "../src/git/exec.js";
import { waitIdle } from "../src/git/jobs.js";

let h: Harness;
afterEach(async () => {
  const invalid = [...h.console.invalid];
  await h.close();
  expect(invalid).toEqual([]);
});

const git = (cwd: string, ...args: string[]) => execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8", env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" } }).trim();

/** remotes/acme/<name>.git with main and the thread branch thread/12-comms. */
function makeRemote(dir: string, name = "app"): string {
  const bare = join(dir, "remotes", "acme", `${name}.git`);
  mkdirSync(bare, { recursive: true });
  execFileSync("git", ["init", "--bare", "-b", "main", bare]);
  const seed = join(dir, `seed-${name}`);
  execFileSync("git", ["clone", bare, seed], { stdio: "ignore" });
  writeFileSync(join(seed, "README.md"), "hello\n");
  git(seed, "add", ".");
  git(seed, "commit", "-m", "init");
  git(seed, "push", "origin", "HEAD:main");
  git(seed, "push", "origin", "HEAD:refs/heads/thread/12-comms");
  return bare;
}

const prepare = (job: string, name: string) => ({
  job_id: job, project_id: "p1", repo: "acme/app", mode: "worktree", shared: "projects/p1/repo",
  cwd: `projects/p1/threads/12/${name}`, branch: `thread/12/${name}`, base: "thread/12-comms", fetch: true,
});

describe("git jobs", () => {
  it("prepares a worktree on a local bare repo, reports status, pushes and cleans up", async () => {
    h = await startHarness();
    const bare = makeRemote(h.dir);
    h.console.command("git.prepare", prepare("g_1", "dev-2"));
    const res = await h.console.waitType("git.result", (b) => b.job_id === "g_1");
    expect(res.body).toMatchObject({ ok: true, created: true });
    const wt = join(h.opts.root, "projects/p1/threads/12/dev-2");
    expect(git(wt, "rev-parse", "--abbrev-ref", "HEAD")).toBe("thread/12/dev-2");
    expect(res.body.head_sha).toBe(git(wt, "rev-parse", "HEAD"));
    expect(git(join(h.opts.root, "projects/p1/repo"), "config", "core.hooksPath")).toBe(join(h.opts.platformDir, "hooks"));

    h.console.command("git.prepare", prepare("g_2", "dev-2"));
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_2")).body).toMatchObject({ ok: true, created: false });

    writeFileSync(join(wt, "feature.txt"), "work\n");
    git(wt, "add", ".");
    git(wt, "commit", "-m", "feature");
    const blocked = (() => {
      try {
        git(wt, "push", "origin", "HEAD:refs/heads/main");
        return "pushed";
      } catch (e) {
        return String((e as { stderr?: string }).stderr ?? e);
      }
    })();
    expect(blocked).toContain("push_branch");

    h.console.command("git.status", { job_id: "g_3", cwd: "projects/p1/threads/12/dev-2" });
    const st = await h.console.waitType("git.result", (b) => b.job_id === "g_3");
    expect(st.body.status).toMatchObject({ branch: "thread/12/dev-2", dirty_files: 0, unpushed_commits: 1 });

    h.console.command("git.push", { job_id: "g_4", project_id: "p1", repo: "acme/app", cwd: "projects/p1/threads/12/dev-2", refspec: "HEAD:refs/heads/thread/12/lead-x", employee: "dev-2", thread_no: 12 });
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_4")).body).toMatchObject({ ok: false, error_code: "refspec_not_allowed" });

    h.console.command("git.push", { job_id: "g_5", project_id: "p1", repo: "acme/app", cwd: "projects/p1/threads/12/dev-2", refspec: "HEAD:refs/heads/thread/12/dev-2", employee: "dev-2", thread_no: 12 });
    const pushed = await h.console.waitType("git.result", (b) => b.job_id === "g_5");
    expect(pushed.body).toMatchObject({ ok: true });
    expect(execFileSync("git", ["--git-dir", bare, "rev-parse", "thread/12/dev-2"], { encoding: "utf8" }).trim()).toBe(pushed.body.head_sha);

    writeFileSync(join(wt, "dirty.txt"), "x\n");
    h.console.command("git.cleanup", { job_id: "g_6", shared: "projects/p1/repo", paths: ["projects/p1/threads/12/dev-2"], keep_unpushed: true, delete_local_branches: true });
    const kept = await h.console.waitType("git.result", (b) => b.job_id === "g_6");
    expect(kept.body.kept[0].reason).toMatch(/uncommitted/);
    h.console.command("git.cleanup", { job_id: "g_7", shared: "projects/p1/repo", paths: ["projects/p1/threads/12/dev-2"], keep_unpushed: false, delete_local_branches: true });
    const removed = await h.console.waitType("git.result", (b) => b.job_id === "g_7");
    expect(removed.body.removed).toEqual(["projects/p1/threads/12/dev-2"]);
    expect(existsSync(wt)).toBe(false);
    expect(git(join(h.opts.root, "projects/p1/repo"), "branch", "--list", "thread/12/dev-2")).toBe("");
  });

  it("re-sends the original git.result for a repeated job instead of running it again", async () => {
    h = await startHarness();
    makeRemote(h.dir);
    h.console.command("git.prepare", prepare("g_10", "qa-1"));
    const first = await h.console.waitType("git.result", (b) => b.job_id === "g_10");
    h.console.command("git.prepare", { ...prepare("g_10", "qa-1"), branch: "something/else" });
    await h.console.waitFor((f) => f.type === "git.result" && f.body.job_id === "g_10" && f !== first);
    const results = h.console.ofType("git.result").filter((f) => f.body.job_id === "g_10");
    expect(results[1]!.body).toEqual(first.body);
  });

  it("opens a session only after its git.prepare job succeeded", async () => {
    h = await startHarness();
    makeRemote(h.dir);
    h.console.command("session.open", claudeOpen("s_G1", 1, { cwd: "projects/p1/threads/12/dev-3", prepare: "g_20" }));
    await new Promise((r) => setTimeout(r, 300));
    expect(h.console.ofType("session.opened")).toEqual([]);
    h.console.command("git.prepare", prepare("g_20", "dev-3"));
    const opened = await h.console.waitType("session.opened");
    expect(opened.body.session_id).toBe("s_G1");
    expect(readFileSync(join(h.opts.root, "projects/p1/threads/12/dev-3/README.md"), "utf8")).toBe("hello\n");
  });

  it("checks out each repository under the employee directory and skips pushing an unchanged one", async () => {
    h = await startHarness();
    makeRemote(h.dir, "app");
    const web = makeRemote(h.dir, "web");
    const repoPrepare = (job: string, repo: string, base: string) => ({
      job_id: job, project_id: "p1", repo, mode: "worktree", shared: `projects/p1/repos/${repo.replace("/", "__")}`,
      cwd: `projects/p1/threads/12/dev-4/${repo.replace("/", "__")}`, branch: "thread/12/dev-4", base, fetch: true,
    });
    h.console.command("session.open", claudeOpen("s_M1", 1, { cwd: "projects/p1/threads/12/dev-4", prepare: "g_40", prepare_jobs: ["g_40", "g_41"] }));
    h.console.command("git.prepare", repoPrepare("g_40", "acme/app", "main"));
    await h.console.waitType("git.result", (b) => b.job_id === "g_40");
    await new Promise((r) => setTimeout(r, 300));
    expect(h.console.ofType("session.opened")).toEqual([]);
    h.console.command("git.prepare", repoPrepare("g_41", "acme/web", "thread/12-comms"));
    expect((await h.console.waitType("session.opened")).body.session_id).toBe("s_M1");
    const appDir = join(h.opts.root, "projects/p1/threads/12/dev-4/acme__app");
    const webDir = join(h.opts.root, "projects/p1/threads/12/dev-4/acme__web");
    expect(git(appDir, "rev-parse", "--abbrev-ref", "HEAD")).toBe("thread/12/dev-4");
    expect(readFileSync(join(webDir, "README.md"), "utf8")).toBe("hello\n");

    writeFileSync(join(webDir, "page.txt"), "work\n");
    git(webDir, "add", ".");
    git(webDir, "commit", "-m", "page");
    const push = (job: string, repo: string, base: string) => ({ job_id: job, project_id: "p1", repo, cwd: `projects/p1/threads/12/dev-4/${repo.replace("/", "__")}`,
      refspec: "HEAD:refs/heads/thread/12/dev-4", employee: "dev-4", thread_no: 12, unchanged_against: ["thread/12-comms", base] });
    h.console.command("git.push", push("g_42", "acme/app", "main"));
    const app = await h.console.waitType("git.result", (b) => b.job_id === "g_42");
    expect(app.body).toMatchObject({ ok: true, unchanged: true, head_sha: git(appDir, "rev-parse", "HEAD") });
    expect(() => execFileSync("git", ["--git-dir", join(h.dir, "remotes/acme/app.git"), "rev-parse", "--verify", "thread/12/dev-4"], { stdio: "ignore" })).toThrow();
    h.console.command("git.push", push("g_43", "acme/web", "main"));
    const pushed = await h.console.waitType("git.result", (b) => b.job_id === "g_43");
    expect(pushed.body.ok).toBe(true);
    expect(pushed.body.unchanged).toBeUndefined();
    expect(execFileSync("git", ["--git-dir", web, "rev-parse", "thread/12/dev-4"], { encoding: "utf8" }).trim()).toBe(pushed.body.head_sha);
  });

  it("refuses paths outside the root and purges a project directory", async () => {
    h = await startHarness();
    h.console.command("git.status", { job_id: "g_30", cwd: "../../etc" });
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_30")).body).toMatchObject({ ok: false, error_code: "path_outside_root" });
    mkdirSync(join(h.opts.root, "projects/p9/repo"), { recursive: true });
    mkdirSync(join(h.opts.root, "projects/p9/threads/3/lead"), { recursive: true });
    h.console.command("git.purge", { job_id: "g_32", path: "projects/p9/threads/3" });
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_32")).body.ok).toBe(true);
    expect(existsSync(join(h.opts.root, "projects/p9/threads/3"))).toBe(false);
    expect(existsSync(join(h.opts.root, "projects/p9/repo"))).toBe(true);
    h.console.command("git.purge", { job_id: "g_31", path: "projects/p9" });
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_31")).body.ok).toBe(true);
    expect(existsSync(join(h.opts.root, "projects/p9"))).toBe(false);
  });

  it("ignores hostile repo config an employee planted in the shared clone", async () => {
    h = await startHarness();
    const bare = makeRemote(h.dir);
    h.console.command("git.prepare", prepare("g_40", "dev-4"));
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_40")).body.ok).toBe(true);
    const wt = join(h.opts.root, "projects/p1/threads/12/dev-4");
    const marker = join(h.dir, "pwned");
    const script = join(h.dir, "evil.sh");
    writeFileSync(script, `#!/bin/sh\necho "$@" >> ${marker}\ncat\n`, { mode: 0o755 });
    const decoy = join(h.dir, "decoy.git");
    execFileSync("git", ["init", "--bare", "-q", decoy]);
    writeFileSync(join(wt, ".gitattributes"), "* filter=evil\n");
    writeFileSync(join(wt, "f.txt"), "x\n");
    git(wt, "add", ".");
    git(wt, "commit", "-m", "work");
    git(wt, "config", "core.fsmonitor", script);
    git(wt, "config", "filter.evil.clean", script);
    git(wt, "config", "remote.origin.url", decoy);
    git(wt, "config", "remote.origin.pushurl", decoy);
    git(wt, "config", `url.${decoy}.insteadOf`, `file://${join(h.dir, "remotes")}`);
    writeFileSync(join(wt, "f.txt"), "changed\n");

    h.console.command("git.status", { job_id: "g_41", cwd: "projects/p1/threads/12/dev-4" });
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_41")).body.ok).toBe(true);
    h.console.command("git.push", { job_id: "g_42", project_id: "p1", repo: "acme/app", cwd: "projects/p1/threads/12/dev-4", refspec: "HEAD:refs/heads/thread/12/dev-4", employee: "dev-4", thread_no: 12 });
    const pushed = await h.console.waitType("git.result", (b) => b.job_id === "g_42");
    expect(pushed.body).toMatchObject({ ok: true });
    h.console.command("git.prepare", prepare("g_43", "dev-5"));
    expect((await h.console.waitType("git.result", (b) => b.job_id === "g_43")).body.ok).toBe(true);

    expect(existsSync(marker) ? readFileSync(marker, "utf8") : "").toBe("");
    expect(execFileSync("git", ["--git-dir", bare, "rev-parse", "thread/12/dev-4"], { encoding: "utf8" }).trim()).toBe(pushed.body.head_sha);
    expect(execFileSync("git", ["--git-dir", decoy, "for-each-ref"], { encoding: "utf8" }).trim()).toBe("");
  });
});

describe("git hardening helpers", () => {
  it("finds filter driver names and disables them", () => {
    expect(driverOf("filter.lfs.clean\ngit-lfs clean -- %f")).toBe("lfs");
    expect(driverOf("filter.a.b.process\nx")).toBe("a.b");
    expect(driverOf("core.fsmonitor\nx")).toBeNull();
    expect(disableFilters(["lfs"])).toContain("filter.lfs.process=");
  });

  it("keeps AGENT_TEAMS_* and inherited GIT_CONFIG_* out of git's environment", () => {
    const env = gitEnv({}, { PATH: "/bin", AGENT_TEAMS_NODE_TOKEN: "secret", GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: "core.fsmonitor" });
    expect(env.AGENT_TEAMS_NODE_TOKEN).toBeUndefined();
    expect(env.GIT_CONFIG_COUNT).toBeUndefined();
    expect(env.PATH).toBe("/bin");
  });
});

describe("waitIdle", () => {
  it("waits for a session that is still stopping, and gives up after the timeout", async () => {
    let busy = true;
    setTimeout(() => (busy = false), 30);
    expect(await waitIdle(() => busy, 1000, 10)).toBe(true);
    expect(await waitIdle(() => true, 30, 10)).toBe(false);
    expect(await waitIdle(() => false, 0)).toBe(true);
  });
});
