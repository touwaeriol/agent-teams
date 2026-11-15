#!/usr/bin/env node
// Proves the v2 control plane reads v1 data in place: rows written by the v1 Python code
// (deploy/v2/e2e/v1_seed.py) before `atctl migrate up`.
//   node deploy/v2/e2e/v1_check.mjs <scratch-dir>/v1.json
import { readFileSync } from "node:fs";
import { Client, check, readEnv } from "./lib.mjs";

const v1 = JSON.parse(readFileSync(process.argv[2], "utf8"));
const env = readEnv();
const base = env.AGENT_TEAMS_PUBLIC_URL;

async function checkLogin() {
  const c = new Client(base);
  const bad = await c.raw("POST", "/auth/login", { email: v1.email, password: "wrong" });
  check(bad.status === 401 || bad.status === 400, "wrong password refused", bad.status);
  const r = await c.raw("POST", "/auth/login", { email: v1.email, password: v1.password });
  check(r.status === 200 && c.cookies.agent_teams_session, "v1 scrypt password accepted by /auth/login", r.json);
  const me = await c.get("/api/me");
  check(me.email === v1.email && me.id === v1.user_id, "login session belongs to the v1 user", me);
}

async function checkSession() {
  const c = new Client(base, { agent_teams_session: v1.session_token });
  const me = await c.get("/api/me");
  check(me.email === v1.email, "v1 session cookie (sha256 hash in at_sessions) accepted", me);
  check(Boolean(c.cookies.agent_teams_csrf), "v1 session got a CSRF token on its first request");
  const projects = await c.get("/api/projects");
  const list = Array.isArray(projects) ? projects : projects.projects ?? projects.items;
  check(list.some((p) => p.id === v1.project_id), "v1 project listed", projects);
  const mem = await c.post(`/api/projects/${v1.project_id}/memory`, { text: "v1 session can write with CSRF" });
  check(Boolean(mem), "cookie write with X-CSRF-Token works for a v1 session", mem);
  return c;
}

async function checkSecret(c) {
  const repos = await c.get(`/api/git/connections/${v1.connection_id}/repos`);
  const names = JSON.stringify(repos);
  check(names.includes("local/v1repo"), "Fernet token sealed by v1 decrypted (localgh accepts only that token)", repos);
}

async function checkLegacy(c) {
  const roles = await c.get("/api/roles");
  const dev = roles.roles.find((r) => r.role === "dev");
  check(dev?.tier === "member" && dev.instructions === "You write code.", "v1 at_teams YAML converted to role templates", roles);
  const convs = await c.get("/api/conversations");
  check(JSON.stringify(convs).includes(v1.conversation_id), "v1 conversation listed read-only", convs);
  const msgs = await c.get(`/api/conversations/${v1.conversation_id}/messages`);
  check(JSON.stringify(msgs).includes("hello from v1"), "v1 messages readable", msgs);
}

function hello() {
  return { v: 2, type: "hello", id: "n_01JD3V0000000000000000001", ts: new Date().toISOString(),
    body: { protocols: [2], daemon: { version: "2.0.0", node: process.versions.node, os: "linux", arch: "x64" },
      epoch: "01JD3V0000000000000000000E", last_seq: 0, acked_cmd_seq: 0, host: "v1-check", root: "/tmp/v1-check",
      capacity: { max_active_sessions: 1 }, runtimes: {}, features: ["sessions"], isolated: false, isolation: "none" } };
}

function connect(token) {
  const url = base.replace(/^http/, "ws") + "/nodes/connect";
  return new Promise((resolve) => {
    const ws = new WebSocket(url, { headers: { Authorization: `Bearer ${token}` } });
    const done = (result) => { resolve({ ...result, ws }); };
    ws.onopen = () => ws.send(JSON.stringify(hello()));
    ws.onmessage = (m) => done({ welcome: JSON.parse(m.data) });
    ws.onclose = (e) => done({ closed: e.code });
    ws.onerror = () => done({ error: true });
  });
}

async function checkNodeToken(c) {
  const bad = await connect("not-a-token");
  check(!bad.welcome, "unknown node token refused", { closed: bad.closed, error: bad.error });
  const good = await connect(v1.node_token);
  check(good.welcome?.type === "welcome" && good.welcome.body.node_id === v1.node_id, "v1 node token (sha256) accepted, welcome names the v1 node", good.welcome);
  const nodes = await c.get("/api/nodes");
  const list = Array.isArray(nodes) ? nodes : nodes.nodes ?? nodes.items;
  check(list.some((n) => n.id === v1.node_id && n.online), "v1 node shows online in GET /api/nodes", nodes);
  good.ws.close();
}

await checkLogin();
const c = await checkSession();
await checkSecret(c);
await checkLegacy(c);
await checkNodeToken(c);
console.log("v1 compatibility: all checks passed");
