// Security red lines of a session with git credentials and remote servers (讨论串 #38,
// docs/v2/remote-servers-verification.md): real git takes a token through the credential helper,
// and afterwards neither the environment, git's config nor any file under the node's data holds it;
// a process without the session secret gets nothing; after the session ends the old environment
// gets nothing either.
import { spawn, spawnSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

const TOKEN = "ghs_REDLINE_0123456789abcdefghijklmnopqrstuv";

function filesContaining(root: string, needle: string): string[] {
  const hits: string[] = [];
  const walk = (d: string) => {
    if (!existsSync(d)) return;
    for (const n of readdirSync(d)) {
      const p = join(d, n);
      const st = statSync(p, { throwIfNoEntry: false });
      if (st?.isDirectory()) walk(p);
      else if (st?.isFile() && readFileSync(p).includes(needle)) hits.push(p);
    }
  };
  walk(root);
  return hits;
}

const hasGit = spawnSync("git", ["--version"]).error === undefined;

describe.skipIf(!hasGit)("red lines: git credentials", () => {
  let h: Harness;
  afterEach(async () => h?.close());

  it("hands the token to git only through the helper, keeps it out of env, git config and disk, and stops at session end", async () => {
    // The helper runs the real CLI, as git starts it in a session.
    h = await startHarness({ nodeCmd: [join(__dirname, "..", "node_modules", ".bin", "tsx"), join(__dirname, "..", "src", "cli.ts")] });
    const answered = new Set<string>();
    const timer = setInterval(() => {
      for (const f of h.console.ofType("cred.request")) {
        if (answered.has(f.body.req_id)) continue;
        answered.add(f.body.req_id);
        h.console.unreliable("cred.response", { req_id: f.body.req_id, ok: true, username: "x-access-token", password: TOKEN, expires_at: new Date(Date.now() + 3600_000).toISOString() });
      }
    }, 10);
    try {
      h.console.command("session.open", claudeOpen("s_red", 1, { git_credentials: true, ssh_servers: [{ name: "prod" }] }));
      await h.console.waitType("session.opened");
      const env = h.log.options[0]!.env as Record<string, string>;
      // Async: the fake console answering cred.request lives in this process.
      const fill = (e: Record<string, string>) =>
        new Promise<{ stdout: string; stderr: string }>((resolve) => {
          const c = spawn("git", ["credential", "fill"], { env: e, cwd: h.dir });
          let stdout = "";
          let stderr = "";
          const t = setTimeout(() => c.kill("SIGKILL"), 20_000);
          c.stdout.on("data", (d) => (stdout += d));
          c.stderr.on("data", (d) => (stderr += d));
          c.on("close", () => (clearTimeout(t), resolve({ stdout, stderr })));
          c.stdin.end("protocol=https\nhost=github.com\npath=acme/app.git\n\n");
        });

      const got = await fill(env);
      expect(got.stdout).toContain(`password=${TOKEN}`);
      expect(h.console.ofType("cred.request")[0]!.body).toMatchObject({ session_id: "s_red", open_gen: 1, host: "github.com", path: "acme/app.git" });

      // Not in the session environment (the token, nor any GitHub token variable).
      expect(Object.values(env).filter((v) => v.includes(TOKEN) || /gh[sopu]_[A-Za-z0-9]{20,}/.test(v))).toEqual([]);
      for (const k of ["GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"]) expect(env[k]).toBeUndefined();
      const printed = spawnSync("env", [], { env, encoding: "utf8" }).stdout;
      expect(printed).not.toContain(TOKEN);
      // Not in git's configuration as the session sees it (helper command line only).
      const cfg = spawnSync("git", ["config", "--list", "--show-origin"], { env, encoding: "utf8", cwd: h.dir }).stdout;
      expect(cfg).not.toContain(TOKEN);
      expect(cfg).toMatch(/credential\.https:\/\/github\.com\.helper=!.* git-credential --socket \S+ --session s_red --secret \S+/);
      // Not on disk anywhere under the node's directories (data, state, platform, run, ssh, tmp).
      expect(filesContaining(h.dir, TOKEN)).toEqual([]);

      // No secret, the wrong secret, or another session's id: nothing.
      const wrong = await fill({ ...env, GIT_CONFIG_VALUE_1: env.GIT_CONFIG_VALUE_1!.replace(/--secret \S+/, "--secret " + "0".repeat(43)) });
      expect(wrong.stdout).not.toContain(TOKEN);
      const other = await fill({ ...env, GIT_CONFIG_VALUE_1: env.GIT_CONFIG_VALUE_1!.replace("--session s_red", "--session s_other") });
      expect(other.stdout).not.toContain(TOKEN);

      // The session ends: its cache and its secret are gone with it.
      h.console.command("session.close", { session_id: "s_red", open_gen: 1 });
      await h.console.waitType("session.closed");
      const after = await fill(env);
      expect(after.stdout).not.toContain(TOKEN);
      expect(h.console.ofType("cred.request")).toHaveLength(1);
      expect(filesContaining(h.dir, TOKEN)).toEqual([]);
    } finally {
      clearInterval(timer);
    }
  }, 60_000);
});
