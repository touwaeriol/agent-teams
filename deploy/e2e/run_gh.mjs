#!/usr/bin/env node
// Real-Claude run for gh and git push in employee sessions (architecture.md §8.1, §10.4; deploy/e2e/up.sh first):
//   1. the daemon downloads the pinned GitHub CLI (the node has none) and puts the platform gh
//      wrapper first on the dev's PATH
//   2. the dev's `GH_REPO=local/hello gh auth status` runs the real gh with the project token in GH_TOKEN
//      (localgh cannot speak gh's API, so the token is checked by gh against github.com and refused
//      there; what matters is that gh reports a GH_TOKEN)
//   3. the dev pushes a new branch with plain `git push` (the bare repo has it), and a push to
//      main is refused by the platform pre-push hook
//   4. the token is in no file on the node (daemon dirs and the worktree)
//   node deploy/e2e/run_gh.mjs <scratch-dir> [--model haiku]
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-gh");
const statePath = join(dir, "gh-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));
const TOKEN = "ghp_e2eToken";

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one sentence. For any request, call open_thread with the user's message copied verbatim as the brief; never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: "Keep it minimal: call update_plan with one main task, then assign_task exactly one task to role dev whose instruction is the brief copied verbatim. When the dev reports the task done, report in one sentence." },
  { tier: "member", role: "dev", title: "开发", count: 1, permission_mode: "bypass",
    instructions: "Follow the numbered steps exactly, one Bash call per step, from the repository directory. No other files, no tests, no exploration. Commands that fail are expected; do not retry or work around them." },
];

const brief = () => [
  "Do these steps in order, each as one Bash command run inside the repository directory (local__hello or the repository root):",
  "1. command -v gh > gh-path.txt; gh --version > gh-version.txt 2>&1; echo \"$PATH\" > path.txt",
  "2. GH_REPO=local/hello gh auth status > gh-auth.txt 2>&1; true",
  "3. echo pushed-by-dev > pushed.txt && git add pushed.txt && git commit -m 'push test'",
  "4. git push origin HEAD:refs/heads/feature/gh-e2e > push-own.txt 2>&1; echo \"exit $?\" >> push-own.txt",
  "5. git push origin HEAD:main > push-main.txt 2>&1; echo \"exit $?\" >> push-main.txt",
  "6. Call complete_task with a one-line summary.",
].join("\n");

async function setup(c) {
  if (state.projectId) return;
  const roles = await c.put("/api/roles", { roles: ROLES.map((r, i) => ({ ...roleBody(r, i, model), permission_mode: r.permission_mode ?? "bypass" })) });
  check(roles.warnings.length === 0, `roles saved (model ${model})`, roles.warnings);
  const node = await c.post("/api/nodes", { name: `rc-e2e-gh-${Date.now() % 100000}` });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  save();
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: TOKEN });
  const project = await c.post("/api/projects", { name: `gh e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  state.projectId = project.id;
  save();
}

function worktreeFile(t, name) {
  const found = execFileSync("find", [join(nodeDir, "root", "projects", state.projectId, "threads", String(t.no)), "-name", name, "-not", "-path", "*/.git/*"])
    .toString().trim().split("\n")[0];
  return found ? readFileSync(found, "utf8") : "";
}

function filesContaining(root, needle) {
  const hits = [];
  for (const name of readdirSync(root, { recursive: true })) {
    const p = join(root, name);
    try {
      if (statSync(p).isFile() && statSync(p).size < 64 << 20 && readFileSync(p).includes(needle)) hits.push(p);
    } catch {
      // sockets, vanished files
    }
  }
  return hits;
}

async function main() {
  const c = await login(base);
  await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: state.nodeToken, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, state.nodeId), { timeoutMs: 60_000 });
    const ghBin = join(nodeDir, "data", "agent-teams", "tools", "gh-2.103.3", "bin", "gh");
    await waitFor("daemon downloads gh", async () => existsSync(ghBin), { timeoutMs: 180_000 });
    check(true, `gh downloaded: ${execFileSync(ghBin, ["--version"]).toString().split("\n")[0]}`);
    const coord = await c.get(`/api/projects/${state.projectId}/coordinator`);
    await c.post(`/api/conversations/${coord.conversation_id}/messages`, { content: brief() });
    const t = await waitFor("coordinator opens a thread", async () => (await c.get(`/api/projects/${state.projectId}/threads`))?.[0],
      { timeoutMs: 300_000, everyMs: 4000 });
    check(true, `coordinator opened thread #${t.no}: ${t.title}`);
    const done = await waitFor("the dev completes its task", async () =>
      ((await c.get(`/api/threads/${t.id}/tasks`)) ?? []).find((x) => x.claimed_by?.startsWith("dev") && ["review", "merging", "done"].includes(x.status)),
    { timeoutMs: 900_000, everyMs: 3000 });
    check(true, `the dev completed ${done.id} (${done.status})`);

    const platformBin = join(nodeDir, "data", "agent-teams", "platform", "bin");
    check(worktreeFile(t, "gh-path.txt").trim() === join(platformBin, "gh"), `gh resolves to the platform wrapper: ${worktreeFile(t, "gh-path.txt").trim()}`);
    check(worktreeFile(t, "path.txt").startsWith(platformBin + ":"), "the platform bin dir is first on the dev's PATH");
    check(worktreeFile(t, "gh-version.txt").includes("gh version 2.103.3"), `gh --version in the session: ${worktreeFile(t, "gh-version.txt").split("\n")[0]}`);
    const auth = worktreeFile(t, "gh-auth.txt");
    check(auth.includes("GH_TOKEN") && !auth.includes(TOKEN), `gh auth status saw a GH_TOKEN (masked): ${auth.replace(/\s+/g, " ").slice(0, 200)}`);
    const own = worktreeFile(t, "push-own.txt");
    const branch = "feature/gh-e2e"; // a branch only the dev's own push creates (the platform pushes thread/<no>/dev-1)
    const remote = execFileSync("git", ["--git-dir", join(env.E2E_GIT_ROOT, "local/hello.git"), "show", `${branch}:pushed.txt`]).toString();
    check(own.includes("exit 0") && remote.includes("pushed-by-dev"), `the dev's own git push created ${branch} on the remote`);
    const main = worktreeFile(t, "push-main.txt");
    check(main.includes("protected branch") && !main.includes("exit 0"), `git push to main refused: ${main.split("\n").find((l) => l.includes("agent-teams")) ?? main}`);
    const mainHas = (() => {
      try {
        execFileSync("git", ["--git-dir", join(env.E2E_GIT_ROOT, "local/hello.git"), "cat-file", "-e", "main:pushed.txt"], { stdio: "ignore" });
        return true;
      } catch {
        return false;
      }
    })();
    check(!mainHas, "main on the remote is untouched");
    const leaks = ["config", "data", "state", "run", "root"].flatMap((d) => filesContaining(join(nodeDir, d), TOKEN));
    check(leaks.length === 0, "the token is in no file on the node (daemon config, data, state, run dirs and worktrees)", leaks);
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
