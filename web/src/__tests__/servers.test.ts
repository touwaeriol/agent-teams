// Remote servers (讨论串 #38): form rules, test connection results, grants and the access log.
import { describe, expect, it } from "vitest";
import type { ServerView } from "@/api/views";
import {
  auditResult, auditWho, createBody, credentialRequired, emptyServerForm, endpoint, fingerprintCommand, patchBody, roleReason,
  serverForm, serverFormProblem, testView, toggleRole, toggleServer,
} from "@/lib/servers";

const saved: ServerView = {
  id: "srv_1", name: "prod", host: "203.0.113.10", port: 22, username: "deploy", auth_kind: "password", has_passphrase: false,
  key_fingerprint: "", host_key_fp: "SHA256:abc", host_key_confirmed_at: null, cred_updated_at: "", created_at: "", updated_at: "", projects: [],
};

describe("server form", () => {
  it("needs a credential for a new server and after host, port, user or kind change", () => {
    expect(credentialRequired(null, emptyServerForm())).toBe(true);
    const f = serverForm(saved);
    expect(credentialRequired(saved, f)).toBe(false);
    expect(credentialRequired(saved, { ...f, name: "prod2" })).toBe(false);
    expect(credentialRequired(saved, { ...f, host: " 203.0.113.10 " })).toBe(false);
    for (const change of [{ host: "h2" }, { port: 2222 }, { username: "root" }, { kind: "private_key" as const }]) {
      expect(credentialRequired(saved, { ...f, ...change })).toBe(true);
    }
  });
  it("reports the first problem", () => {
    const ok = { ...emptyServerForm(), name: "prod", host: "h", username: "u", password: "pw" };
    expect(serverFormProblem(null, ok)).toBe("");
    expect(serverFormProblem(null, { ...ok, name: "Prod" })).toContain("小写字母");
    expect(serverFormProblem(null, { ...ok, host: "-oProxyCommand=x" })).toBe("主机格式不对");
    expect(serverFormProblem(null, { ...ok, port: 70000 })).toContain("1–65535");
    expect(serverFormProblem(null, { ...ok, password: "" })).toBe("请填写密码");
    expect(serverFormProblem(null, { ...ok, kind: "private_key", password: "" })).toBe("请粘贴私钥");
    // Editing: blank credential keeps the saved one, unless a bound field changed.
    const f = serverForm(saved);
    expect(serverFormProblem(saved, f)).toBe("");
    expect(serverFormProblem(saved, { ...f, port: 2200 })).toContain("重新填写凭证");
    expect(serverFormProblem(saved, { ...f, port: 2200, password: "new" })).toBe("");
  });
  it("builds create and patch bodies without sending what did not change", () => {
    const f = { ...emptyServerForm(), name: " prod ", host: "h ", username: "u", kind: "private_key" as const, privateKey: "KEY", passphrase: "pp" };
    expect(createBody(f)).toEqual({ name: "prod", host: "h", port: 22, username: "u", auth: { kind: "private_key", private_key: "KEY", passphrase: "pp" } });
    const e = serverForm(saved);
    expect(patchBody(saved, e)).toEqual({});
    expect(patchBody(saved, { ...e, name: "prod2" })).toEqual({ name: "prod2" });
    expect(patchBody(saved, { ...e, password: "new" })).toEqual({ auth: { kind: "password", password: "new" } });
    expect(patchBody(saved, { ...e, host: "h2", password: "x" })).toEqual({ host: "h2", auth: { kind: "password", password: "x" } });
    expect(endpoint(saved)).toBe("deploy@203.0.113.10");
    expect(endpoint({ ...saved, port: 2222 })).toBe("deploy@203.0.113.10:2222");
  });
});

describe("test connection", () => {
  it("shows the pinned host key on the first success", () => {
    const v = testView({ ok: true, host_key_pinned: true, host_key_fp: "SHA256:new", host_key_type: "ssh-ed25519", latency_ms: 120 });
    expect(v).toMatchObject({ tone: "ok", canReset: false, hostKey: { fingerprint: "SHA256:new", pinnedNow: true } });
    expect(v.title).toContain("120 ms");
    expect(v.detail).toContain("核对");
  });
  it("offers reset on a mismatch and shows both fingerprints", () => {
    const v = testView({ ok: false, error: "host_key_mismatch", message: "m", host_key_fp: "SHA256:seen", expected_host_key_fp: "SHA256:old", host_key_type: "ssh-ed25519", host_key_pinned: false, latency_ms: 1 });
    expect(v).toMatchObject({ tone: "danger", canReset: true, expected: "SHA256:old", hostKey: { fingerprint: "SHA256:seen" } });
    expect(testView({ ok: false, error: "auth_failed", host_key_pinned: true, latency_ms: 1 })).toMatchObject({ tone: "warn", canReset: false, title: "认证失败" });
    expect(testView({ ok: false, error: "host_key_invalid", host_key_pinned: false, latency_ms: 1 }).canReset).toBe(true);
  });
  it("names the host key file to check", () => {
    expect(fingerprintCommand("ssh-ed25519")).toBe("ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub");
    expect(fingerprintCommand("rsa-sha2-512")).toContain("ssh_host_rsa_key");
  });
});

describe("grants", () => {
  it("toggles servers and roles", () => {
    let g = toggleServer([], "a", true);
    g = toggleRole(g, "a", "qa", true);
    g = toggleRole(g, "a", "dev", true);
    expect(g).toEqual([{ server_id: "a", roles: ["dev", "qa"] }]);
    expect(toggleRole(g, "a", "qa", false)).toEqual([{ server_id: "a", roles: ["dev"] }]);
    expect(toggleServer(g, "a", false)).toEqual([]);
    expect(roleReason({ grantable: false, reason: "lead" })).toContain("只读");
    expect(roleReason({ grantable: false, reason: "coordinator" })).toContain("凭证");
    expect(roleReason({ grantable: true })).toBe("");
  });
});

describe("access log", () => {
  it("formats results and who", () => {
    expect(auditResult({ kind: "exec", exit_code: 0, duration_ms: 12400, reason: "" })).toEqual({ text: "退出码 0 · 12.4 s", tone: "ok" });
    expect(auditResult({ kind: "exec", exit_code: 7, duration_ms: 300, reason: "" })).toEqual({ text: "退出码 7 · 300 ms", tone: "warn" });
    expect(auditResult({ kind: "denied", exit_code: null, duration_ms: null, reason: "岗位没有授权" }).tone).toBe("danger");
    expect(auditResult({ kind: "test", exit_code: 1, duration_ms: null, reason: "auth_failed" }).text).toBe("失败（auth_failed）");
    expect(auditWho({ employee: "user", role: "" })).toBe("你");
    expect(auditWho({ employee: "dev-1", role: "dev" })).toBe("dev-1（dev）");
  });
});
