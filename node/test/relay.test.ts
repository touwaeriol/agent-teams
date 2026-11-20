import { execFileSync, spawn, spawnSync } from "node:child_process";
import { createServer, type Server } from "node:http";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { startHarness, type Harness } from "./helpers/daemon.js";
import { gitCredentialEnv } from "../src/cred/helper.js";

const hasGit = spawnSync("git", ["--version"]).error === undefined;
const TOKEN = "atgr1.relay-token";

const git = (cwd: string, ...args: string[]) =>
  execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8", env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" } }).trim();

/** A stand-in for the console's Git relay: git http-backend over root, Basic auth with TOKEN. */
function relay(root: string): Promise<{ server: Server; base: string }> {
  const server = createServer((req, res) => {
    const auth = req.headers.authorization ?? "";
    const pass = Buffer.from(auth.replace(/^Basic /, ""), "base64").toString().split(":").slice(1).join(":");
    if (pass !== TOKEN) {
      res.writeHead(401, { "WWW-Authenticate": 'Basic realm="t"' }).end();
      return;
    }
    const url = new URL(req.url ?? "/", "http://x");
    const cgi = spawn("git", ["http-backend"], {
      env: { ...process.env, GIT_PROJECT_ROOT: root, GIT_HTTP_EXPORT_ALL: "1", REMOTE_USER: "t", PATH_INFO: url.pathname.replace(/^\/git/, ""),
        QUERY_STRING: url.search.slice(1), REQUEST_METHOD: req.method ?? "GET", CONTENT_TYPE: req.headers["content-type"] ?? "",
        ...(req.headers["content-encoding"] ? { HTTP_CONTENT_ENCODING: String(req.headers["content-encoding"]) } : {}) },
    });
    req.pipe(cgi.stdin);
    let head: Buffer | null = Buffer.alloc(0);
    cgi.stdout.on("data", (d: Buffer) => {
      if (head === null) return void res.write(d);
      head = Buffer.concat([head, d]);
      const end = head.indexOf("\r\n\r\n");
      if (end < 0) return;
      const lines = head.subarray(0, end).toString().split("\r\n");
      let status = 200;
      for (const l of lines) {
        const [k, ...v] = l.split(":");
        if (k!.toLowerCase() === "status") status = Number(v.join(":").trim().split(" ")[0]);
        else res.setHeader(k!, v.join(":").trim());
      }
      res.writeHead(status);
      res.write(head.subarray(end + 4));
      head = null;
    });
    cgi.on("close", () => res.end());
  });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => {
    const a = server.address() as { port: number };
    resolve({ server, base: `http://127.0.0.1:${a.port}` });
  }));
}

describe.skipIf(!hasGit)("standalone repositories (relay)", () => {
  let h: Harness;
  let srv: Server | undefined;
  afterEach(async () => {
    srv?.close();
    await h?.close();
  });

  it("clones and pushes git/<id> through remote_url with a relay token asked for the relay's host", async () => {
    h = await startHarness();
    const root = join(h.dir, "relay");
    const bare = join(root, "gr_abc.git");
    mkdirSync(root, { recursive: true });
    execFileSync("git", ["init", "--bare", "-b", "main", bare]);
    git(bare, "config", "http.receivepack", "true");
    const seed = join(h.dir, "seed");
    execFileSync("git", ["clone", bare, seed], { stdio: "ignore" });
    writeFileSync(join(seed, "README.md"), "hi\n");
    git(seed, "add", ".");
    git(seed, "commit", "-m", "init");
    git(seed, "push", "origin", "HEAD:main");
    const r = await relay(root);
    srv = r.server;
    const remote = `${r.base}/git/gr_abc.git`;
    const answered = new Set<string>();
    const timer = setInterval(() => {
      for (const f of h.console.ofType("cred.request")) {
        if (answered.has(f.body.req_id)) continue;
        answered.add(f.body.req_id);
        h.console.unreliable("cred.response", { req_id: f.body.req_id, ok: true, username: "x-relay-token", password: TOKEN, expires_at: new Date(Date.now() + 3600_000).toISOString() });
      }
    }, 10);
    try {
      h.console.command("git.prepare", { job_id: "g_1", project_id: "p1", repo: "git/gr_abc", remote_url: remote, mode: "clone",
        cwd: "projects/p1/threads/1/dev", branch: "thread/1/dev", base: "main", fetch: true });
      const res = await h.console.waitType("git.result", (b) => b.job_id === "g_1");
      expect(res.body).toMatchObject({ ok: true, created: true });
      expect(h.console.ofType("cred.request")[0]!.body).toMatchObject({ job_id: "g_1", host: r.base.replace("http://", ""), path: "git/gr_abc.git", purpose: "read" });
      const wt = join(h.opts.root, "projects/p1/threads/1/dev");
      expect(git(wt, "remote", "get-url", "origin")).toBe(remote);
      writeFileSync(join(wt, "b.txt"), "b\n");
      git(wt, "add", ".");
      git(wt, "commit", "-m", "work");
      h.console.command("git.push", { job_id: "g_2", project_id: "p1", repo: "git/gr_abc", remote_url: remote, cwd: "projects/p1/threads/1/dev",
        refspec: "HEAD:refs/heads/thread/1/dev", employee: "dev", thread_no: 1 });
      expect((await h.console.waitType("git.result", (b) => b.job_id === "g_2")).body).toMatchObject({ ok: true });
      expect(git(bare, "rev-parse", "refs/heads/thread/1/dev")).toBe(git(wt, "rev-parse", "HEAD"));
    } finally {
      clearInterval(timer);
    }
  });

  it("points git's credential helper at the relay prefix too", () => {
    const env = gitCredentialEnv(["node", "cli.mjs"], "/s", "s_1", "x", "https://console.example/git/");
    expect(env.GIT_CONFIG_COUNT).toBe("6");
    expect(env.GIT_CONFIG_KEY_4).toBe("credential.https://console.example/git/.helper");
    expect(env.GIT_CONFIG_KEY_5).toBe("credential.https://console.example/git/.useHttpPath");
  });
});
