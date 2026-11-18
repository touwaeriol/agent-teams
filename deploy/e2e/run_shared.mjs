#!/usr/bin/env node
// Real-Claude run of shared project members (docs/v2/shared-members.md; deploy/e2e/up.sh first):
// - one project on local/hello whose staff is dev × 2 + reviewer × 1 (the role counts are the
//   project's headcount, not per thread);
// - two threads opened at once, each with three tasks: two dev tasks (one file each) and a
//   reviewer task that depends on both;
// - the members take tasks from the project queue for both threads; the headcount stays 3
//   (no member is created per thread, at most 3 member sessions are live at once, the office
//   shows 3 members);
// - both threads merge their dev branches, open a PR, are accepted and end done; afterwards the
//   members are still there and no member session is left open.
//   node deploy/e2e/run_shared.mjs <scratch-dir> [--model haiku]
import { createWriteStream, existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { answerApprovals, ASK_ME, gitShow, installDaemon, login, nodeOnline, record, roleBody, sql, startDaemon } from "./harness.mjs";
import { SSE, check, readEnv, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const gitRoot = env.E2E_GIT_ROOT;
const nodeDir = join(dir, "node-shared");
const statePath = join(dir, "shared-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));
const log = createWriteStream(join(dir, "shared-events.jsonl"), { flags: "a" });

const REPO = "local/hello";
const HEADCOUNT = 3;
const THREADS = ["alpha", "beta"];

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "This is a test. Never open threads and never call remember. Reply in one short line." },
  { tier: "lead", role: "lead", title: "组长", count: 1,
    instructions: "This is a cheap test; follow these steps exactly. 1) update_plan with one step \"做\" (status todo). " +
      "2) The brief lists three tasks: call assign_task for them in order — the first two with assignee \"dev\", the third with " +
      "assignee \"reviewer\" and depends_on set to the ids of the first two — each with main \"做\" and the task text copied word for word. " +
      "3) When a dev task is reported done, call enqueue_merge for it. 4) When all three tasks are done, call open_pr with a one-line " +
      "title, then finish with a one-line summary. Members are shared with other threads and may be busy there: just wait. Never assign anything else." },
  { tier: "member", role: "dev", title: "开发", count: 2, permission_mode: "bypass",
    instructions: "This is a cheap test. Do exactly the task with the Write tool in the repository directory, git add and git commit it, " +
      "then complete_task with a one-line summary. No other files, no tests, no exploration." },
  { tier: "member", role: "reviewer", title: "审查", count: 1, read_only: true,
    instructions: "This is a cheap test. Read only the files the task names (in the repository directory) and call complete_task with " +
      "their contents as the summary. Never edit anything." },
];

const brief = (name) => [
  `Task 1 (dev): create ${name}-1.txt at the repository root containing exactly: ${name} one`,
  `Task 2 (dev): create ${name}-2.txt at the repository root containing exactly: ${name} two`,
  `Task 3 (reviewer, after tasks 1 and 2): read ${name}-1.txt and ${name}-2.txt and report their contents.`,
].join("\n");

async function setup(c) {
  if (state.projectId) return;
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody(r, i, model)) });
  const node = await c.post("/api/nodes", { name: `rc-e2e-shared-${Date.now() % 100000}` });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  const conn = await c.post("/api/git/connections", { name: `local-shared-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `shared e2e ${Date.now() % 100000}`, delivery: { ...ASK_ME, ci: { mode: "off" } },
    merge_policy: "manual", repos: [{ repo: REPO, git_connection_id: conn.id }] });
  state.projectId = project.id;
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  save();
}

/** staff checks the headcount: 3 active project members, none created per thread. */
function staff() {
  const p = state.projectId;
  const shared = Number(sql(`SELECT count(*) FROM at_employees WHERE project_id = '${p}' AND tier = 'member' AND thread_id IS NULL AND active`));
  const perThread = Number(sql(`SELECT count(*) FROM at_employees WHERE project_id = '${p}' AND tier = 'member' AND thread_id IS NOT NULL`));
  const live = Number(sql(`SELECT count(*) FROM at_agent_sessions a JOIN at_employees e ON e.id = a.employee_id
    WHERE e.project_id = '${p}' AND e.tier = 'member' AND a.state <> 'closed'`));
  return { shared, perThread, live };
}

async function progress(c, answered, watch) {
  await answerApprovals(c, state.projectId, answered);
  const s = staff();
  watch.maxLive = Math.max(watch.maxLive, s.live);
  watch.bad ||= s.shared !== HEADCOUNT || s.perThread !== 0 ? s : null;
  const office = await c.get(`/api/projects/${state.projectId}/office`);
  const members = office.employees.filter((e) => e.tier === "member");
  watch.badOffice ||= members.length !== HEADCOUNT ? members.map((e) => e.name) : null;
  const out = [];
  for (const id of state.threads) {
    const t = await c.get(`/api/threads/${id}`);
    const tasks = (await c.get(`/api/threads/${id}/tasks`)) ?? [];
    for (const x of tasks) if (x.claimed_by) watch.claims.add(`#${t.no}:${x.claimed_by}`);
    out.push({ t, tasks });
  }
  console.log(`  ${out.map(({ t, tasks }) => `#${t.no} ${t.status} [${tasks.map((x) => `${x.id}:${x.status}@${x.claimed_by || "-"}`).join(" ")}]`).join("  ")}` +
    `  · members ${members.map((m) => `${m.name}${m.thread_no ? `@#${m.thread_no}` : ""}`).join(" ")} · live member sessions ${s.live}`);
  return out;
}

async function main() {
  const c = await login(base);
  await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: state.nodeToken, gitRoot, pkg });
  try {
    await drive(c);
  } finally {
    daemon.kill("SIGTERM");
    log.end();
  }
}

async function drive(c) {
  const node = await waitFor("node online", () => nodeOnline(c, state.nodeId), { timeoutMs: 60_000 });
  check(true, `daemon online: ${node.version ?? ""}`);
  const sse = new SSE(c, `/api/projects/${state.projectId}/events`);
  const recorder = record(sse, log);
  if (!state.threads) {
    state.threads = [];
    for (const name of THREADS) state.threads.push((await c.post(`/api/projects/${state.projectId}/threads`, { title: name, brief: brief(name) })).id);
    save();
  }
  const answered = new Map();
  const watch = { maxLive: 0, bad: null, badOffice: null, claims: new Set() };
  const opened = await waitFor("both threads have three tasks", async () => {
    const p = await progress(c, answered, watch);
    return p.every(({ tasks }) => tasks.length >= 3) && p;
  }, { timeoutMs: 600_000, everyMs: 5000 });
  check(opened.every(({ tasks }) => tasks.some((x) => x.role === "reviewer" && x.depends_on.length === 2)), "each thread has a reviewer task depending on both dev tasks");
  for (const { t } of await acceptAll(c, answered, watch)) await finished(c, t);
  check(!watch.bad, `headcount stayed ${HEADCOUNT} project members, none per thread`, watch.bad);
  check(!watch.badOffice, `the office always showed ${HEADCOUNT} members`, watch.badOffice);
  check(watch.maxLive <= HEADCOUNT, `at most ${HEADCOUNT} member sessions live at once (max seen ${watch.maxLive})`);
  console.log(`  who worked where: ${[...watch.claims].sort().join(", ")}`);
  const after = staff();
  check(after.shared === HEADCOUNT && after.live === 0, `after both threads: ${after.shared} members, ${after.live} member sessions open`);
  await sleep(1500);
  clearInterval(recorder);
  sse.close();
  writeFileSync(join(dir, "shared-summary.json"), JSON.stringify({ state, claims: [...watch.claims], maxLive: watch.maxLive, sseTypes: sse.types() }, null, 2));
}

/** acceptAll waits for each thread to open its PR and accepts it; returns the done threads. */
async function acceptAll(c, answered, watch) {
  const accepted = new Set();
  return waitFor("both threads done", async () => {
    const p = await progress(c, answered, watch);
    for (const { t } of p) {
      if (t.status !== "review" || accepted.has(t.id)) continue;
      await c.post(`/api/threads/${t.id}/accept`, {});
      accepted.add(t.id);
      console.log(`  accepted #${t.no}`);
    }
    return p.every(({ t }) => t.status === "done") && p;
  }, { timeoutMs: 2_400_000, everyMs: 5000 });
}

/** finished checks a done thread: every task done, its files merged into main, its member branches gone. */
async function finished(c, t) {
  const name = t.title;
  const tasks = (await c.get(`/api/threads/${t.id}/tasks`)) ?? [];
  const work = tasks.filter((x) => ["dev", "reviewer"].includes(x.role));
  check(work.length >= 3 && work.every((x) => x.status === "done"), `#${t.no} tasks done: ${work.map((x) => `${x.id}@${x.claimed_by}`).join(", ")}`);
  check(gitShow(gitRoot, REPO, "main", `${name}-1.txt`)?.trim() === `${name} one` && gitShow(gitRoot, REPO, "main", `${name}-2.txt`)?.trim() === `${name} two`,
    `#${t.no} files merged into ${REPO} main`);
  const reviewer = work.find((x) => x.role === "reviewer");
  check(reviewer?.summary?.includes(`${name} one`) && reviewer.summary.includes(`${name} two`), `#${t.no} reviewer saw both files: ${reviewer?.summary?.slice(0, 120)}`);
  const branches = [...new Set(work.filter((x) => x.role === "dev").map((x) => `thread/${t.no}/${x.claimed_by}`))];
  const gone = (b) => gitShow(gitRoot, REPO, b, `${name}-1.txt`) === null && gitShow(gitRoot, REPO, b, `${name}-2.txt`) === null;
  await waitFor(`#${t.no} member branches deleted`, async () => branches.every(gone), { timeoutMs: 120_000, everyMs: 3000 });
  check(true, `#${t.no} member branches deleted after the thread ended (${branches.join(", ")})`);
}

main().then(() => process.exit(0), (e) => {
  console.error(e.stack ?? e);
  process.exit(1); // the SSE reader would keep the process alive
});
