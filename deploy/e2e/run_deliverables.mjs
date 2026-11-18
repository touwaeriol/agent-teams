#!/usr/bin/env node
// Real-Claude run for revising a deliverable from another thread (deploy/e2e/up.sh first):
//   1. designer-1 in thread #1 publishes an HTML page with publish_file
//   2. in thread #2 dev-1 is asked to change the page title: it calls list_deliverables and
//      read_deliverable (files land in .agent-deliverables/, git-ignored), edits the file and
//      publishes v2 with the same deliverable_id
//   3. v2 exists, was published by thread #2's dev-1, its content changed; still one deliverable
//   4. a user comment on v2 reaches dev-1 of thread #2
//   node deploy/e2e/run_deliverables.mjs <scratch-dir> [--model haiku]
import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { installDaemon, login, nodeOnline, roleBody, sql, startDaemon } from "./harness.mjs";
import { check, readEnv, waitFor } from "./lib.mjs";

const dir = process.argv[2];
const arg = (name, d) => (process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : d);
const model = arg("--model", "haiku");
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;
const nodeDir = join(dir, "node-deliverables");

const ROLES = [
  { tier: "coordinator", role: "coordinator", title: "统筹", count: 1,
    instructions: "This is a test. Never open threads and never call remember. Reply in one short line." },
  { tier: "lead", role: "lead", title: "组长", count: 1,
    instructions: "This is a cheap test. The brief names one member role (designer or dev). Steps: update_plan with one step \"做\" (status todo), " +
      "then assign_task(assignee=<that role>, instruction=<the brief, copied word for word>, main=\"做\"). When the task is reported done, " +
      "update_plan with the step done and stop. Never call finish, open_pr, enqueue_merge or assign_task again." },
  { tier: "member", role: "designer", title: "设计师", count: 1, skills: ["design"],
    instructions: "This is a cheap test. Do exactly what the task says, nothing more, then complete_task with a one-line summary." },
  { tier: "member", role: "dev", title: "开发", count: 1,
    instructions: "This is a cheap test. Do exactly what the task says, nothing more, then complete_task with a one-line summary. Never git commit." },
];

const PAGE = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Alpha Page</title></head><body><h1>Hello</h1></body></html>";

async function setup(c) {
  await c.put("/api/roles", { roles: ROLES.map((r, i) => roleBody({ ...r, permission_mode: "bypass" }, i, model)) });
  const nodes = await c.get("/api/nodes");
  for (const n of (nodes.nodes ?? nodes).filter((x) => x.name.startsWith("rc-e2e-") && !x.online)) await c.call("DELETE", `/api/nodes/${n.id}`);
  const node = await c.post("/api/nodes", { name: `rc-e2e-deliv-${Date.now() % 100000}` });
  const conn = await c.post("/api/git/connections", { name: `local-${Date.now() % 100000}`, token: "ghp_e2eToken" });
  const project = await c.post("/api/projects", { name: `deliverables e2e ${Date.now() % 100000}`, repos: [{ repo: "local/hello", git_connection_id: conn.id }] });
  check(true, `project ${project.id}, node ${node.node.id}, model ${model}`);
  return { projectId: project.id, nodeId: node.node.id, token: node.token };
}

const files = (id, version) => sql(`SELECT path || '|' || published_by || '|' || publisher_id FROM at_deliverable_files
  WHERE deliverable_id = '${id}' AND version = ${version} ORDER BY path`).split("\n").filter(Boolean);
const content = (id, version, path) => sql(`SELECT convert_from(data, 'UTF8') FROM at_deliverable_files
  WHERE deliverable_id = '${id}' AND version = ${version} AND path = '${path}'`);
const employeeId = (tid, name) => sql(`SELECT id FROM at_employees WHERE project_id = (SELECT project_id FROM at_threads WHERE id = '${tid}') AND thread_id IS NULL AND name = '${name}'`); // project member
const toolCalls = (empId, tool) => sql(`SELECT count(*) FROM at_tool_calls c JOIN at_agent_sessions s ON s.id = c.session_id
  WHERE s.employee_id = '${empId}' AND c.tool = '${tool}' AND c.status = 'done'`);

async function publishThread(c, st) {
  const brief = `Role: designer. Create the file design/page.html with exactly this content: ${PAGE} ` +
    "Then call publish_file(path=\"design/page.html\", kind=\"design\", title=\"Alpha page\"). Then complete_task.";
  const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "Alpha page", brief });
  const d = await waitFor("designer published the page", async () => {
    const list = await c.get(`/api/projects/${st.projectId}/deliverables`);
    return list.items.find((x) => x.detail?.hosted && x.title === "Alpha page");
  }, { timeoutMs: 600_000, everyMs: 3000 });
  check(d.published_by === "designer-1" && d.thread_no === t.no, `thread #${t.no}: designer-1 published ${d.id} (${d.detail.entry})`);
  return { thread: t, deliverable: d };
}

async function reviseThread(c, st, first) {
  const brief = "Role: dev. This project already has a deliverable titled \"Alpha page\" (an HTML page made in another thread). " +
    "Change its HTML <title> from \"Alpha Page\" to \"Beta Page\" and change nothing else. Use list_deliverables to find its id, " +
    "read_deliverable to download it, edit the downloaded file in place, then publish_file with that file's path, kind \"design\", " +
    "title \"Alpha page\" and the same deliverable_id. Do not create a new deliverable. Then complete_task.";
  const t = await c.post(`/api/projects/${st.projectId}/threads`, { title: "Retitle the page", brief });
  const id = first.deliverable.id;
  await waitFor("v2 published", async () => files(id, 2).length > 0, { timeoutMs: 600_000, everyMs: 3000 });
  const dev = employeeId(t.id, "dev-1");
  const v2 = files(id, 2);
  check(v2.every((r) => r.endsWith(`|dev-1|${dev}`)), `v2 published by thread #${t.no}'s dev-1 (${v2.join(", ")})`);
  const entry = v2[0].split("|")[0];
  const html = content(id, 2, entry);
  check(html.includes("<title>Beta Page</title>") && !html.includes("Alpha Page") && html.includes("<h1>Hello</h1>"), `v2 content changed: ${html.slice(0, 120)}`);
  check(content(id, 1, first.deliverable.detail.entry).includes("<title>Alpha Page</title>"), "v1 kept unchanged");
  const count = sql(`SELECT count(*) FROM at_deliverables WHERE project_id = '${st.projectId}' AND kind <> 'pr'`);
  check(count === "1", "still a single deliverable (no new one created)");
  check(Number(toolCalls(dev, "list_deliverables")) >= 1 && Number(toolCalls(dev, "read_deliverable")) >= 1, "dev-1 called list_deliverables and read_deliverable");
  const view = await c.get(`/api/deliverables/${id}/files`);
  const top = view.versions[0];
  check(top.version === 2 && top.published_by === "dev-1" && top.publisher_thread_no === t.no, `version list: v2 · dev-1 修改（#${top.publisher_thread_no}）`);
  checkDownloadIgnored(id);
  return { thread: t, dev };
}

/** checkDownloadIgnored finds the downloaded copy on the node and checks git never sees it. */
function checkDownloadIgnored(id) {
  const found = [];
  const walk = (d, depth) => {
    if (depth > 8) return;
    for (const e of readdirSync(d, { withFileTypes: true })) {
      if (!e.isDirectory() || e.name === ".git" || e.name === "node_modules") continue;
      const p = join(d, e.name);
      if (e.name === ".agent-deliverables") found.push(d);
      else walk(p, depth + 1);
    }
  };
  walk(join(nodeDir, "root"), 0);
  const cwd = found.find((d) => existsSync(join(d, ".agent-deliverables", id)));
  check(Boolean(cwd), `read_deliverable wrote ${cwd}/.agent-deliverables/${id}/`);
  check(readFileSync(join(cwd, ".agent-deliverables", ".gitignore"), "utf8").includes("*"), "the folder carries a .gitignore of *");
  for (const repo of [cwd, ...readdirSync(cwd).map((n) => join(cwd, n))].filter((p) => existsSync(join(p, ".git")))) {
    const status = execFileSync("git", ["-C", repo, "status", "--porcelain", "--untracked-files=all"]).toString();
    check(!status.includes(".agent-deliverables"), `git status in ${repo.split("/").slice(-2).join("/")} does not list the downloaded files`);
  }
}

async function commentReachesDev(c, first, second) {
  const id = first.deliverable.id;
  const sent = await c.post(`/api/deliverables/${id}/comments`, { text: "e2e 批注：标题再加一个感叹号", version: 2 });
  check(sent.sent_to === "dev-1", `comment on v2 sent to ${sent.sent_to}`);
  const msg = await waitFor("comment message in thread #2", async () => sql(`SELECT count(*) FROM at_messages WHERE conversation_id = '${second.thread.id}'
    AND content LIKE '%e2e 批注%' AND meta->>'to' = 'dev-1'`) !== "0", { timeoutMs: 60_000 });
  check(msg, `comment delivered as a message to thread #${second.thread.no}'s dev-1, none to thread #${first.thread.no}`);
  const wrong = sql(`SELECT count(*) FROM at_messages WHERE conversation_id = '${first.thread.id}' AND content LIKE '%e2e 批注%'`);
  check(wrong === "0", "thread #1 got no copy of the comment");
}

async function main() {
  for (const d of ["config", "data", "state", "run", "root"]) rmSync(join(nodeDir, d), { recursive: true, force: true });
  const c = await login(base);
  const st = await setup(c);
  const pkg = process.env.E2E_DAEMON ? null : installDaemon(base, nodeDir);
  const daemon = startDaemon({ base, nodeDir, token: st.token, gitRoot: env.E2E_GIT_ROOT, pkg });
  try {
    await waitFor("node online", () => nodeOnline(c, st.nodeId), { timeoutMs: 60_000 });
    const first = await publishThread(c, st);
    const second = await reviseThread(c, st, first);
    await commentReachesDev(c, first, second);
  } finally {
    daemon.kill("SIGTERM");
  }
}

main().catch((e) => {
  console.error(e.stack ?? e);
  process.exitCode = 1;
});
