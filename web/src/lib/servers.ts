// Remote servers (api.md §3.9.1, 讨论串 #38): the form rules of the 服务器 settings page, test
// connection results and the access log, as plain functions (tested in servers.test.ts).
// A 服务器 is a remote machine employees reach over SSH through the console; a 节点 is a machine
// that runs agents. The credential is write-only: the page never has it after saving.
import type { GrantableRoleView, ServerTestResult, ServerView, SshAuditView } from "@/api/views";

export type AuthKind = "password" | "private_key";

export const AUTH_LABEL: Record<AuthKind, string> = { password: "密码", private_key: "SSH 私钥" };

export const SERVER_NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;

export interface ServerForm {
  name: string;
  host: string;
  port: number;
  username: string;
  kind: AuthKind;
  password: string;
  privateKey: string;
  passphrase: string;
}

export const emptyServerForm = (): ServerForm => ({ name: "", host: "", port: 22, username: "", kind: "password", password: "", privateKey: "", passphrase: "" });

/** The edit form of a saved server: credential fields empty ("已保存，留空不修改"). */
export const serverForm = (s: ServerView): ServerForm => ({ ...emptyServerForm(), name: s.name, host: s.host, port: s.port, username: s.username, kind: s.auth_kind });

const trimmed = (f: ServerForm) => ({ name: f.name.trim(), host: f.host.trim(), username: f.username.trim() });

/** The credential must be (re)entered: always for a new server; when editing, if host, port, user
 * or the auth kind changed (the server seals the credential for exactly those, AAD). */
export function credentialRequired(saved: ServerView | null, f: ServerForm): boolean {
  if (!saved) return true;
  const t = trimmed(f);
  return t.host !== saved.host || Number(f.port) !== saved.port || t.username !== saved.username || f.kind !== saved.auth_kind;
}

/** Something was typed into the credential fields of the chosen kind. */
export const credentialFilled = (f: ServerForm): boolean => (f.kind === "password" ? f.password !== "" : f.privateKey.trim() !== "");

/** The first problem of the form, or "" when it can be submitted. */
export function serverFormProblem(saved: ServerView | null, f: ServerForm): string {
  const t = trimmed(f);
  if (!SERVER_NAME.test(t.name)) return "名称只能用小写字母、数字和 -，以字母或数字开头（会话里用 ssh 名称 连接）";
  if (!t.host) return "请填写主机";
  if (/\s/.test(t.host) || t.host.startsWith("-")) return "主机格式不对";
  const port = Number(f.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return "端口要在 1–65535 之间";
  if (!t.username) return "请填写用户名";
  if (credentialRequired(saved, f) && !credentialFilled(f)) {
    if (!saved) return f.kind === "password" ? "请填写密码" : "请粘贴私钥";
    return "改了主机、端口、用户或认证方式，要重新填写凭证";
  }
  return "";
}

type Auth = { kind: AuthKind; password?: string; private_key?: string; passphrase?: string };

function auth(f: ServerForm): Auth {
  if (f.kind === "password") return { kind: "password", password: f.password };
  return { kind: "private_key", private_key: f.privateKey, ...(f.passphrase ? { passphrase: f.passphrase } : {}) };
}

/** POST /api/servers body. */
export function createBody(f: ServerForm) {
  const t = trimmed(f);
  return { name: t.name, host: t.host, port: Number(f.port), username: t.username, auth: auth(f) };
}

/** PATCH /api/servers/{id} body: only what changed; the credential when it was typed or is required. */
export function patchBody(saved: ServerView, f: ServerForm) {
  const t = trimmed(f);
  const out: { name?: string; host?: string; port?: number; username?: string; auth?: Auth } = {};
  if (t.name !== saved.name) out.name = t.name;
  if (t.host !== saved.host) out.host = t.host;
  if (Number(f.port) !== saved.port) out.port = Number(f.port);
  if (t.username !== saved.username) out.username = t.username;
  if (credentialFilled(f) || credentialRequired(saved, f)) out.auth = auth(f);
  return out;
}

export const endpoint = (s: Pick<ServerView, "host" | "port" | "username">): string => `${s.username}@${s.host}${s.port === 22 ? "" : `:${s.port}`}`;

// ---- test connection --------------------------------------------------------------------

export type Tone = "ok" | "warn" | "danger";

export interface TestView {
  tone: Tone;
  title: string;
  detail: string;
  /** A host key to show prominently (type + fingerprint) and have the user check. */
  hostKey?: { type: string; fingerprint: string; pinnedNow: boolean };
  /** host_key_mismatch: the fingerprint that was pinned before. */
  expected?: string;
  /** Offer “重置主机密钥”. */
  canReset: boolean;
}

const ERROR_TITLE: Record<string, string> = {
  host_key_mismatch: "主机密钥不一致，已拒绝连接",
  host_key_invalid: "已固定的主机密钥校验不通过",
  auth_failed: "认证失败",
  address_blocked: "地址不允许",
  resolve_failed: "主机名解析不到",
  connect_failed: "连不上",
  timeout: "连接超时",
  handshake_failed: "SSH 握手失败",
  credential_unreadable: "保存的凭证解不开",
};

/** How a test connection result is shown. */
export function testView(r: ServerTestResult): TestView {
  const hostKey = r.host_key_fp ? { type: r.host_key_type ?? "", fingerprint: r.host_key_fp, pinnedNow: r.host_key_pinned } : undefined;
  if (r.ok) {
    return {
      tone: "ok",
      title: `连接成功 · ${r.latency_ms} ms`,
      detail: r.host_key_pinned ? "第一次连接：已把下面的主机密钥固定下来，以后指纹不一致会拒绝连接。请到服务器上核对指纹。" : "主机密钥与已固定的一致，凭证登录成功。",
      hostKey,
      canReset: false,
    };
  }
  const error = r.error ?? "";
  const mismatch = error === "host_key_mismatch";
  return {
    tone: mismatch || error === "host_key_invalid" ? "danger" : "warn",
    title: ERROR_TITLE[error] ?? "测试失败",
    detail: r.message ?? "",
    hostKey,
    expected: mismatch ? r.expected_host_key_fp : undefined,
    canReset: mismatch || error === "host_key_invalid",
  };
}

/** The command that prints the host key fingerprint on the server, for the user to compare. */
export function fingerprintCommand(type: string): string {
  const file = { "ssh-ed25519": "ed25519", "ecdsa-sha2-nistp256": "ecdsa", "ssh-rsa": "rsa", "rsa-sha2-512": "rsa", "rsa-sha2-256": "rsa" }[type] ?? "ed25519";
  return `ssh-keygen -lf /etc/ssh/ssh_host_${file}_key.pub`;
}

// ---- project grants ---------------------------------------------------------------------

export const ROLE_REASON: Record<string, string> = {
  coordinator: "统筹不拿服务器凭证，也不直接操作服务器",
  lead: "组长是只读会话，不能执行 ssh",
  read_only: "只读岗位（审查 / 决策）的会话不能执行 ssh",
};

export const roleReason = (r: Pick<GrantableRoleView, "grantable" | "reason">): string => (r.grantable ? "" : (ROLE_REASON[r.reason ?? ""] ?? "这个岗位不能使用服务器"));

export interface Grant {
  server_id: string;
  roles: string[];
}

/** The PUT body after ticking a server on or off. */
export function toggleServer(list: Grant[], serverId: string, on: boolean): Grant[] {
  const rest = list.filter((g) => g.server_id !== serverId);
  return on ? [...rest, { server_id: serverId, roles: list.find((g) => g.server_id === serverId)?.roles ?? [] }] : rest;
}

/** The PUT body after ticking a role of a server on or off. */
export function toggleRole(list: Grant[], serverId: string, role: string, on: boolean): Grant[] {
  return list.map((g) => {
    if (g.server_id !== serverId) return g;
    const roles = g.roles.filter((r) => r !== role);
    return { ...g, roles: on ? [...roles, role].sort() : roles };
  });
}

// ---- access log -------------------------------------------------------------------------

export const AUDIT_KINDS: { value: string; label: string }[] = [
  { value: "exec", label: "执行命令" },
  { value: "shell", label: "交互 shell" },
  { value: "subsystem", label: "文件传输" },
  { value: "connect", label: "连接" },
  { value: "disconnect", label: "断开" },
  { value: "denied", label: "被拒绝" },
  { value: "auth_fail", label: "认证失败" },
  { value: "host_key_mismatch", label: "主机密钥不符" },
  { value: "test", label: "测试连接" },
];

export const auditKindLabel = (kind: string): string => AUDIT_KINDS.find((k) => k.value === kind)?.label ?? kind;

/** The result column of an access log row. */
export function auditResult(a: Pick<SshAuditView, "kind" | "exit_code" | "duration_ms" | "reason">): { text: string; tone: Tone | "muted" } {
  const took = a.duration_ms != null ? ` · ${a.duration_ms < 1000 ? `${a.duration_ms} ms` : `${(a.duration_ms / 1000).toFixed(1)} s`}` : "";
  if (["denied", "auth_fail", "host_key_mismatch"].includes(a.kind)) return { text: a.reason || auditKindLabel(a.kind), tone: "danger" };
  if (a.kind === "test") return a.exit_code === 0 ? { text: `成功${took}`, tone: "ok" } : { text: `失败${a.reason ? `（${a.reason}）` : ""}${took}`, tone: "warn" };
  if (a.exit_code == null) return { text: took ? took.slice(3) : "—", tone: "muted" };
  return { text: `退出码 ${a.exit_code}${took}`, tone: a.exit_code === 0 ? "ok" : "warn" };
}

/** Who did it: the employee (and role), or 你 for the user's own test connections. */
export const auditWho = (a: Pick<SshAuditView, "employee" | "role">): string => (a.employee === "user" ? "你" : a.role ? `${a.employee}（${a.role}）` : a.employee || "—");
