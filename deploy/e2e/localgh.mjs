#!/usr/bin/env node
// localgh: the subset of the GitHub REST API the control plane uses, served from bare
// repositories on disk. For local end-to-end tests only (no auth: every token is accepted).
//
//   node localgh.mjs --root /path/to/gitroot --port 8787
//
// Repositories live at <root>/<owner>/<name>.git, the same layout the node daemon clones from
// with --git-base-url file://<root>. Point the control plane at it with GITHUB_API_URL.
// Merges use `git merge-tree --write-tree` (git >= 2.38); pull requests are kept in
// <root>/.localgh-pulls.json, numbered per repository. Check runs are always empty (no CI).
// LOCALGH_TOKENS=a,b makes it accept only those bearer tokens (401 otherwise) and log which
// one each request used, so a test can prove the control plane decrypted a sealed token.
import { execFile } from "node:child_process";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { join } from "node:path";
import { promisify } from "node:util";

const run = promisify(execFile);
const args = parseArgs(process.argv.slice(2));
const root = args.root ?? process.env.LOCALGH_ROOT;
const port = Number(args.port ?? process.env.LOCALGH_PORT ?? 8787);
const identity = { GIT_AUTHOR_NAME: "localgh", GIT_AUTHOR_EMAIL: "localgh@localhost", GIT_COMMITTER_NAME: "localgh", GIT_COMMITTER_EMAIL: "localgh@localhost" };

function parseArgs(list) {
  const out = {};
  for (let i = 0; i < list.length; i += 2) out[list[i].replace(/^--/, "")] = list[i + 1];
  return out;
}

class HttpError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function git(repo, argv, opts = {}) {
  try {
    const { stdout } = await run("git", ["--git-dir", repo, ...argv], { env: { ...process.env, ...identity }, maxBuffer: 64 << 20, ...opts });
    return stdout;
  } catch (err) {
    err.gitExit = err.code;
    throw err;
  }
}

async function tryGit(repo, argv) {
  return git(repo, argv).then((s) => s.trim(), () => null);
}

function repoPath(owner, name) {
  const p = join(root, owner, `${name}.git`);
  if (!existsSync(p)) throw new HttpError(404, "Not Found");
  return p;
}

function repoView(owner, name) {
  return { full_name: `${owner}/${name}`, private: true, default_branch: "main", description: "local repository",
    html_url: `http://localgh/${owner}/${name}`, pushed_at: new Date().toISOString() };
}

function listRepos() {
  if (!existsSync(root)) return [];
  return readdirSync(root, { withFileTypes: true }).filter((d) => d.isDirectory() && !d.name.startsWith("."))
    .flatMap((o) => readdirSync(join(root, o.name)).filter((n) => n.endsWith(".git")).map((n) => repoView(o.name, n.slice(0, -4))));
}

async function refSHA(repo, branch) {
  const sha = await tryGit(repo, ["rev-parse", "--verify", "--quiet", `refs/heads/${branch}^{commit}`]);
  if (!sha) throw new HttpError(404, "Not Found");
  return sha;
}

async function isAncestor(repo, a, b) {
  return (await tryGit(repo, ["merge-base", "--is-ancestor", a, b])) !== null;
}

async function createRef(repo, body) {
  const branch = String(body.ref).replace(/^refs\/heads\//, "");
  if (await tryGit(repo, ["rev-parse", "--verify", "--quiet", `refs/heads/${branch}`])) throw new HttpError(422, "Reference already exists");
  await git(repo, ["update-ref", `refs/heads/${branch}`, body.sha]);
  return [201, { ref: `refs/heads/${branch}`, object: { sha: body.sha } }];
}

async function updateRef(repo, branch, body) {
  const current = await refSHA(repo, branch);
  if (!body.force && !(await isAncestor(repo, current, body.sha))) throw new HttpError(422, "Update is not a fast forward");
  await git(repo, ["update-ref", `refs/heads/${branch}`, body.sha]);
  return [200, { ref: `refs/heads/${branch}`, object: { sha: body.sha } }];
}

async function deleteRef(repo, branch) {
  await refSHA(repo, branch);
  await git(repo, ["update-ref", "-d", `refs/heads/${branch}`]);
  return [204, null];
}

/** mergeCommit merges head into base like POST /merges: 204 when nothing to merge, 409 on conflict. */
async function mergeCommit(repo, base, head, message) {
  const baseSHA = await refSHA(repo, base);
  const headSHA = (await tryGit(repo, ["rev-parse", "--verify", "--quiet", `${head}^{commit}`])) ?? (await refSHA(repo, head));
  if (await isAncestor(repo, headSHA, baseSHA)) return [204, null];
  const tree = await tryGit(repo, ["merge-tree", "--write-tree", baseSHA, headSHA]);
  if (!tree) throw new HttpError(409, "Merge conflict");
  const sha = (await git(repo, ["commit-tree", tree.split("\n")[0], "-p", baseSHA, "-p", headSHA, "-m", message || `Merge ${head} into ${base}`])).trim();
  await git(repo, ["update-ref", `refs/heads/${base}`, sha, baseSHA]);
  return [201, { sha, commit: { message } }];
}

async function branches(repo) {
  const out = await git(repo, ["for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads"]);
  return [200, out.trim().split("\n").filter(Boolean).map((l) => { const [name, sha] = l.split(" "); return { name, commit: { sha } }; })];
}

async function contents(repo, path, ref) {
  const rev = ref || "main";
  const type = await tryGit(repo, ["cat-file", "-t", `${rev}:${path}`]);
  if (type === "tree") return [200, await dirListing(repo, rev, path)];
  if (type !== "blob") throw new HttpError(404, "Not Found");
  const content = await git(repo, ["show", `${rev}:${path}`], { encoding: "buffer" });
  const sha = await git(repo, ["rev-parse", `${rev}:${path}`]);
  return [200, { type: "file", path, sha: sha.trim(), encoding: "base64", content: Buffer.from(content).toString("base64") }];
}

async function dirListing(repo, rev, path) {
  const out = await git(repo, ["ls-tree", `${rev}:${path}`]);
  return out.trim().split("\n").filter(Boolean).map((l) => {
    const [meta, name] = l.split("\t");
    const [, kind, sha] = meta.split(" ");
    return { name, path: `${path}/${name}`, sha, type: kind === "tree" ? "dir" : "file" };
  });
}

const pullsFile = () => join(root, ".localgh-pulls.json");
const loadPulls = () => (existsSync(pullsFile()) ? JSON.parse(readFileSync(pullsFile(), "utf8")) : {});
const savePulls = (p) => writeFileSync(pullsFile(), JSON.stringify(p, null, 2));

// Pull requests are numbered per repository, like GitHub: keys are "<owner>/<name>#<number>".
const pullKey = (slug, number) => `${slug}#${number}`;

async function createPull(repo, slug, body) {
  const pulls = loadPulls();
  if (!(await tryGit(repo, ["rev-parse", "--verify", "--quiet", `refs/heads/${body.base}`]))) throw new HttpError(422, `base ${body.base} does not exist`);
  const number = Object.values(pulls).filter((p) => p.slug === slug).length + 1;
  const pr = { slug, number, head: body.head, base: body.base, title: body.title, body: body.body ?? "", state: "open", merged: false };
  pulls[pullKey(slug, number)] = pr;
  savePulls(pulls);
  return [201, await pullView(repo, number, pr)];
}

async function pullView(repo, number, pr) {
  const sha = await tryGit(repo, ["rev-parse", "--verify", "--quiet", `refs/heads/${pr.head}`]);
  return { number, html_url: `http://localgh/${pr.slug}/pull/${number}`, state: pr.state, merged: pr.merged, mergeable: true,
    title: pr.title, additions: 0, deletions: 0, head: { sha: sha ?? pr.head_sha ?? "", ref: pr.head }, base: { ref: pr.base } };
}

async function getPull(repo, slug, number) {
  const pr = loadPulls()[pullKey(slug, number)];
  if (!pr) throw new HttpError(404, "Not Found");
  return [200, await pullView(repo, pr.number, pr)];
}

async function mergePull(repo, slug, number) {
  const pulls = loadPulls();
  const pr = pulls[pullKey(slug, number)];
  if (!pr) throw new HttpError(404, "Not Found");
  if (pr.merged) throw new HttpError(405, "Pull Request is not mergeable");
  const [, out] = await mergeCommit(repo, pr.base, pr.head, `${pr.title} (#${number})`);
  pr.head_sha = await tryGit(repo, ["rev-parse", "--verify", "--quiet", `refs/heads/${pr.head}`]);
  Object.assign(pr, { merged: true, state: "closed" });
  savePulls(pulls);
  return [200, { merged: true, sha: out?.sha ?? "" }];
}

/** routes: [method, pattern, handler(match, body, url)]; repo routes get the bare repo path first. */
const routes = [
  ["GET", /^\/user$/, () => [200, { login: "local", type: "User" }]],
  ["GET", /^\/user\/repos$/, () => [200, listRepos()]],
  ["GET", /^\/user\/installations$/, () => [200, { total_count: 0, installations: [] }]],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)$/, (m) => (repoPath(m[1], m[2]), [200, repoView(m[1], m[2])])],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)\/branches$/, (m) => branches(repoPath(m[1], m[2]))],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)\/git\/ref\/heads\/(.+)$/, async (m) => [200, { ref: `refs/heads/${m[3]}`, object: { sha: await refSHA(repoPath(m[1], m[2]), m[3]) } }]],
  ["POST", /^\/repos\/([^/]+)\/([^/]+)\/git\/refs$/, (m, b) => createRef(repoPath(m[1], m[2]), b)],
  ["PATCH", /^\/repos\/([^/]+)\/([^/]+)\/git\/refs\/heads\/(.+)$/, (m, b) => updateRef(repoPath(m[1], m[2]), m[3], b)],
  ["DELETE", /^\/repos\/([^/]+)\/([^/]+)\/git\/refs\/heads\/(.+)$/, (m) => deleteRef(repoPath(m[1], m[2]), m[3])],
  ["POST", /^\/repos\/([^/]+)\/([^/]+)\/merges$/, (m, b) => mergeCommit(repoPath(m[1], m[2]), b.base, b.head, b.commit_message)],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)\/commits\/([^/]+)\/check-runs$/, () => [200, { total_count: 0, check_runs: [] }]],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)\/contents\/(.+)$/, (m, _b, u) => contents(repoPath(m[1], m[2]), m[3], u.searchParams.get("ref"))],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)\/rulesets$/, () => [200, []]],
  ["POST", /^\/repos\/([^/]+)\/([^/]+)\/pulls$/, (m, b) => createPull(repoPath(m[1], m[2]), `${m[1]}/${m[2]}`, b)],
  ["GET", /^\/repos\/([^/]+)\/([^/]+)\/pulls\/(\d+)$/, (m) => getPull(repoPath(m[1], m[2]), `${m[1]}/${m[2]}`, m[3])],
  ["PUT", /^\/repos\/([^/]+)\/([^/]+)\/pulls\/(\d+)\/merge$/, (m) => mergePull(repoPath(m[1], m[2]), `${m[1]}/${m[2]}`, m[3])],
];

async function readBody(req) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const raw = Buffer.concat(chunks).toString("utf8");
  return raw ? JSON.parse(raw) : {};
}

const allowed = (process.env.LOCALGH_TOKENS ?? "").split(",").map((t) => t.trim()).filter(Boolean);

function checkToken(req) {
  if (allowed.length === 0) return "";
  const token = (req.headers.authorization ?? "").replace(/^(Bearer|token)\s+/i, "");
  if (!allowed.includes(token)) throw new HttpError(401, "Bad credentials");
  return ` token#${allowed.indexOf(token)}`;
}

async function dispatch(req) {
  req.tokenTag = checkToken(req);
  const url = new URL(req.url, "http://localgh");
  const path = decodeURIComponent(url.pathname);
  for (const [method, pattern, handler] of routes) {
    const m = req.method === method && path.match(pattern);
    if (m) return handler(m, await readBody(req), url);
  }
  throw new HttpError(404, "Not Found");
}

function send(res, status, body) {
  res.writeHead(status, body === null ? {} : { "Content-Type": "application/json" });
  res.end(body === null ? undefined : JSON.stringify(body));
}

function serve(listenPort = port) {
  const server = createServer((req, res) => {
    dispatch(req).then(([status, body]) => send(res, status, body), (err) => {
      const status = err instanceof HttpError ? err.status : 500;
      if (status === 500) console.error(req.method, req.url, err);
      send(res, status, { message: err.message });
    }).finally(() => console.log(req.method, req.url, res.statusCode, req.tokenTag ?? ""));
  });
  return new Promise((resolve) => server.listen(listenPort, "0.0.0.0", () => resolve(server)));
}

if (!root) {
  console.error("usage: localgh.mjs --root <dir> [--port 8787]");
  process.exit(2);
}
serve().then(() => console.log(`localgh on :${port}, repositories in ${root}`));
