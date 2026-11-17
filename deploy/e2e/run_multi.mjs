#!/usr/bin/env node
// Real-Claude run of the v2.1 features against the local stack (deploy/e2e/up.sh):
// - a project bound to TWO localgh repositories (local/web, local/api) whose api base branch
//   (develop) is stated only in the project context;
// - one request that touches both repositories: the thread branches from the right base in each
//   repository, the merge queue runs per repository and open_pr opens one PR per repository
//   (web -> main, api -> develop); accepting the thread merges both;
// - a designer (the "设计师" preset, design + docs skills) makes a small HTML page and
//   publishes it with publish_file; the deliverable's file is served sandboxed.
//   node deploy/e2e/run_multi.mjs <scratch-dir> [--model haiku]
import { createWriteStream, existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { answerApprovals, ASK_ME, gitIsAncestor, gitShow, installDaemon, login, nodeOnline, record, roleBody, sql, startDaemon } from "./harness.mjs";
import { SSE, check, readEnv, sleep, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const model = process.argv.includes("--model") ? process.argv[process.argv.indexOf("--model") + 1] : "haiku";
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const gitRoot = env.E2E_GIT_ROOT;
const nodeDir = join(dir, "node-multi");
const statePath = join(dir, "multi-state.json");
const state = existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")) : {};
const save = () => writeFileSync(statePath, JSON.stringify(state, null, 2));
const log = createWriteStream(join(dir, "multi-events.jsonl"), { flags: "a" });

const WEB = "local/web";
const API = "local/api";
const CONTEXT = `两个仓库：${WEB} 是前端，${API} 是后端。\n仓库 ${API} 的基准分支是 develop（不是 main）。`;
const REQUEST = `Two small changes in one thread, please:\n` +
  `1. In ${WEB} add a file web.txt at the repository root containing exactly: web ok\n` +
  `2. In ${API} add a file api.txt at the repository root containing exactly: api ok\n` +
  `Also have the designer make a tiny one-file HTML page with the heading "Hello design" and publish it.`;

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "Keep replies to one sentence. For any change, call open_thread exactly once with a one-line brief, roles [\"dev\", \"designer\"] when a design is asked for, and repos listing every repository involved, each with the base branch the project context states for it (omit base when none is stated). Never do the work yourself." },
  { tier: "lead", role: "lead", title: "组长", count: 1, can_delegate: true,
    instructions: "Keep it minimal. Call update_plan with one main task. Then call assign_task twice: one task to role dev covering every repository change (name each repository and its file), and one task to role designer for the HTML page. When a dev task is reported done, call enqueue_merge for it. When the designer's task is done, call enqueue_merge for it too. Once every task is done, call open_pr once with a one-line title. Then report in one sentence. Do not review at length." },
  { tier: "member", role: "dev", title: "开发", count: 1,
    instructions: "Do exactly the assigned task with the Write tool: each repository is a subdirectory of your working directory (see the 仓库 section). Then call complete_task with a one-line summary. No extra files, no tests, no exploration." },
];

const DESIGNER_EXTRA = "\n\nFor this run keep it tiny: write ONE self-contained file design/index.html in your working directory (not inside a repository directory) with the Write tool, under 40 lines, then call publish_file with path design/index.html, kind design and a short title, then complete_task with the deliverable id. No revisions.";

async function setup(c) {
  if (state.projectId) return;
  const node = await c.post("/api/nodes", { name: "rc-e2e-multi" });
  state.nodeId = node.node.id;
  state.nodeToken = node.token;
  const conn = await c.post("/api/git/connections", { name: "local-multi", token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: "web + api", context: CONTEXT, delivery: ASK_ME, merge_policy: "manual",
    delivery: { ci: { mode: "off" } },
    repos: [{ repo: WEB, git_connection_id: conn.id }, { repo: API, git_connection_id: conn.id }] });
  check(project.repos?.map((r) => r.repo).join(",") === `${WEB},${API}`, `project binds ${WEB} and ${API} in order`, project.repos);
  state.projectId = project.id;
  const presets = await c.get("/api/roles/presets");
  const designer = presets.roles.find((r) => r.role === "designer");
  check(designer?.skills?.includes("design"), `"设计师" preset carries skills ${designer?.skills}`);
  const all = [...ROLES, { ...designer, instructions: designer.instructions + DESIGNER_EXTRA }];
  const roles = await c.put("/api/roles", { roles: all.map((r, i) => roleBody({ ...r, model }, i, model)) });
  check(roles.roles.length === 4 && roles.roles.find((r) => r.role === "designer")?.skills?.length === 2, "roles saved (coordinator, lead, dev, designer with skills)", roles.warnings);
  save();
}

async function threadOf(c) {
  const threads = await c.get(`/api/projects/${state.projectId}/threads`);
  return threads?.[0];
}

async function progress(c, answered) {
  await answerApprovals(c, state.projectId, answered);
  const t = await threadOf(c);
  if (!t) return null;
  const tasks = (await c.get(`/api/threads/${t.id}/tasks`)) ?? [];
  const repos = (t.repos ?? []).map((r) => `${r.repo}@${r.base_branch || "?"}${r.branch_created ? "+" : ""}${r.pr_number ? `#${r.pr_number}:${r.pr_state}` : ""}`).join(" ");
  console.log(`  thread #${t.no} ${t.status}; repos ${repos}; tasks ${tasks.map((x) => `${x.id}:${x.status}@${x.claimed_by ?? "-"}`).join(", ")}`);
  return { t, tasks };
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
  const coord = await c.get(`/api/projects/${state.projectId}/coordinator`);
  if (!state.sent) {
    await c.post(`/api/conversations/${coord.conversation_id}/messages`, { content: REQUEST });
    state.sent = true;
    save();
  }
  const answered = new Map();
  const thread = await waitFor("coordinator opens a thread", async () => (await progress(c, answered))?.t, { timeoutMs: 300_000, everyMs: 5000 });
  check(true, `coordinator opened thread #${thread.no}: ${thread.title}`);
  await checkDesign(c, answered);
  const prs = await waitFor("lead opens a PR per repository", async () => {
    const p = await progress(c, answered);
    const repos = p?.t.repos ?? [];
    return repos.length >= 2 && repos.every((r) => r.pr_number) && p;
  }, { timeoutMs: 1_200_000, everyMs: 5000 });
  checkBranches(prs.t);
  await checkAccept(c, prs.t, answered);
  await sleep(1500);
  clearInterval(recorder);
  sse.close();
  writeFileSync(join(dir, "multi-summary.json"), JSON.stringify({ state, thread: await threadOf(c), sseTypes: sse.types(), events: sse.events.length }, null, 2));
}

/** checkDesign: the designer's page is a hosted deliverable served from a signed URL inside a CSP sandbox. */
async function checkDesign(c, answered) {
  const d = await waitFor("designer publishes the page", async () => {
    await progress(c, answered);
    const list = await c.get(`/api/projects/${state.projectId}/deliverables`);
    return (list.items ?? []).find((x) => x.detail?.hosted);
  }, { timeoutMs: 900_000, everyMs: 5000 });
  state.deliverableId = d.id;
  save();
  check(d.published_by?.startsWith("designer") && d.detail.render === "html", `deliverable ${d.id} "${d.title}" published by ${d.published_by} (${d.detail.entry}, ${d.detail.size} bytes)`);
  const files = await c.get(`/api/deliverables/${d.id}/files`);
  check(files.render === "html" && files.isolated === false && files.entry_url.includes("/files/"), `signed entry URL ${files.entry_url.replace(/\/files\/[^/]+/, "/files/<token>")}`);
  const entry = new URL(files.entry_url, base).href; // relative (/files/…) without a separate file origin
  const res = await fetch(entry, { redirect: "manual" });
  const body = await res.text();
  const csp = res.headers.get("content-security-policy") ?? "";
  check(res.status === 200 && /hello design/i.test(body), `page served without a cookie (${res.status}, ${body.length} bytes, mentions "Hello design")`);
  check(csp.startsWith("sandbox allow-scripts") && !csp.includes("allow-same-origin") && res.headers.get("x-content-type-options") === "nosniff",
    `served with CSP sandbox without allow-same-origin and nosniff`, csp);
  const forged = await fetch(entry.replace(/\.([0-9a-f]{32})\//, (_, mac) => `.${mac.replace(/^./, mac[0] === "0" ? "1" : "0")}/`));
  check(forged.status === 403, `a tampered signature is refused (${forged.status})`);
  const tools = sql(`SELECT s.init->'tools' FROM at_agent_sessions s JOIN at_employees e ON e.id = s.employee_id
    WHERE e.project_id = '${state.projectId}' AND e.name LIKE 'designer%' ORDER BY s.opened_at DESC LIMIT 1`);
  check(tools.includes('"Skill"') && tools.includes("publish_file"), "designer session had the Skill tool and publish_file");
}

/** checkBranches: thread branches were cut from each repository's own base and PRs target it. */
function checkBranches(t) {
  const byRepo = Object.fromEntries(t.repos.map((r) => [r.repo, r]));
  check(byRepo[API]?.base_branch === "develop", `thread base in ${API} is develop (from the project context)`, t.repos);
  check(byRepo[WEB]?.base_branch === "main", `thread base in ${WEB} is main (its default branch)`, t.repos);
  check(gitShow(gitRoot, WEB, t.branch, "web.txt")?.trim() === "web ok", `${WEB} ${t.branch} has web.txt`);
  check(gitShow(gitRoot, API, t.branch, "api.txt")?.trim() === "api ok", `${API} ${t.branch} has api.txt`);
  check(gitIsAncestor(gitRoot, API, "develop^{/Start develop}", t.branch) && gitShow(gitRoot, API, t.branch, "DEVELOP.md") !== null, `${API} ${t.branch} branches from develop`);
  check(gitShow(gitRoot, WEB, t.branch, "api.txt") === null && gitShow(gitRoot, API, t.branch, "web.txt") === null, "each file landed only in its own repository");
  const pulls = JSON.parse(readFileSync(join(gitRoot, ".localgh-pulls.json"), "utf8"));
  const prOf = (slug) => Object.values(pulls).find((p) => p.slug === slug && p.head === t.branch);
  check(prOf(WEB)?.base === "main" && prOf(API)?.base === "develop", `PRs: ${WEB} #${prOf(WEB)?.number} -> ${prOf(WEB)?.base}, ${API} #${prOf(API)?.number} -> ${prOf(API)?.base}`);
  check(byRepo[WEB].pr_number === prOf(WEB).number && byRepo[API].pr_number === prOf(API).number, "thread view shows both PR numbers");
}

/** checkAccept: accepting the thread merges both PRs into their bases. */
async function checkAccept(c, t, answered) {
  const status = await waitFor("thread waits for acceptance", async () => {
    const st = (await progress(c, answered))?.t.status;
    return (st === "review" || st === "done") && st;
  }, { timeoutMs: 300_000, everyMs: 5000 });
  if (status === "review") await c.post(`/api/threads/${t.id}/accept`, {}); // done already on a resumed run
  const done = await waitFor("both PRs merged", async () => {
    const p = await progress(c, answered);
    return p?.t.repos.every((r) => r.pr_state === "merged") && p.t;
  }, { timeoutMs: 300_000, everyMs: 5000 });
  check(gitShow(gitRoot, WEB, "main", "web.txt")?.trim() === "web ok" && gitShow(gitRoot, API, "develop", "api.txt")?.trim() === "api ok",
    `accepted: web.txt merged into ${WEB} main, api.txt into ${API} develop`);
  check(gitShow(gitRoot, API, "main", "api.txt") === null, `${API} main untouched`);
  console.log(`  thread #${done.no} ${done.status}`);
}

main().then(() => process.exit(0), (e) => {
  console.error(e.stack ?? e);
  process.exit(1); // the SSE reader would keep the process alive
});
