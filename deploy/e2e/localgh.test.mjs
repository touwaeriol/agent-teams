// node --test deploy/e2e/localgh.test.mjs
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, test } from "node:test";
import { here } from "./lib.mjs";

const port = 18000 + Math.floor(Math.random() * 1000);
const api = `http://127.0.0.1:${port}/repos/o/r`;
let root, proc;

function git(cwd, ...args) {
  return execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@t", ...args], { cwd }).toString().trim();
}

function commitFile(work, branch, file, text) {
  git(work, "checkout", "-q", "-B", branch, "origin/main");
  writeFileSync(join(work, file), text);
  git(work, "add", file);
  git(work, "commit", "-q", "-m", `${branch}: ${file}`);
  git(work, "push", "-q", "-f", "origin", `${branch}`);
}

async function call(method, path, body, token = "good") {
  const res = await fetch(api + path, { method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body && JSON.stringify(body) });
  const text = await res.text();
  return { status: res.status, json: text ? JSON.parse(text) : null };
}

before(async () => {
  root = mkdtempSync(join(tmpdir(), "localgh-"));
  const bare = join(root, "o", "r.git");
  execFileSync("git", ["init", "-q", "--bare", "-b", "main", bare]);
  const work = join(root, "work");
  execFileSync("git", ["clone", "-q", bare, work]);
  git(work, "checkout", "-q", "-b", "main");
  writeFileSync(join(work, "README.md"), "hi\n");
  git(work, "add", ".");
  git(work, "commit", "-q", "-m", "init");
  git(work, "push", "-q", "origin", "main");
  git(work, "fetch", "-q");
  commitFile(work, "feat-a", "a.txt", "a\n");
  commitFile(work, "feat-b", "README.md", "conflict\n");
  commitFile(work, "feat-c", "README.md", "other\n");
  execFileSync("git", ["clone", "-q", "--bare", bare, join(root, "o", "s.git")]);
  proc = spawn(process.execPath, [join(here, "localgh.mjs"), "--root", root, "--port", String(port)],
    { env: { ...process.env, LOCALGH_TOKENS: "good" }, stdio: "ignore" });
  for (let i = 0; i < 50; i++) {
    if (await fetch(`http://127.0.0.1:${port}/user`).then(() => true, () => false)) return;
    await new Promise((r) => setTimeout(r, 100));
  }
});

after(() => {
  proc?.kill();
  rmSync(root, { recursive: true, force: true });
});

test("rejects unknown tokens", async () => {
  assert.equal((await call("GET", "", undefined, "bad")).status, 401);
  assert.equal((await call("GET", "")).json.full_name, "o/r");
});

test("refs: read, create (422 when present), fast-forward only update, delete", async () => {
  const main = (await call("GET", "/git/ref/heads/main")).json.object.sha;
  assert.equal((await call("POST", "/git/refs", { ref: "refs/heads/mq/1", sha: main })).status, 201);
  assert.equal((await call("POST", "/git/refs", { ref: "refs/heads/mq/1", sha: main })).status, 422);
  const a = (await call("GET", "/git/ref/heads/feat-a")).json.object.sha;
  assert.equal((await call("PATCH", "/git/refs/heads/mq/1", { sha: a, force: false })).status, 200);
  assert.equal((await call("PATCH", "/git/refs/heads/mq/1", { sha: main, force: false })).status, 422);
  assert.equal((await call("DELETE", "/git/refs/heads/mq/1")).status, 204);
  assert.equal((await call("GET", "/git/ref/heads/mq/1")).status, 404);
});

test("merges: merge commit, 204 when already merged, 409 on conflict", async () => {
  await call("POST", "/git/refs", { ref: "refs/heads/trial", sha: (await call("GET", "/git/ref/heads/main")).json.object.sha });
  const m = await call("POST", "/merges", { base: "trial", head: "feat-a", commit_message: "merge a" });
  assert.equal(m.status, 201);
  assert.match(m.json.sha, /^[0-9a-f]{40}$/);
  assert.equal((await call("POST", "/merges", { base: "trial", head: "feat-a" })).status, 204);
  assert.equal((await call("POST", "/merges", { base: "trial", head: "feat-b" })).status, 201);
  assert.equal((await call("POST", "/merges", { base: "trial", head: "feat-c" })).status, 409);
  const file = await call("GET", "/contents/a.txt?ref=trial");
  assert.equal(Buffer.from(file.json.content, "base64").toString(), "a\n");
});

test("pull requests: create, read, merge", async () => {
  const pr = await call("POST", "/pulls", { head: "feat-a", base: "main", title: "A" });
  assert.equal(pr.status, 201);
  const got = (await call("GET", `/pulls/${pr.json.number}`)).json;
  assert.equal(got.merged, false);
  assert.equal(got.number, pr.json.number); // a number, not the string from the URL
  assert.equal((await call("PUT", `/pulls/${pr.json.number}/merge`, { merge_method: "merge" })).status, 200);
  assert.equal((await call("GET", `/pulls/${pr.json.number}`)).json.merged, true);
  assert.equal((await call("GET", "/contents/a.txt?ref=main")).status, 200);
  assert.equal((await call("GET", "/contents/missing?ref=main")).status, 404);
});

test("pull requests are numbered per repository and need an existing base", async () => {
  const other = `http://127.0.0.1:${port}/repos/o/s`;
  const res = await fetch(`${other}/pulls`, { method: "POST", headers: { Authorization: "Bearer good" },
    body: JSON.stringify({ head: "feat-a", base: "main", title: "S" }) });
  assert.equal(res.status, 201);
  assert.equal((await res.json()).number, 1);
  assert.equal((await call("GET", "/pulls/1")).json.title, "A");
  assert.equal((await call("POST", "/pulls", { head: "feat-a", base: "nope", title: "X" })).status, 422);
});
