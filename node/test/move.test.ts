import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { startHarness, type Harness, claudeOpen } from "./helpers/daemon.js";
import { ArchiveError, pack, unpack } from "../src/move/archive.js";
import { claudeProjectDirName, findRollout, rewritePaths } from "../src/move/transcripts.js";
import { Mover } from "../src/move/mover.js";
import type { MoveTransfer } from "../src/move/transfer.js";
import { silentLogger } from "../src/util/log.js";

const cwd = "projects/p1/threads/12/dev-2";

const git = (dir: string, ...args: string[]) => execFileSync("git", ["-C", dir, ...args], { encoding: "utf8", env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" } }).trim();

/** An in-memory /nodes/moves/{id} store shared by two daemons. */
function memoryTransfer(): MoveTransfer & { store: Map<string, Buffer> } {
  const store = new Map<string, Buffer>();
  return {
    store,
    async upload(id, data, sha) {
      expect(createHash("sha256").update(data).digest("hex")).toBe(sha);
      store.set(id, data);
    },
    async download(id) {
      const d = store.get(id);
      if (!d) throw new Error("download: HTTP 404");
      return d;
    },
  };
}

describe("move archive", () => {
  it("round-trips entries and refuses names that climb out", () => {
    const a = pack([{ name: "manifest.json", data: Buffer.from("{}") }, { name: "claude/session/sub/a.jsonl", data: Buffer.from("x\n") }]);
    expect(unpack(a).map((e) => [e.name, e.data.toString()])).toEqual([["manifest.json", "{}"], ["claude/session/sub/a.jsonl", "x\n"]]);
    for (const bad of ["../x", "/abs", "a//b", "a/./b", "a\\b"]) expect(() => pack([{ name: bad, data: Buffer.alloc(0) }])).toThrow(ArchiveError);
    expect(() => unpack(Buffer.from("not gzip"))).toThrow(ArchiveError);
  });

  it("rewrites the source node's paths in one pass, longest first", () => {
    const pairs: Array<[string, string]> = [["/home/a/.agents_team", "/home/b/.agents_team"], ["/home/a/.agents_team/agent-home", "/x"], ["/home/a/.claude", "/home/b/.claude"]];
    expect(rewritePaths('{"cwd":"/home/a/.agents_team/projects/p1","h":"/home/a/.agents_team/agent-home/f","c":"/home/a/.claude/plans/p.md"}', pairs))
      .toBe('{"cwd":"/home/b/.agents_team/projects/p1","h":"/x/f","c":"/home/b/.claude/plans/p.md"}');
    // a replacement that contains another source path is not rewritten again
    expect(rewritePaths("/r", [["/r", "/r/r"], ["/r/r", "/never"]])).toBe("/r/r");
  });

  it("names Claude project directories like the CLI", () => {
    expect(claudeProjectDirName("/home/claude/.agents_team/projects/p1/threads/12/dev-2")).toBe("-home-claude--agents-team-projects-p1-threads-12-dev-2");
    const long = `/${"x".repeat(250)}`;
    expect(claudeProjectDirName(long)).toMatch(new RegExp(`^-x{199}-[0-9a-z]+$`));
  });
});

describe("session moves between daemons", () => {
  const hs: Harness[] = [];
  afterEach(async () => {
    for (const h of hs.splice(0)) {
      const invalid = [...h.console.invalid];
      await h.close();
      expect(invalid).toEqual([]);
    }
  });

  /** remotes/acme/app.git with main and thread/12-comms, inside dir. */
  function makeRemote(dir: string): void {
    const bare = join(dir, "remotes", "acme", "app.git");
    mkdirSync(bare, { recursive: true });
    execFileSync("git", ["init", "--bare", "-b", "main", bare]);
    const seed = join(dir, "seed");
    execFileSync("git", ["clone", bare, seed], { stdio: "ignore" });
    writeFileSync(join(seed, "README.md"), "hello\n");
    writeFileSync(join(seed, "gone.txt"), "deleted later\n");
    git(seed, "add", ".");
    git(seed, "commit", "-m", "init");
    git(seed, "push", "origin", "HEAD:main");
    git(seed, "push", "origin", "HEAD:refs/heads/thread/12-comms");
  }

  const repo = `${cwd}/acme__app`;
  const prepare = (job: string) => ({ job_id: job, project_id: "p1", repo: "acme/app", mode: "worktree", shared: "projects/p1/repos/acme__app", cwd: repo, branch: "thread/12/dev-2", base: "thread/12-comms", fetch: true });
  const sid = "8d2f6c1e-0d6a-4f7e-9a11-5b2f3c4d5e6f";

  async function two(): Promise<{ a: Harness; b: Harness; t: ReturnType<typeof memoryTransfer> }> {
    const t = memoryTransfer();
    const a = await startHarness({ moveTransfer: t });
    hs.push(a);
    makeRemote(a.dir);
    const b = await startHarness({ moveTransfer: t, gitBaseUrl: a.opts.gitBaseUrl });
    hs.push(b);
    return { a, b, t };
  }

  it("moves a Claude session: transcript under the target's paths, commits and uncommitted work in the checkout", async () => {
    const { a, b, t } = await two();
    a.console.command("git.prepare", prepare("g_a1"));
    expect((await a.console.waitType("git.result", (x) => x.job_id === "g_a1")).body.ok).toBe(true);
    const wtA = join(a.opts.root, repo);
    writeFileSync(join(wtA, "feature.txt"), "committed\n");
    git(wtA, "add", ".");
    git(wtA, "commit", "-m", "local work");
    const headA = git(wtA, "rev-parse", "HEAD");
    writeFileSync(join(wtA, "README.md"), "hello\nedited\n");
    git(wtA, "rm", "-q", "gone.txt");
    writeFileSync(join(wtA, "staged.txt"), "staged\n");
    git(wtA, "add", "staged.txt");
    writeFileSync(join(wtA, "new.txt"), "untracked\n");
    mkdirSync(join(wtA, ".agent-notes"));
    writeFileSync(join(wtA, ".agent-notes", "t1.md"), "ignored notes\n"); // excluded: stays behind

    const rootA = resolve(a.opts.root);
    const projA = join(a.opts.claudeConfigDir, "projects", claudeProjectDirName(join(rootA, cwd)));
    mkdirSync(join(projA, sid, "subagents"), { recursive: true });
    writeFileSync(join(projA, `${sid}.jsonl`), `{"type":"user","uuid":"u1","cwd":"${join(rootA, cwd)}","message":{"content":"read ${join(rootA, repo)}/README.md"}}\n`);
    writeFileSync(join(projA, sid, "subagents", "agent-1.jsonl"), `{"cwd":"${join(rootA, cwd)}"}\n`);

    a.console.command("session.export", { export_id: "mv_01JD3V0000000000000000", session_id: "s_old", runtime: "claude", resume: { sdk_session_id: sid }, cwd, repos: [repo] });
    const exported = (await a.console.waitType("session.exported")).body;
    expect(exported).toMatchObject({ export_id: "mv_01JD3V0000000000000000", session_id: "s_old", ok: true });
    expect(exported.digest).toContain("- 收到：read README.md"); // the checkout's absolute path dropped
    expect(t.store.get("mv_01JD3V0000000000000000")?.length).toBe(exported.size);
    // the source checkout is untouched (no ref left behind, staged file still staged)
    expect(git(wtA, "for-each-ref", "refs/agent-teams")).toBe("");
    expect(git(wtA, "diff", "--cached", "--name-only")).toContain("staged.txt");

    b.console.command("git.prepare", prepare("g_b1"));
    b.console.command("session.open", claudeOpen("s_new", 1, { cwd, prepare_jobs: ["g_b1"], resume: { sdk_session_id: sid },
      import: { export_id: "mv_01JD3V0000000000000000", size: exported.size, sha256: exported.sha256 } }));
    const opened = (await b.console.waitType("session.opened", (x) => x.session_id === "s_new")).body;
    expect(opened.resume_failed).toBeUndefined();
    expect(opened.sdk_session_id).toBe(sid);
    expect(b.log.options.at(-1)?.resume).toBe(sid);

    const rootB = resolve(b.opts.root);
    const projB = join(b.opts.claudeConfigDir, "projects", claudeProjectDirName(join(rootB, cwd)));
    const moved = readFileSync(join(projB, `${sid}.jsonl`), "utf8");
    expect(moved).toContain(`"cwd":"${join(rootB, cwd)}"`);
    expect(moved).toContain(`read ${join(rootB, repo)}/README.md`);
    expect(moved).not.toContain(rootA);
    expect(readFileSync(join(projB, sid, "subagents", "agent-1.jsonl"), "utf8")).toBe(`{"cwd":"${join(rootB, cwd)}"}\n`);

    const wtB = join(b.opts.root, repo);
    expect(git(wtB, "rev-parse", "--abbrev-ref", "HEAD")).toBe("thread/12/dev-2");
    expect(git(wtB, "rev-parse", "HEAD")).toBe(headA);
    expect(readFileSync(join(wtB, "README.md"), "utf8")).toBe("hello\nedited\n");
    expect(readFileSync(join(wtB, "new.txt"), "utf8")).toBe("untracked\n");
    expect(existsSync(join(wtB, "gone.txt"))).toBe(false);
    expect(existsSync(join(wtB, ".agent-notes"))).toBe(false);
    expect(git(wtB, "diff", "--cached", "--name-only")).toBe("");
    expect(git(wtB, "status", "--porcelain").split("\n").map((l) => l.trim()).sort()).toEqual(["?? new.txt", "?? staged.txt", "D gone.txt", "M README.md"]);
  });

  it("answers resume_failed when the archive does not check out", async () => {
    const { a, b } = await two();
    const rootA = resolve(a.opts.root);
    const projA = join(a.opts.claudeConfigDir, "projects", claudeProjectDirName(join(rootA, cwd)));
    mkdirSync(projA, { recursive: true });
    writeFileSync(join(projA, `${sid}.jsonl`), "{}\n");
    a.console.command("session.export", { export_id: "mv_01JD3V0000000000000001", session_id: "s_old", runtime: "claude", resume: { sdk_session_id: sid }, cwd, repos: [] });
    const exported = (await a.console.waitType("session.exported")).body;
    expect(exported.ok).toBe(true);
    b.console.command("session.open", claudeOpen("s_new", 1, { cwd, resume: { sdk_session_id: sid },
      import: { export_id: "mv_01JD3V0000000000000001", size: exported.size, sha256: "0".repeat(64) } }));
    const opened = (await b.console.waitType("session.opened", (x) => x.session_id === "s_new")).body;
    expect(opened).toMatchObject({ resume_failed: true });
    expect(opened.error).toContain("sha256");
  });

  it("reports an export without a transcript as a failure", async () => {
    const { a } = await two();
    a.console.command("session.export", { export_id: "mv_01JD3V0000000000000002", session_id: "s_old", runtime: "claude", resume: { sdk_session_id: sid }, cwd, repos: [] });
    expect((await a.console.waitType("session.exported")).body).toMatchObject({ ok: false, error: expect.stringContaining("not found") });
  });
});

describe("Codex session moves", () => {
  it("copies the rollout under the target's CODEX_HOME with its paths rewritten", async () => {
    const dir = mkdtempSync(join(tmpdir(), "at-move-"));
    try {
      const t = memoryTransfer();
      const mk = (n: string) => {
        const root = join(dir, n, "root");
        mkdirSync(root, { recursive: true });
        return new Mover({ root, claudeConfigDir: join(dir, n, "claude"), codexHome: join(dir, n, "codex"), tmpDir: join(dir, n), hooksDir: join(dir, n, "hooks"), transfer: t, log: silentLogger });
      };
      const a = mk("a");
      const b = mk("b");
      const thread = "019a0000-1111-2222-3333-444455556666";
      const rel = `sessions/2026/11/22/rollout-2026-11-22T10-00-00-${thread}.jsonl`;
      mkdirSync(join(dir, "a", "codex", "sessions/2026/11/22"), { recursive: true });
      writeFileSync(join(dir, "a", "codex", rel), `{"type":"session_meta","payload":{"cwd":"${join(dir, "a", "root")}/${cwd}"}}\n`);
      const res = await a.export({ export_id: "mv_01JD3V0000000000000003", session_id: "s_old", runtime: "codex", resume: { codex_thread_id: thread }, cwd, repos: [] });
      expect(res.ok).toBe(true);
      await b.import({ ...claudeOpen("s_new"), runtime: "codex", claude: null, codex: { model: "", sandbox: "workspace-write", approval_policy: "never" }, resume: { codex_thread_id: thread },
        import: { export_id: "mv_01JD3V0000000000000003", size: res.size!, sha256: res.sha256! } } as never, join(dir, "b", "root", cwd));
      const placed = findRollout(join(dir, "b", "codex"), thread);
      expect(placed).toBe(join(dir, "b", "codex", rel));
      expect(readFileSync(placed!, "utf8")).toContain(`"cwd":"${join(dir, "b", "root")}/${cwd}"`);
      // the conversation id must match the session it goes into
      await expect(b.import({ ...claudeOpen("s_x"), runtime: "codex", resume: { codex_thread_id: "other" },
        import: { export_id: "mv_01JD3V0000000000000003", size: res.size!, sha256: res.sha256! } } as never, join(dir, "b", "root", cwd))).rejects.toThrow(/not other/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
