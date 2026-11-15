import { existsSync, readdirSync, rmSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import type { GitCleanup, GitKeptPath, GitPrepare, GitPurge, GitPush, GitResult, GitStatus } from "../protocol.js";
import { deferred, withTimeout, type Deferred } from "../util/async.js";
import type { Logger } from "../util/log.js";
import { PathOutsideRoot, resolveInRoot } from "../util/paths.js";
import { redact } from "../util/redact.js";
import { gitEnv, run, type RunResult } from "./exec.js";
import { LockTimeout, withLock } from "./lock.js";
import type { PlatformFiles } from "./platform.js";

export type JobCredential = (jobId: string, repo: string, purpose: "read" | "push") => Promise<{ username: string; password: string } | null>;
type ErrorCode = NonNullable<GitResult["error_code"]>;

export interface GitJobsOptions {
  root: string;
  platform: PlatformFiles;
  /** Remote base, https://github.com in production; file:///… in tests. */
  gitBaseUrl: string;
  credential: JobCredential;
  log: Logger;
  /** git.result already recorded by an earlier process (done.jsonl), for prepare waits. */
  lookup?: (jobId: string) => GitResult | undefined;
}

class JobError extends Error {
  constructor(readonly code: ErrorCode, message: string, readonly log = "") {
    super(message);
  }
}

const LOG_TAIL = 4096;

/** Local git work for the console (protocol.md §7, architecture.md §8.1). Every job is idempotent. */
export class GitJobs {
  private readonly results = new Map<string, GitResult>();
  private readonly waiters = new Map<string, Deferred<GitResult>>();

  constructor(private readonly o: GitJobsOptions) {}

  prepare(b: GitPrepare): Promise<GitResult> {
    return this.job(b.job_id, () => (b.mode === "clone" ? this.prepareClone(b) : this.prepareWorktree(b)));
  }

  status(b: GitStatus): Promise<GitResult> {
    return this.job(b.job_id, async () => ({ status: await this.checkoutStatus(this.repoDir(b.cwd)) }));
  }

  push(b: GitPush): Promise<GitResult> {
    return this.job(b.job_id, () => this.doPush(b));
  }

  cleanup(b: GitCleanup): Promise<GitResult> {
    return this.job(b.job_id, () => this.doCleanup(b));
  }

  purge(b: GitPurge, busy: (abs: string) => boolean): Promise<GitResult> {
    return this.job(b.job_id, async () => {
      if (!/^projects\/[^/]+$/.test(b.path)) throw new PathOutsideRoot(b.path);
      const abs = resolveInRoot(this.o.root, b.path);
      if (busy(abs)) throw new JobError("dirty_conflict", "a session is still active in this project");
      rmSync(abs, { recursive: true, force: true });
      return {};
    });
  }

  /** Waits for a git.prepare job (session.open `prepare`), up to timeoutMs. */
  async waitFor(jobId: string, timeoutMs: number): Promise<GitResult> {
    const known = this.results.get(jobId) ?? this.o.lookup?.(jobId);
    if (known) return known;
    let w = this.waiters.get(jobId);
    if (!w) this.waiters.set(jobId, (w = deferred<GitResult>()));
    return withTimeout(w.promise, timeoutMs, () => new Error(`git job ${jobId} did not finish in time`));
  }

  private async job(jobId: string, fn: () => Promise<Partial<GitResult>>): Promise<GitResult> {
    const result = await this.capture(jobId, fn);
    this.results.set(jobId, result);
    this.waiters.get(jobId)?.resolve(result);
    this.waiters.delete(jobId);
    return result;
  }

  private async capture(jobId: string, fn: () => Promise<Partial<GitResult>>): Promise<GitResult> {
    try {
      return { job_id: jobId, ok: true, ...(await fn()) };
    } catch (err) {
      return failure(jobId, err);
    }
  }

  private async prepareWorktree(b: GitPrepare): Promise<Partial<GitResult>> {
    const shared = resolveInRoot(this.o.root, b.shared ?? `projects/${b.project_id}/repo`);
    const cwd = resolveInRoot(this.o.root, b.cwd);
    const lock = join(dirname(shared), `.${basename(shared)}.agent-teams.lock`);
    try {
      return await withLock(lock, () => this.worktreeLocked(b, shared, cwd));
    } catch (err) {
      if (err instanceof LockTimeout) throw new JobError("locked_timeout", err.message);
      throw err;
    }
  }

  private async worktreeLocked(b: GitPrepare, shared: string, cwd: string): Promise<Partial<GitResult>> {
    const cloned = await this.ensureClone(b.job_id, b.repo, shared);
    if (!cloned || b.fetch !== false) await this.fetch(b.job_id, b.repo, shared);
    await this.git(shared, ["config", "core.hooksPath", this.o.platform.hooksDir]);
    if (isGitDir(cwd)) return { created: false, head_sha: await this.head(cwd) };
    if (existsSync(cwd) && readdirSync(cwd).length > 0) throw new JobError("dirty_conflict", `${b.cwd} exists and is not a worktree`);
    const start = await this.startPoint(shared, b.branch, b.base);
    const add = await this.git(shared, ["worktree", "add", "-B", b.branch, cwd, start]);
    if (add.code !== 0) throw new JobError("dirty_conflict", "git worktree add failed", add.stderr);
    return { created: true, head_sha: await this.head(cwd), log_tail: tail(add.stdout + add.stderr) };
  }

  private async prepareClone(b: GitPrepare): Promise<Partial<GitResult>> {
    const cwd = resolveInRoot(this.o.root, b.cwd);
    const cloned = await this.ensureClone(b.job_id, b.repo, cwd);
    if (!cloned && b.fetch !== false) await this.fetch(b.job_id, b.repo, cwd);
    await this.git(cwd, ["config", "core.hooksPath", this.o.platform.hooksDir]);
    if (cloned) await this.checkoutBranch(cwd, b.branch, b.base);
    return { created: cloned, head_sha: await this.head(cwd) };
  }

  private async checkoutBranch(cwd: string, branch: string, base?: string): Promise<void> {
    const start = await this.startPoint(cwd, branch, base);
    const r = await this.git(cwd, ["checkout", "-B", branch, start]);
    if (r.code !== 0) throw new JobError("dirty_conflict", `cannot check out ${branch}`, r.stderr);
  }

  /** origin/<branch> when the branch already exists remotely, else origin/<base>. */
  private async startPoint(repo: string, branch: string, base?: string): Promise<string> {
    const exists = await this.git(repo, ["rev-parse", "--verify", "--quiet", `refs/remotes/origin/${branch}`]);
    if (exists.code === 0 || !base) return `origin/${branch}`;
    return `origin/${base}`;
  }

  private async ensureClone(jobId: string, repo: string, dir: string): Promise<boolean> {
    if (isGitDir(dir)) return false;
    if (existsSync(dir) && readdirSync(dir).length > 0) throw new JobError("not_a_repo", `${dir} exists and is not a git repository`);
    const env = await this.credEnv(jobId, repo, "read");
    const r = await run("git", ["clone", "--", this.url(repo), dir], { env, timeoutMs: 30 * 60_000 });
    if (r.code !== 0) throw new JobError("clone_failed", "git clone failed", r.stderr);
    return true;
  }

  private async fetch(jobId: string, repo: string, dir: string): Promise<void> {
    const env = await this.credEnv(jobId, repo, "read");
    const r = await run("git", ["-C", dir, "fetch", "origin", "--prune"], { env, timeoutMs: 30 * 60_000 });
    if (r.code !== 0) throw new JobError("fetch_failed", "git fetch failed", r.stderr);
  }

  private async doPush(b: GitPush): Promise<Partial<GitResult>> {
    const cwd = this.repoDir(b.cwd);
    const target = `refs/heads/thread/${b.thread_no}/${b.employee}`;
    if (b.refspec !== `HEAD:${target}` || b.employee.includes("..") || b.employee.includes("/")) throw new JobError("refspec_not_allowed", `refspec must be HEAD:${target}`);
    const env = await this.credEnv(b.job_id, b.repo, "push");
    const lease = b.force_with_lease ? [`--force-with-lease=${target}:${b.force_with_lease}`] : [];
    const r = await run("git", ["-C", cwd, "push", "--no-verify", "--porcelain", ...lease, "origin", b.refspec], { env, timeoutMs: 15 * 60_000 });
    if (r.code !== 0) throw new JobError(pushErrorCode(r), "git push failed", r.stderr + r.stdout);
    return { head_sha: await this.head(cwd), log_tail: tail(r.stdout + r.stderr) };
  }

  private async doCleanup(b: GitCleanup): Promise<Partial<GitResult>> {
    const shared = resolveInRoot(this.o.root, b.shared);
    const removed: string[] = [];
    const kept: GitKeptPath[] = [];
    for (const path of b.paths) {
      const reason = await this.removeWorktree(shared, path, b);
      if (reason) kept.push({ path, reason });
      else removed.push(path);
    }
    if (isGitDir(shared)) await this.git(shared, ["worktree", "prune"]);
    if (b.remove_shared_if_empty && (await this.worktreeCount(shared)) <= 1) rmSync(shared, { recursive: true, force: true });
    return { removed, kept };
  }

  /** Returns a reason when the worktree is kept. */
  private async removeWorktree(shared: string, path: string, b: GitCleanup): Promise<string | null> {
    const abs = resolveInRoot(this.o.root, path);
    if (!existsSync(abs)) return null;
    const st = await this.checkoutStatus(abs);
    if (b.keep_unpushed && st.unpushed_commits > 0) return `${st.unpushed_commits} unpushed commit(s)`;
    if (b.keep_unpushed && st.dirty_files > 0) return `${st.dirty_files} uncommitted change(s)`;
    const r = await this.git(shared, ["worktree", "remove", "--force", abs]);
    if (r.code !== 0) rmSync(abs, { recursive: true, force: true });
    if (b.delete_local_branches && st.branch.startsWith("thread/")) await this.git(shared, ["branch", "-D", st.branch]);
    return null;
  }

  private async worktreeCount(shared: string): Promise<number> {
    if (!isGitDir(shared)) return 0;
    const r = await this.git(shared, ["worktree", "list", "--porcelain"]);
    return r.stdout.split("\n").filter((l) => l.startsWith("worktree ")).length;
  }

  private async checkoutStatus(cwd: string): Promise<NonNullable<GitResult["status"]>> {
    const branch = (await this.git(cwd, ["rev-parse", "--abbrev-ref", "HEAD"])).stdout.trim();
    const head_sha = await this.head(cwd);
    const dirty = (await this.git(cwd, ["status", "--porcelain"])).stdout.split("\n").filter(Boolean).length;
    const unpushed = Number((await this.git(cwd, ["rev-list", "--count", "HEAD", "--not", "--remotes"])).stdout.trim() || 0);
    const counts = await this.git(cwd, ["rev-list", "--left-right", "--count", `refs/remotes/origin/${branch}...HEAD`]);
    const [behind, ahead] = counts.code === 0 ? counts.stdout.trim().split(/\s+/).map(Number) : [0, unpushed];
    return { branch, head_sha, ahead: ahead ?? 0, behind: behind ?? 0, dirty_files: dirty, unpushed_commits: unpushed };
  }

  private repoDir(rel: string): string {
    const abs = resolveInRoot(this.o.root, rel);
    if (!isGitDir(abs)) throw new JobError("not_a_repo", `${rel} is not a git checkout`);
    return abs;
  }

  private async head(cwd: string): Promise<string> {
    return (await this.git(cwd, ["rev-parse", "HEAD"])).stdout.trim();
  }

  private git(cwd: string, args: string[]): Promise<RunResult> {
    return run("git", ["-C", cwd, ...args], { env: gitEnv(), timeoutMs: 10 * 60_000 });
  }

  private url(repo: string): string {
    return `${this.o.gitBaseUrl.replace(/\/$/, "")}/${repo}.git`;
  }

  /** The job token goes only into this one git child (GIT_ASKPASS), never to disk. */
  private async credEnv(jobId: string, repo: string, purpose: "read" | "push"): Promise<NodeJS.ProcessEnv> {
    if (!this.url(repo).startsWith("https://")) return gitEnv();
    const cred = await this.o.credential(jobId, repo, purpose).catch(() => null);
    if (!cred) return gitEnv();
    return gitEnv({
      GIT_ASKPASS: this.o.platform.askpass, AT_GIT_USERNAME: cred.username, AT_GIT_PASSWORD: cred.password,
      GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: "credential.helper", GIT_CONFIG_VALUE_0: "",
    });
  }
}

function isGitDir(dir: string): boolean {
  return existsSync(join(dir, ".git"));
}

function pushErrorCode(r: RunResult): ErrorCode {
  const text = r.stderr + r.stdout;
  if (/workflow/i.test(text) && /permission|scope/i.test(text)) return "workflow_permission";
  if (/non-fast-forward|fetch first|\[rejected\]|stale info/i.test(text)) return "rejected_non_ff";
  if (/authentication failed|403|could not read username|permission denied/i.test(text)) return "auth_failed";
  return "rejected_non_ff";
}

function failure(jobId: string, err: unknown): GitResult {
  const e = err as Error & { code?: string; log?: string };
  const code = (err instanceof JobError ? err.code : e.code === "path_outside_root" ? "path_outside_root" : undefined) as ErrorCode | undefined;
  return { job_id: jobId, ok: false, ...(code ? { error_code: code } : {}), error: e.message, ...(e.log ? { log_tail: tail(e.log) } : {}) };
}

function tail(text: string): string {
  return redact(text.length > LOG_TAIL ? text.slice(-LOG_TAIL) : text);
}
