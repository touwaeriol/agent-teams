// Pieces shared by the real-Claude runs (run.mjs, run_multi.mjs): login, the daemon installed
// from the tarball the control plane serves (or E2E_DAEMON), approval answering and
// read-only SQL against the compose PostgreSQL.
import { spawn, execFileSync } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { Client, check, envFile, repoRoot } from "./lib.mjs";

export const E2E_USER = { email: "e2e@example.com", password: "e2e-password-123456" };

export async function login(base) {
  const c = new Client(base);
  const r = await c.raw("POST", "/auth/register", E2E_USER);
  if (r.status >= 400) await c.post("/auth/login", E2E_USER);
  check(Boolean(c.cookies.agent_teams_session), "registered / logged in via the API");
  await c.get("/api/me");
  return c;
}

/**
 * roleBody fills a role template with the defaults the e2e runs use. Members ask for every
 * write (permission_mode "ask") so the runs see real approval cards; with the default "bypass"
 * nothing would ask.
 */
export function roleBody(r, i, model) {
  return { id: "", runtime: "claude", node_ids: [], description: r.title, model, effort: "", permission_mode: "ask",
    can_delegate: false, allowed_tools: [], disallowed_tools: [], sandbox: "", max_turns: null, gate: "", task_kinds: [], skills: [], plugins: [],
    look: { color: ["#7A4FD1", "#C8622A", "#4C7BD0", "#2E9E6B"][i % 4], outfit: ["hoodie", "suit", "tee", "hoodie"][i % 4], accessory: "none", hair: null },
    sort: i, ...r };
}

/** The runs answer approval cards themselves, so they go straight to the user (not the lead first). */
export const ASK_ME = { approval_routing: "user" };

export function installDaemon(base, nodeDir) {
  const pkg = join(nodeDir, "pkg");
  if (existsSync(join(pkg, "node_modules"))) return pkg;
  mkdirSync(pkg, { recursive: true });
  // The release the console serves (<version>+<build>) is the one its install script names.
  const script = execFileSync("curl", ["-fsS", `${base}/install-node.sh`]).toString();
  const release = /^release_version="([^"]+)"/m.exec(script)?.[1];
  check(Boolean(release), `the console serves a daemon release (${release})`);
  const tgz = join(nodeDir, `agent-teams-node-${release}.tgz`);
  execFileSync("curl", ["-fsS", "-o", tgz, `${base}/node/dist/agent-teams-node-${encodeURIComponent(release)}.tgz`]);
  const want = execFileSync("curl", ["-fsS", `${base}/node/dist/agent-teams-node-${encodeURIComponent(release)}.tgz.sha256`]).toString().trim();
  const got = execFileSync("sha256sum", [tgz]).toString().split(" ")[0];
  check(want === got, "daemon tarball from /node/dist/ matches its sha256");
  execFileSync("tar", ["-xzf", tgz, "-C", pkg, "--strip-components=1"]);
  execFileSync("npm", ["ci", "--omit=dev", "--no-audit", "--no-fund"], { cwd: pkg, stdio: "inherit" });
  return pkg;
}

function daemonEnv(nodeDir) {
  const clean = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith("AGENT_TEAMS_")));
  return { ...clean, XDG_CONFIG_HOME: join(nodeDir, "config"), XDG_DATA_HOME: join(nodeDir, "data"),
    XDG_STATE_HOME: join(nodeDir, "state"), XDG_RUNTIME_DIR: join(nodeDir, "run"), AGENT_TEAMS_ENV_FILE: join(nodeDir, "no-node.env") };
}

/** startDaemon runs the daemon under nodeDir (never ~/.config/agent-teams/node.env); pkg null = E2E_DAEMON. */
export function startDaemon({ base, nodeDir, token, gitRoot, pkg }) {
  for (const d of ["config", "data", "state", "run", "root"]) mkdirSync(join(nodeDir, d), { recursive: true });
  const entry = pkg === null ? process.env.E2E_DAEMON : existsSync(join(pkg, "dist/node.mjs")) ? join(pkg, "dist/node.mjs") : join(pkg, "node.mjs");
  const out = createWriteStream(join(nodeDir, "daemon.log"), { flags: "a" });
  const child = spawn(process.execPath, [entry, "run", "--console", base, "--token", token, "--root", join(nodeDir, "root"),
    "--state", join(nodeDir, "state"), "--max-sessions", "6", "--git-base-url", `file://${gitRoot}`, "--log-level", "debug"],
  { env: daemonEnv(nodeDir), stdio: ["ignore", "pipe", "pipe"] });
  child.stdout.pipe(out);
  child.stderr.pipe(out);
  child.on("exit", (code) => console.log(`daemon exited ${code}`));
  return child;
}

export async function nodeOnline(c, nodeId) {
  const nodes = await c.get("/api/nodes");
  const n = (nodes.nodes ?? nodes).find((x) => x.id === nodeId);
  return n?.online && n;
}

/** answerApprovals allows every approval card and says yes to every question of the project. */
export async function answerApprovals(c, projectId, answered) {
  const waiting = await c.get(`/api/projects/${projectId}/waiting`);
  for (const q of waiting.questions ?? []) {
    if (answered.has(q.id)) continue;
    console.log(`  card ${q.kind} from ${q.asker}: ${String(q.text).slice(0, 160)}`);
    const answer = q.kind === "approval" ? "allow" : "Yes, go ahead.";
    await c.post(`/api/conversations/${q.conversation_id}/questions/${q.id}/answer`, { answer });
    answered.set(q.id, q);
  }
}

/** record appends every SSE event to a JSONL log. */
export function record(sse, log) {
  let seen = 0;
  return setInterval(() => {
    for (; seen < sse.events.length; seen++) log.write(JSON.stringify(sse.events[seen]) + "\n");
  }, 500);
}

/** gitShow reads a file at a ref of a bare repository under the e2e git root (null when missing). */
export function gitShow(gitRoot, slug, ref, path) {
  try {
    return execFileSync("git", ["--git-dir", join(gitRoot, `${slug}.git`), "show", `${ref}:${path}`], { stdio: ["ignore", "pipe", "ignore"] }).toString();
  } catch {
    return null;
  }
}

export function gitIsAncestor(gitRoot, slug, a, b) {
  try {
    execFileSync("git", ["--git-dir", join(gitRoot, `${slug}.git`), "merge-base", "--is-ancestor", a, b], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

/** sql runs a read-only query in the compose PostgreSQL (facts the REST API does not expose). */
export function sql(query) {
  return execFileSync(join(repoRoot, "deploy/compose.sh"), ["exec", "-T", "postgres", "psql", "-U", "agent_teams", "-d", "agent_teams", "-At", "-c", query],
    { env: { ...process.env, ENV_FILE: envFile } }).toString().trim();
}
