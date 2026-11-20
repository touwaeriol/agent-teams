// End-to-end remote servers (讨论串 #38, docs/v2/remote-servers-verification.md): a real console
// (atctl serve), a real daemon with scripted Claude sessions, real OpenSSH clients (ssh, scp, rsync)
// and two real targets. Skipped unless server/scripts/e2e-ssh/run.sh sets AT_E2E_URL.
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import type { KeyObject } from "node:crypto";
import { connect } from "node:net";
import { join } from "node:path";
import { afterAll, describe, expect, it } from "vitest";
import type { Options, Query, SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import { Daemon } from "../src/daemon.js";
import { createLogger, silentLogger } from "../src/util/log.js";
import { fakeQueryFactory, type FakeQueryLog } from "./helpers/fake-query.js";

const E = process.env;
const BASE = E.AT_E2E_URL ?? "";
const WORK = E.AT_E2E_WORK ?? "";
const TARGET_PASSWORD = E.AT_E2E_PW ?? "";
const KEY_PASSPHRASE = E.AT_E2E_KEY_PASS ?? "";
const USER = E.AT_E2E_USER ?? "";

/** What the run prints for the verification document. */
const report: string[] = [];
const note = (s: string) => {
  report.push(s);
  console.log(`[e2e] ${s}`);
};

// ---- console HTTP client (cookie session + CSRF header) ----

const cookies = new Map<string, string>();
async function api<T = unknown>(method: string, path: string, body?: unknown, want?: number): Promise<{ status: number; body: T }> {
  const headers: Record<string, string> = { Cookie: [...cookies].map(([k, v]) => `${k}=${v}`).join("; ") };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && cookies.has("agent_teams_csrf")) headers["X-CSRF-Token"] = cookies.get("agent_teams_csrf")!;
  const res = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  for (const c of res.headers.getSetCookie()) {
    const [kv] = c.split(";");
    const i = kv!.indexOf("=");
    cookies.set(kv!.slice(0, i), kv!.slice(i + 1));
  }
  const text = await res.text();
  const parsed = (text ? JSON.parse(text) : null) as T;
  if (want !== undefined && res.status !== want) throw new Error(`${method} ${path}: ${res.status} ${text.slice(0, 500)}`);
  return { status: res.status, body: parsed };
}

// ---- processes ----

interface Run {
  code: number;
  out: string;
  err: string;
}
function run(cmd: string, args: string[], env: Record<string, string>, timeoutMs = 30_000, cwd?: string): Promise<Run> {
  return new Promise((resolve) => {
    const c = spawn(cmd, args, { env, cwd, stdio: ["ignore", "pipe", "pipe"] });
    let out = "";
    let err = "";
    const t = setTimeout(() => c.kill("SIGKILL"), timeoutMs);
    c.stdout.on("data", (d) => (out += d));
    c.stderr.on("data", (d) => (err += d));
    c.on("close", (code) => {
      clearTimeout(t);
      resolve({ code: code ?? -1, out, err });
    });
  });
}
const short = (r: Run) => `exit ${r.code}${r.out.trim() ? ` · out: ${r.out.trim().slice(0, 160)}` : ""}${r.err.trim() ? ` · err: ${r.err.trim().replace(/\s+/g, " ").slice(0, 240)}` : ""}`;

async function waitFor<T>(what: string, get: () => T | undefined | null | false | Promise<T | undefined | null | false>, ms = 30_000): Promise<T> {
  const until = Date.now() + ms;
  for (;;) {
    const v = await get();
    if (v) return v;
    if (Date.now() > until) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

/** Every regular file under dir (symlinks not followed). */
function walk(dir: string, out: string[] = []): string[] {
  if (!existsSync(dir)) return out;
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    const st = statSync(p, { throwIfNoEntry: false });
    if (!st) continue;
    if (st.isDirectory()) walk(p, out);
    else if (st.isFile()) out.push(p);
  }
  return out;
}

// ---- scripted Claude: the lead plans and assigns, members stay idle ----

const log: FakeQueryLog = { options: [], prompts: [], closed: 0, interrupts: 0, modes: [] };
const byName = new Map<string, Options>();
let leadScript: string[] = [];

async function* prefixed(first: string[], rest: AsyncIterable<SDKUserMessage>): AsyncIterable<SDKUserMessage> {
  for (const text of first) yield { type: "user", message: { role: "user", content: text }, parent_tool_use_id: null, session_id: "" } as SDKUserMessage;
  yield* rest;
}
const fake = fakeQueryFactory(log);
function scriptedQuery(params: { prompt: AsyncIterable<SDKUserMessage>; options: Options }): Query {
  const name = (params.options.cwd ?? "").split("/").pop() ?? "";
  byName.set(name, params.options);
  const prompt = name.startsWith("lead") ? prefixed(leadScript, params.prompt) : params.prompt;
  return fake({ prompt, options: params.options });
}

/** A session's options by employee name; the lead's cwd ends in lead/, members' in their name. */
const optionsOf = (name: string) => byName.get(name) ?? (name.startsWith("lead") ? byName.get("lead") : undefined);
const envOf = (name: string) => optionsOf(name)!.env as Record<string, string>;

describe.skipIf(!BASE)("e2e: remote servers through a real console", () => {
  let daemon: Daemon | null = null;
  const data = join(WORK, "node");
  afterAll(async () => {
    await daemon?.stop();
    writeFileSync(join(WORK, "e2e-report.txt"), report.join("\n") + "\n");
  });

  it("grants role A only: A reaches both servers with ssh, scp and rsync; B and the lead are refused; secrets stay out", async () => {
    // 1. user, roles (dev = A, ops = B, both members), node, servers, project grants.
    await api("POST", "/auth/register", { email: `e2e-${Date.now()}@example.com`, password: "password123" }, 201);
    const roles = (await api<{ roles: Array<Record<string, unknown>> }>("GET", "/api/roles", undefined, 200)).body.roles;
    const dev = roles.find((r) => r.role === "dev")!;
    dev.count = 1;
    const ops = { ...dev, id: "", role: "ops", title: "运维", count: 1 };
    await api("PUT", "/api/roles", { roles: [...roles, ops] }, 200);
    const created = (await api<{ node: { id: string }; token: string }>("POST", "/api/nodes", { name: "e2e-node" }, 201)).body;
    const node = { id: created.node.id, token: created.token };

    const privateKey = readFileSync(join(WORK, "client_key"), "utf8");
    const pw = (await api<{ id: string }>("POST", "/api/servers", { name: "pwbox", host: "127.0.0.1", port: 2231, username: "e2e", auth: { kind: "password", password: TARGET_PASSWORD } }, 201)).body;
    const key = (await api<{ id: string; key_fingerprint: string; has_passphrase: boolean }>("POST", "/api/servers", {
      name: "keybox", host: "127.0.0.1", port: 2232, username: USER, auth: { kind: "private_key", private_key: privateKey, passphrase: KEY_PASSPHRASE },
    }, 201)).body;
    note(`服务器：pwbox（密码，Go 目标 127.0.0.1:2231）、keybox（私钥+口令，OpenSSH sshd 127.0.0.1:2232，has_passphrase=${key.has_passphrase}）`);
    for (const s of [pw, key]) {
      const t = (await api<{ ok: boolean; host_key_pinned: boolean; host_key_fp: string; error?: string }>("POST", `/api/servers/${s.id}/test`, undefined, 200)).body;
      note(`测试连接 ${s.id === pw.id ? "pwbox" : "keybox"}：ok=${t.ok} pinned=${t.host_key_pinned} ${t.host_key_fp}${t.error ? ` error=${t.error}` : ""}`);
      expect(t.ok).toBe(true);
      expect(t.host_key_pinned).toBe(true);
    }
    const project = (await api<{ id: string }>("POST", "/api/projects", { name: "e2e" }, 201)).body;
    const grant = (devOn: string[]) => api("PUT", `/api/projects/${project.id}/servers`, { servers: [pw, key].map((s) => ({ server_id: s.id, roles: devOn.includes(s.id) ? ["dev"] : [] })) }, 200);
    await grant([pw.id, key.id]);
    const view = (await api<{ roles: Array<{ role: string; grantable: boolean; reason?: string }> }>("GET", `/api/projects/${project.id}/servers`, undefined, 200)).body;
    note(`可授权岗位：${view.roles.map((r) => `${r.role}=${r.grantable ? "可" : `否(${r.reason})`}`).join(" ")}`);
    const leadGrant = await api<{ code?: string }>("PUT", `/api/projects/${project.id}/servers`, { servers: [{ server_id: pw.id, roles: ["dev", "lead"] }] });
    note(`给组长授权：${leadGrant.status} ${leadGrant.body?.code ?? ""}`);
    expect(leadGrant.status).toBe(422);

    // 2. the daemon (fresh data directory): real ssh-proxy through the CLI (tsx), scripted Claude.
    rmSync(data, { recursive: true, force: true });
    const wsUrl = BASE.replace(/^http/, "ws") + "/nodes/connect";
    daemon = new Daemon({
      url: wsUrl, token: node.token, root: join(data, "root"), stateDir: join(data, "state"), configDir: join(data, "config"),
      platformDir: join(data, "platform"), runtimeDir: join(data, "run"), installDir: join(data, "install"), maxActive: 6, passEnv: [],
      gitBaseUrl: `file://${join(data, "remotes")}`, claudeConfigDir: join(data, "claude"), codexHome: join(data, "codex"),
      nodeCmd: [join(__dirname, "..", "node_modules", ".bin", "tsx"), join(__dirname, "..", "src", "cli.ts")], log: E.AT_E2E_DEBUG ? createLogger("debug") : silentLogger,
      sshBaseDir: join(data, "ssh"), sshTmpDir: join(data, "tmp"), probeRuntimes: false, isolation: "none", queryFn: scriptedQuery as never,
      link: { pingMs: 2000, deadMs: 10_000, backoff: { minMs: 100, maxMs: 1000 } },
    });
    await daemon.start();
    await waitFor("node online", async () => {
      const b = (await api<unknown>("GET", "/api/nodes")).body as { nodes?: unknown[] } | unknown[];
      const list = (Array.isArray(b) ? b : (b.nodes ?? [])) as Array<{ id?: string; node?: { id: string }; online?: boolean }>;
      return list.some((n) => (n.id ?? n.node?.id) === node.id && n.online);
    });

    // 3. a thread: the lead assigns one task to dev (A) and one to ops (B).
    leadScript = [
      `use update_plan ${JSON.stringify({ steps: [{ title: "部署", status: "todo" }] })}`,
      `use assign_task ${JSON.stringify({ assignee: "dev", instruction: "deploy to the servers", main: "部署" })}`,
      `use assign_task ${JSON.stringify({ assignee: "ops", instruction: "look at the servers", main: "部署" })}`,
    ];
    const thread = (await api<{ id: string; no: number }>("POST", `/api/projects/${project.id}/threads`, { title: "E2E", brief: "use the servers" }, 201)).body;
    await waitFor("dev-1 and ops-1 sessions", () => optionsOf("dev-1") && optionsOf("ops-1") && optionsOf("lead-1"), 60_000).catch(async (e) => {
      const v = await api("GET", `/api/threads/${thread.id}`);
      throw new Error(`${(e as Error).message}; sessions=${[...byName.keys()]}; prompts=${JSON.stringify(log.prompts).slice(0, 1500)}; thread=${JSON.stringify(v.body).slice(0, 1500)}`);
    });
    const A = envOf("dev-1");
    const B = envOf("ops-1");
    const L = envOf("lead-1");
    note(`会话：dev-1 AT_SSH_CONFIG=${A.AT_SSH_CONFIG ? "有" : "无"}；ops-1 AT_SSH_CONFIG=${B.AT_SSH_CONFIG ? "有" : "无"}；lead-1 AT_SSH_CONFIG=${L.AT_SSH_CONFIG ? "有" : "无"}`);
    expect(A.AT_SSH_CONFIG).toBeTruthy();
    expect(B.AT_SSH_CONFIG).toBeUndefined();
    expect(L.AT_SSH_CONFIG).toBeUndefined();
    const sdir = join(A.AT_SSH_CONFIG!, "..");
    await waitFor("agent.sock", () => existsSync(join(sdir, "agent.sock")));

    // 4. role A: ssh, scp, rsync to both servers.
    const files = join(WORK, "files");
    mkdirSync(join(files, "tree", "sub"), { recursive: true });
    writeFileSync(join(files, "payload.txt"), "payload-e2e\n");
    writeFileSync(join(files, "tree", "a.txt"), "a\n");
    writeFileSync(join(files, "tree", "sub", "b.txt"), "b\n");
    for (const srv of ["pwbox", "keybox"]) {
      const r = await run("ssh", [srv, "echo ok"], A);
      note(`dev-1 $ ssh ${srv} 'echo ok' → ${short(r)}`);
      expect(r.out).toBe("ok\n");
      expect(r.code).toBe(0);
      const remote = join(WORK, `remote-${srv}`);
      mkdirSync(remote, { recursive: true });
      const scp = await run("scp", [join(files, "payload.txt"), `${srv}:${join(remote, "payload.txt")}`], A);
      note(`dev-1 $ scp payload.txt ${srv}:… → ${short(scp)}；目标上内容=${JSON.stringify(existsSync(join(remote, "payload.txt")) ? readFileSync(join(remote, "payload.txt"), "utf8") : null)}`);
      expect(scp.code).toBe(0);
      expect(readFileSync(join(remote, "payload.txt"), "utf8")).toBe("payload-e2e\n");
      const back = await run("scp", [`${srv}:${join(remote, "payload.txt")}`, join(files, `back-${srv}.txt`)], A);
      expect(back.code).toBe(0);
      const rs = await run("rsync", ["-a", join(files, "tree") + "/", `${srv}:${join(remote, "tree")}/`], A);
      note(`dev-1 $ rsync -a tree/ ${srv}:… → ${short(rs)}；同步后 sub/b.txt=${JSON.stringify(existsSync(join(remote, "tree", "sub", "b.txt")) ? readFileSync(join(remote, "tree", "sub", "b.txt"), "utf8") : null)}`);
      expect(rs.code).toBe(0);
      expect(readFileSync(join(remote, "tree", "sub", "b.txt"), "utf8")).toBe("b\n");
    }
    const exit7 = await run("ssh", ["pwbox", "exit 7"], A);
    note(`dev-1 $ ssh pwbox 'exit 7' → ${short(exit7)}`);
    expect(exit7.code).toBe(7);
    const asRoot = await run("ssh", ["root@pwbox", "id -un"], A);
    note(`dev-1 $ ssh root@pwbox 'id -un'（想换用户）→ ${short(asRoot)}`);
    expect(asRoot.code).not.toBe(0);

    // 5. role B and the lead.
    for (const [who, env] of [["ops-1", B], ["lead-1", L]] as const) {
      const r = await run("ssh", ["-o", "ConnectTimeout=5", "pwbox", "echo ok"], env);
      note(`${who} $ ssh pwbox 'echo ok' → ${short(r)}`);
      expect(r.code).not.toBe(0);
      // Borrow dev-1's ssh_config: ssh-proxy checks the session behind AT_CRED_SESSION / SECRET.
      const borrowed = await run("ssh", ["-F", A.AT_SSH_CONFIG!, "pwbox", "echo ok"], { ...env, PATH: A.PATH! });
      note(`${who} $ ssh -F <dev-1 的 ssh_config> pwbox → ${short(borrowed)}`);
      expect(borrowed.code).not.toBe(0);
    }

    // 6. dev-1's certificate under another session / open_gen, straight at the gateway with the node token.
    const gw = BASE.replace(/^http/, "ws") + "/nodes/ssh";
    const proxyJs = join(WORK, "wsproxy.mjs");
    writeFileSync(proxyJs, [
      `import { createRequire } from "node:module";`,
      `const require = createRequire(${JSON.stringify(join(__dirname, "..", "package.json"))});`,
      `const { WebSocket, createWebSocketStream } = require("ws");`,
      `const [url, token, session, gen, server] = process.argv.slice(2);`,
      `const ws = new WebSocket(url, "agent-teams-ssh.v1", { headers: { Authorization: "Bearer " + token, "X-AT-Session": session, "X-AT-Open-Gen": gen, "X-AT-Server": server } });`,
      `ws.on("unexpected-response", (_q, res) => { process.stderr.write("gateway refused: " + res.statusCode + "\\n"); process.exit(1); });`,
      `ws.on("error", (e) => { process.stderr.write("gateway error: " + e.message + "\\n"); process.exit(1); });`,
      `ws.on("open", () => { const s = createWebSocketStream(ws); process.stdin.pipe(s); s.pipe(process.stdout); s.on("close", () => process.exit(0)); s.on("error", () => process.exit(1)); });`,
    ].join("\n"));
    const direct = async (session: string, gen: number, label: string, quiet = false) => {
      const r = await run("ssh", ["-F", A.AT_SSH_CONFIG!, "-o", `ProxyCommand=${process.execPath} ${proxyJs} ${gw} ${node.token} ${session} ${gen} %n`, "pwbox", "echo ok"], A, 20_000);
      if (!quiet) note(`${label} → ${short(r)}`);
      return r;
    };
    // The control: dev-1's own session and open_gen (found by trying; the node knows it, the test does not).
    let aGen = 0;
    for (let g = 1; g <= 4 && !aGen; g++) if ((await direct(A.AT_CRED_SESSION!, g, "", true)).code === 0) aGen = g;
    note(`直连网关：dev-1 证书 + dev-1 会话头 + open_gen=${aGen}（对照组）→ ${aGen ? "exit 0 · out: ok" : "失败"}`);
    expect(aGen).toBeGreaterThan(0);
    expect((await direct(B.AT_CRED_SESSION ?? "s_none", 1, "直连网关：dev-1 证书 + ops-1 会话头")).code).not.toBe(0);
    expect((await direct(L.AT_CRED_SESSION ?? "s_none", 1, "直连网关：dev-1 证书 + lead-1 会话头")).code).not.toBe(0);
    expect((await direct(A.AT_CRED_SESSION!, aGen + 1, `直连网关：dev-1 证书 + open_gen=${aGen + 1}`)).code).not.toBe(0);

    // 7. a process without the session secret.
    const noSecret = await run("ssh", ["pwbox", "echo ok"], { ...A, AT_CRED_SECRET: "0".repeat(64) });
    note(`dev-1 环境但 AT_CRED_SECRET 错误 $ ssh pwbox → ${short(noSecret)}`);
    expect(noSecret.code).not.toBe(0);
    const noSecret2 = await run("ssh", ["pwbox", "echo ok"], Object.fromEntries(Object.entries(A).filter(([k]) => !k.startsWith("AT_CRED_"))));
    note(`dev-1 环境去掉 AT_CRED_* $ ssh pwbox → ${short(noSecret2)}`);
    expect(noSecret2.code).not.toBe(0);
    const agentProbe = await run("ssh-add", ["-L"], { ...A, SSH_AUTH_SOCK: join(sdir, "agent.sock") });
    note(`同账号进程直接连 agent.sock $ ssh-add -L → ${short(agentProbe).slice(0, 200)}`);

    // 8. secrets: env, files, the data directory.
    const secrets: Array<[string, string]> = [
      ["目标密码", TARGET_PASSWORD], ["私钥口令", KEY_PASSPHRASE], ["私钥正文", privateKey.split("\n")[2] ?? privateKey.slice(40, 100)],
      ["节点令牌", node.token],
    ];
    for (const [who, env] of [["dev-1", A], ["ops-1", B], ["lead-1", L]] as const) {
      const printed = await run("env", [], env);
      const hits = secrets.filter(([, v]) => v && printed.out.includes(v)).map(([k]) => k);
      const extra = ["BEGIN OPENSSH PRIVATE KEY", "-cert-v01@openssh.com", "ghs_", "ghp_", "GH_TOKEN=", "GITHUB_TOKEN=", "SSH_AUTH_SOCK="].filter((p) => printed.out.includes(p));
      note(`${who} $ env：${printed.out.split("\n").filter(Boolean).length} 个变量；命中秘密=${JSON.stringify(hits)}；命中特征=${JSON.stringify(extra)}；AT_* 变量=${printed.out.split("\n").filter((l) => l.startsWith("AT_")).map((l) => l.split("=")[0]).join(",")}`);
      expect(hits).toEqual([]);
      expect(extra).toEqual([]);
    }
    const sessionFiles = walk(sdir);
    note(`dev-1 会话目录 ${sdir}：${sessionFiles.map((f) => f.slice(sdir.length + 1)).join(", ")}（agent.sock 是 socket，不算文件）`);
    note(`ssh_config：\n${readFileSync(A.AT_SSH_CONFIG!, "utf8").trim()}`);
    note(`known_hosts：${readFileSync(join(sdir, "known_hosts"), "utf8").trim()}`);
    note(`平台 bin/ssh：${readFileSync(join(data, "platform", "bin", "ssh"), "utf8").trim().replace(/\n/g, " ⏎ ")}`);
    const all = walk(data);
    const leaked = all.flatMap((f) => {
      let body: Buffer;
      try {
        body = readFileSync(f);
      } catch {
        return [];
      }
      return secrets.filter(([, v]) => v && body.includes(v)).map(([k]) => `${f.slice(data.length + 1)}: ${k}`);
    });
    note(`节点数据目录 ${all.length} 个文件里搜秘密：${leaked.length ? leaked.join("; ") : "无"}`);
    expect(leaked.filter((l) => !l.endsWith("节点令牌"))).toEqual([]);

    // 9. grants change while dev-1 holds a connection: cut within seconds, ssh_config rewritten.
    const long = run("ssh", ["pwbox", "sleep 60; echo late"], A, 70_000);
    await new Promise((r) => setTimeout(r, 1500));
    const t0 = Date.now();
    await grant([key.id]);
    const cut = await long;
    note(`dev-1 正在 ssh pwbox 'sleep 60' 时撤销 pwbox 授权：${((Date.now() - t0) / 1000).toFixed(1)} 秒后断开，${short(cut)}`);
    expect(cut.out).not.toContain("late");
    await waitFor("ssh_config without pwbox", () => !readFileSync(A.AT_SSH_CONFIG!, "utf8").includes("Host pwbox"), 15_000);
    const after = await run("ssh", ["pwbox", "echo ok"], A);
    note(`撤销后 dev-1 $ ssh pwbox → ${short(after)}`);
    expect(after.code).not.toBe(0);
    expect((await run("ssh", ["keybox", "echo still"], A)).out).toBe("still\n");
    await grant([pw.id, key.id]);
    await waitFor("ssh_config with pwbox again", () => readFileSync(A.AT_SSH_CONFIG!, "utf8").includes("Host pwbox"), 15_000);

    // 10. audit.
    const audit = (await api<{ items: Array<{ employee: string; role: string; server_name: string; kind: string; command: string; exit_code: number | null; duration_ms: number | null; reason: string; thread_id: string; session_id: string; detail: Record<string, unknown> }> }>(
      "GET", `/api/projects/${project.id}/server-audit?limit=100`, undefined, 200)).body.items;
    note(`项目访问记录 ${audit.length} 行：\n${audit.map((a) => `  ${a.employee}(${a.role}) ${a.server_name} ${a.kind} ${JSON.stringify(a.command)} exit=${a.exit_code ?? "-"}${a.duration_ms != null ? ` ${a.duration_ms}ms` : ""}${a.reason ? ` reason=${a.reason}` : ""}${Object.keys(a.detail ?? {}).length ? ` detail=${JSON.stringify(a.detail)}` : ""}`).join("\n")}`);
    expect(audit.some((a) => a.employee === "dev-1" && a.kind === "exec" && a.command === "echo ok" && a.exit_code === 0)).toBe(true);
    expect(audit.some((a) => a.kind === "subsystem")).toBe(true);
    expect(audit.some((a) => a.kind === "exec" && a.command.startsWith("rsync --server"))).toBe(true);
    expect(audit.some((a) => a.employee !== "dev-1")).toBe(false);

    // 11. the session ends: directory and agent.sock gone, the old environment no longer works.
    // Worst case for reuse after the end: while the session is alive, take its certificate (ssh-add -L on
    // agent.sock) and even its private key out of the daemon's memory (this test runs the daemon
    // in-process; a real attacker would need a memory dump). The control connects with them straight to
    // the gateway (node token, the session's own headers); after the session ends the same must fail.
    const stolenKey = join(WORK, "stolen_key");
    const certLine = (await run("ssh-add", ["-L"], { ...A, SSH_AUTH_SOCK: join(sdir, "agent.sock") })).out.split("\n").find((l) => l.includes("-cert-v01@openssh.com"))!;
    const hub = (daemon as unknown as { ssh: { sessions: Map<string, { key: { priv: KeyObject } }> } }).ssh;
    writeFileSync(stolenKey, hub.sessions.get(A.AT_CRED_SESSION!)!.key.priv.export({ type: "pkcs8", format: "pem" }) as string, { mode: 0o600 });
    writeFileSync(`${stolenKey}-cert.pub`, certLine + "\n");
    writeFileSync(join(WORK, "stolen_known_hosts"), readFileSync(join(sdir, "known_hosts")));
    const stolen = (label: string) =>
      run("ssh", ["-F", "/dev/null", "-i", stolenKey, "-o", `CertificateFile=${stolenKey}-cert.pub`, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
        "-o", `UserKnownHostsFile=${join(WORK, "stolen_known_hosts")}`, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=agent-teams-gateway",
        "-o", "StrictHostKeyChecking=yes", "-o", "BatchMode=yes", "-o", `ProxyCommand=${process.execPath} ${proxyJs} ${gw} ${node.token} ${A.AT_CRED_SESSION} ${aGen} %n`,
        "-l", "pwbox", "pwbox", "echo ok"], { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: WORK }, 20_000).then((r) => (note(`${label} → ${short(r)}`), r));
    const before = await stolen("会话活着：取出的私钥 + 证书 + dev-1 会话头直连网关（对照组）");
    expect(before.out).toBe("ok\n");

    const held = run("ssh", ["keybox", "sleep 60; echo late"], A, 90_000);
    await new Promise((r) => setTimeout(r, 1500));
    const t1 = Date.now();
    const archived = await api("POST", `/api/threads/${thread.id}/archive`, {});
    note(`归档讨论串：${archived.status}`);
    const heldEnd = await held;
    note(`归档时 dev-1 正连着 keybox（sleep 60）：${((Date.now() - t1) / 1000).toFixed(1)} 秒后断开，${short(heldEnd)}`);
    expect(heldEnd.out).not.toContain("late");
    const closed = await waitFor("dev-1 session directory removed", () => !existsSync(sdir), 60_000).catch(() => false);
    note(`归档讨论串后：会话目录存在=${existsSync(sdir)}，agent.sock 存在=${existsSync(join(sdir, "agent.sock"))}`);
    if (closed) {
      const r = await run("ssh", ["pwbox", "echo ok"], A);
      note(`会话结束后用旧环境 $ ssh pwbox → ${short(r)}`);
      expect(r.code).not.toBe(0);
      const sock = await new Promise<string>((resolve) => {
        const c = connect(join(sdir, "agent.sock"));
        c.on("connect", () => (c.destroy(), resolve("connected")));
        c.on("error", (e) => resolve(e.message));
      });
      note(`会话结束后连 agent.sock：${sock}`);
      expect(sock).not.toBe("connected");
      const reuse = await stolen("会话结束后：同一私钥 + 证书 + 旧会话头直连网关");
      expect(reuse.code).not.toBe(0);
      expect(reuse.out).not.toContain("ok");
    }
    const tail = (await api<{ items: Array<{ employee: string; server_name: string; kind: string; command: string; exit_code: number | null; duration_ms: number | null; reason: string; detail: Record<string, unknown> }> }>(
      "GET", `/api/projects/${project.id}/server-audit?limit=3`, undefined, 200)).body.items;
    note(`归档后最新 3 行访问记录：\n${tail.map((a) => `  ${a.employee} ${a.server_name} ${a.kind} ${JSON.stringify(a.command)} exit=${a.exit_code ?? "-"}${a.duration_ms != null ? ` ${a.duration_ms}ms` : ""}${a.reason ? ` reason=${a.reason}` : ""} detail=${JSON.stringify(a.detail ?? {})}`).join("\n")}`);
    rmSync(join(WORK, "files"), { recursive: true, force: true });
  }, 300_000);
});
