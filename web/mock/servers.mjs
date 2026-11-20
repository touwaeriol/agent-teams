// Remote servers for the mock API (api.md §3.9.1, 讨论串 #38): the user's servers, a project's
// servers and role grants, test connection with host key pinning, and the access log. Kept apart
// from server.mjs so threads editing the mock do not collide. Never returns a credential.
//
// Test connection outcomes are picked by the server's name, so every state can be shown:
//   *-badauth → auth_failed (still pins the host key), *-down → connect_failed,
//   *-changed → the target's host key differs from any pin (host_key_mismatch once pinned).

const ago = (min) => new Date(Date.now() - min * 60_000).toISOString();
const NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;
const FP = {
  prod: "SHA256:k3Vq8mW0pXbJ2r7c9eYtN5uLhA1dGfZsQ4oI6vRwE0M",
  staging: "SHA256:Zt9cN2wQ5mR8xV1bJ7kH4pL0aE3sD6fG9uY2iO5nM8c",
  changed: "SHA256:Qm7aX2cV9bN4kL1jH8gF5dS3aP0oI7uY6tR4eW2qZ9x",
};
const hostKeyFor = (s) => (s.name.endsWith("-changed") ? FP.changed : (FP[s.name] ?? `SHA256:${Buffer.from(s.name.padEnd(32, "x")).toString("base64").slice(0, 43)}`));

function server(id, name, host, port, username, auth_kind, extra = {}) {
  return { id, name, host, port, username, auth_kind, has_passphrase: false, key_fingerprint: "", host_key_fp: "", host_key_confirmed_at: null,
    cred_updated_at: ago(60 * 24 * 12), created_at: ago(60 * 24 * 30), updated_at: ago(60 * 24 * 3), ...extra };
}

export function serverState() {
  return {
    servers: [
      server("srv_prod", "prod", "203.0.113.10", 22, "deploy", "private_key", { has_passphrase: true, key_fingerprint: "SHA256:8bL2fP0xQm1vC4kZ7nR3tY6uI9oE5wA2sD1gH0jK3lM",
        host_key_fp: FP.prod, host_key_confirmed_at: ago(60 * 24 * 20) }),
      server("srv_staging", "staging", "staging.example.com", 2222, "ubuntu", "password", { host_key_fp: FP.staging, host_key_confirmed_at: ago(60 * 24 * 2) }),
      server("srv_db", "db-backup", "198.51.100.7", 22, "backup", "password"),
    ],
    projectServers: { p1: [{ server_id: "srv_prod", roles: ["dev"] }, { server_id: "srv_staging", roles: ["dev", "qa"] }] },
    audit: [
      ...[
        ["exec", "dev-1", "dev", "srv_prod", "prod", "docker compose pull && docker compose up -d", 0, 12_400, 12],
        ["exec", "dev-2", "dev", "srv_staging", "staging", "systemctl status agent-teams --no-pager", 0, 380, 30],
        ["denied", "qa-1", "qa", "srv_prod", "prod", "", null, null, 45, "岗位没有被授权使用这台服务器"],
        ["exec", "dev-1", "dev", "srv_prod", "prod", "tail -n 200 /var/log/agent-teams/console.log", 0, 640, 80],
        ["connect", "dev-1", "dev", "srv_prod", "prod", "", null, null, 82],
        ["exec", "qa-1", "qa", "srv_staging", "staging", "curl -fsS http://127.0.0.1:8080/healthz", 7, 2_010, 120, ""],
        ["host_key_mismatch", "dev-2", "dev", "srv_staging", "staging", "", null, null, 600, "目标主机密钥和已固定的不一致"],
        ["subsystem", "dev-1", "dev", "srv_prod", "prod", "sftp", 0, 5_300, 700],
      ].map(([kind, employee, role, server_id, server_name, command, exit_code, duration_ms, min, reason = ""], i) => ({
        id: 900 - i, created_at: ago(min), project_id: "p1", thread_id: i % 2 ? "t13" : "t12", session_id: `s_0${i}`, employee_id: `e_${employee}`, employee, role,
        node_id: "n1", server_id, server_name, cert_serial: 40 + i, conn_id: `sc_${i}`, kind, command, command_truncated: false, exit_code, duration_ms,
        bytes_in: kind === "exec" ? 320 : 0, bytes_out: kind === "exec" ? 18_200 : 0, reason, detail: {},
      })),
      // Written when the command started, not completed yet (still running, or the console restarted mid-command).
      { id: 901, created_at: ago(3), project_id: "p1", thread_id: "t12", session_id: "s_run", employee_id: "e_dev-1", employee: "dev-1", role: "dev",
        node_id: "n1", server_id: "srv_prod", server_name: "prod", cert_serial: 39, conn_id: "sc_run", kind: "exec", command: "journalctl -u agent-teams -f",
        command_truncated: false, exit_code: null, duration_ms: null, bytes_in: 0, bytes_out: 0, reason: "", detail: { state: "running" } },
      { id: 800, created_at: ago(60 * 24 * 2), project_id: "", thread_id: "", session_id: "", employee_id: "", employee: "user", role: "", node_id: "",
        server_id: "srv_staging", server_name: "staging", cert_serial: null, conn_id: "", kind: "test", command: "", command_truncated: false, exit_code: 0,
        duration_ms: 210, bytes_in: 0, bytes_out: 0, reason: "", detail: { host_key_fp: FP.staging, pinned: true } },
    ],
  };
}

export function serverRoutes(state, { json, fail, body, roles, projects }) {
  const S = (state.remote ??= serverState());
  const now = () => new Date().toISOString();
  const used = (id) => Object.entries(S.projectServers).filter(([, list]) => list.some((x) => x.server_id === id))
    .map(([pid]) => ({ id: pid, name: projects().find((p) => p.id === pid)?.name ?? pid }));
  const view = (s) => ({ ...s, projects: used(s.id) });
  const find = (id) => S.servers.find((s) => s.id === id);
  const validate = (res, b, partial) => {
    if ((!partial || b.name !== undefined) && !NAME.test(b.name ?? "")) {
      json(res, 422, { detail: "名字只能用小写字母、数字和 -，以字母或数字开头，最多 63 个字符", code: "validation", fields: [{ path: "body.name", message: "名字只能用小写字母、数字和 -，以字母或数字开头，最多 63 个字符" }] });
      return false;
    }
    if (b.host !== undefined && /^(127\.|169\.254\.|localhost$|0\.0\.0\.0$)/.test(b.host)) {
      fail(res, 422, `地址不允许：${b.host} 是回环或链路本地地址；需要的话请管理员在 ssh_allowed_private_cidrs 里放行`, "address_blocked");
      return false;
    }
    if (b.host !== undefined && b.host.endsWith(".invalid")) {
      fail(res, 422, `主机名解析不到地址：${b.host}`, "resolve_failed");
      return false;
    }
    const a = b.auth;
    if (a && ((a.kind === "password" && !a.password) || (a.kind === "private_key" && !a.private_key))) {
      const msg = a.kind === "password" ? "密码认证只填密码" : "私钥认证要填私钥";
      json(res, 422, { detail: msg, code: "validation", fields: [{ path: "body.auth", message: msg }] });
      return false;
    }
    if (a?.kind === "private_key" && /ENCRYPTED|pp/.test(a.private_key) && !a.passphrase) {
      json(res, 422, { detail: "私钥有口令，请一并填写", code: "validation", fields: [{ path: "body.auth", message: "私钥有口令，请一并填写" }] });
      return false;
    }
    return true;
  };
  const credFields = (a) => (a.kind === "private_key"
    ? { auth_kind: "private_key", has_passphrase: Boolean(a.passphrase), key_fingerprint: "SHA256:Rk2mW9xQ0pL4vC7nB1zT5yU8iO3eA6sD9fG2hJ5kL8m" }
    : { auth_kind: "password", has_passphrase: false, key_fingerprint: "" });
  const projectView = (pid) => ({
    servers: (S.projectServers[pid] ?? []).map((x) => ({ server: view(find(x.server_id)), roles: [...x.roles].sort() }))
      .filter((x) => x.server).sort((a, b) => a.server.name.localeCompare(b.server.name)),
    available: S.servers.map(view).sort((a, b) => a.name.localeCompare(b.name)),
    roles: roles().map((r) => {
      const reason = r.tier === "coordinator" ? "coordinator" : r.tier === "lead" ? "lead" : r.read_only || ["review", "decision"].includes(r.gate) ? "read_only" : "";
      return { role: r.role, title: r.title, tier: r.tier, grantable: !reason, ...(reason ? { reason } : {}) };
    }),
  });
  const auditPage = (rows, url) => {
    const kind = url.searchParams.get("kind");
    const emp = url.searchParams.get("employee");
    const limit = Number(url.searchParams.get("limit") || 50);
    const after = Number(url.searchParams.get("cursor") || Infinity);
    const list = rows.filter((a) => (!kind || a.kind === kind) && (!emp || a.employee === emp) && a.id < after).sort((a, b) => b.id - a.id);
    const items = list.slice(0, limit);
    return { items, next_cursor: list.length > limit ? String(items.at(-1).id) : null };
  };

  return [
    ["GET", /^\/api\/servers$/, (req, res) => json(res, 200, S.servers.map(view).sort((a, b) => a.name.localeCompare(b.name)))],
    ["POST", /^\/api\/servers$/, async (req, res) => {
      const b = await body(req);
      if (!validate(res, b, false)) return;
      if (S.servers.some((s) => s.name === b.name)) return fail(res, 409, "已经有同名的服务器了", "server_name_taken");
      const s = server(`srv_${Date.now()}`, b.name, b.host, b.port || 22, b.username, b.auth.kind, { ...credFields(b.auth), cred_updated_at: now(), created_at: now(), updated_at: now() });
      S.servers.push(s);
      json(res, 201, view(s));
    }],
    ["GET", /^\/api\/servers\/([^/]+)$/, (req, res, [id]) => (find(id) ? json(res, 200, view(find(id))) : fail(res, 404, "没有这台服务器", "not_found"))],
    ["PATCH", /^\/api\/servers\/([^/]+)$/, async (req, res, [id]) => {
      const s = find(id);
      if (!s) return fail(res, 404, "没有这台服务器", "not_found");
      const b = await body(req);
      if (!validate(res, b, true)) return;
      const bound = ["host", "port", "username"].some((k) => b[k] !== undefined && b[k] !== s[k]) || (b.auth && b.auth.kind !== s.auth_kind);
      if (bound && !b.auth) return fail(res, 422, "改了主机、端口、用户名或认证方式，必须重新填写凭证", "credential_required");
      if (b.name && b.name !== s.name && S.servers.some((x) => x.name === b.name)) return fail(res, 409, "已经有同名的服务器了", "server_name_taken");
      const moved = (b.host !== undefined && b.host !== s.host) || (b.port !== undefined && b.port !== s.port);
      for (const k of ["name", "host", "port", "username"]) if (b[k] !== undefined) s[k] = b[k];
      if (b.auth) Object.assign(s, credFields(b.auth), { cred_updated_at: now() });
      if (moved) Object.assign(s, { host_key_fp: "", host_key_confirmed_at: null });
      s.updated_at = now();
      json(res, 200, view(s));
    }],
    ["DELETE", /^\/api\/servers\/([^/]+)$/, (req, res, [id]) => {
      S.servers = S.servers.filter((s) => s.id !== id);
      for (const pid of Object.keys(S.projectServers)) S.projectServers[pid] = S.projectServers[pid].filter((x) => x.server_id !== id);
      json(res, 204);
    }],
    ["POST", /^\/api\/servers\/([^/]+)\/test$/, async (req, res, [id]) => {
      const s = find(id);
      if (!s) return fail(res, 404, "没有这台服务器", "not_found");
      await new Promise((r) => setTimeout(r, Number(process.env.MOCK_TEST_MS ?? 400)));
      const seen = hostKeyFor(s);
      const base = { host_key_fp: seen, host_key_type: "ssh-ed25519", host_key_pinned: false, latency_ms: 140 + Math.round(Math.random() * 60) };
      let out;
      if (s.name.endsWith("-down")) out = { ok: false, error: "connect_failed", message: `连不上 ${s.host}，请检查主机、端口和防火墙`, latency_ms: 3000, host_key_pinned: false };
      else {
        if (!s.host_key_fp) {
          Object.assign(s, { host_key_fp: seen, host_key_confirmed_at: now() });
          base.host_key_pinned = true;
        }
        if (s.host_key_fp !== seen) out = { ...base, ok: false, error: "host_key_mismatch", expected_host_key_fp: s.host_key_fp,
          message: "目标服务器的主机密钥和已固定的不一样，没有发送凭证。请核实服务器是否重装或被冒充；确认无误后重置主机密钥再测试" };
        else if (s.name.endsWith("-badauth")) out = { ...base, ok: false, error: "auth_failed", message: "认证失败：密码或私钥不对，或用户不存在" };
        else out = { ...base, ok: true };
      }
      S.audit.push({ id: Math.max(...S.audit.map((a) => a.id)) + 1, created_at: now(), project_id: "", thread_id: "", session_id: "", employee_id: "", employee: "user", role: "",
        node_id: "", server_id: s.id, server_name: s.name, cert_serial: null, conn_id: "", kind: "test", command: "", command_truncated: false, exit_code: out.ok ? 0 : 1,
        duration_ms: out.latency_ms, bytes_in: 0, bytes_out: 0, reason: out.error ?? "", detail: { host_key_fp: out.host_key_fp ?? "", pinned: out.host_key_pinned } });
      json(res, 200, out);
    }],
    ["POST", /^\/api\/servers\/([^/]+)\/host-key\/reset$/, (req, res, [id]) => {
      const s = find(id);
      if (!s) return fail(res, 404, "没有这台服务器", "not_found");
      Object.assign(s, { host_key_fp: "", host_key_confirmed_at: null, updated_at: now() });
      json(res, 200, view(s));
    }],
    ["GET", /^\/api\/servers\/([^/]+)\/audit$/, (req, res, [id], url) => json(res, 200, auditPage(S.audit.filter((a) => a.server_id === id), url))],
    ["GET", /^\/api\/projects\/([^/]+)\/servers$/, (req, res, [pid]) => json(res, 200, projectView(pid))],
    ["PUT", /^\/api\/projects\/([^/]+)\/servers$/, async (req, res, [pid]) => {
      const b = await body(req);
      const view0 = projectView(pid);
      for (const x of b.servers ?? []) {
        if (!find(x.server_id)) return fail(res, 404, "没有这台服务器", "not_found");
        for (const role of x.roles ?? []) {
          const r = view0.roles.find((y) => y.role === role);
          if (!r || !r.grantable) return fail(res, 422, `这个岗位不能使用远程服务器：${role}`, "role_not_grantable");
        }
      }
      S.projectServers[pid] = (b.servers ?? []).map((x) => ({ server_id: x.server_id, roles: [...new Set(x.roles ?? [])] }));
      json(res, 200, projectView(pid));
    }],
    ["GET", /^\/api\/projects\/([^/]+)\/server-audit$/, (req, res, [pid], url) => {
      const sid = url.searchParams.get("server_id");
      const tid = url.searchParams.get("thread_id");
      json(res, 200, auditPage(S.audit.filter((a) => a.project_id === pid && (!sid || a.server_id === sid) && (!tid || a.thread_id === tid)), url));
    }],
  ];
}
