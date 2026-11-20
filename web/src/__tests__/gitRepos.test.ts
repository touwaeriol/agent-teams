import { describe, expect, it } from "vitest";
import type { GitCredentialView, GitTestResult } from "@/api/views";
import { accessText, credentialsFor, daysLeft, expiryText, parseRepoURL, repoTitle, testSummary } from "@/lib/gitRepos";

describe("parseRepoURL", () => {
  it("reads HTTPS, ssh:// and scp-style URLs and the known hosts", () => {
    expect(parseRepoURL("https://gitlab.com/group/sub/project.git")).toEqual({ ok: true, transport: "https", host: "gitlab.com", path: "group/sub/project", provider: "gitlab", username: "oauth2" });
    expect(parseRepoURL("git@codeberg.org:me/app.git")).toMatchObject({ ok: true, transport: "ssh", host: "codeberg.org", path: "me/app", provider: "gitea", username: "git" });
    expect(parseRepoURL("ssh://deploy@git.example.com:2222/infra/deploy.git")).toMatchObject({ transport: "ssh", host: "git.example.com:2222", username: "deploy", provider: "generic" });
    expect(parseRepoURL("ssh://git@git.example.com:22/a/b")).toMatchObject({ host: "git.example.com" });
    expect(parseRepoURL("https://git.example.com:443/a/b")).toMatchObject({ host: "git.example.com" });
  });
  it("explains what it refuses", () => {
    expect(parseRepoURL("")).toEqual({ ok: false });
    expect(parseRepoURL("https://oauth2:glpat-x@gitlab.com/a/b.git").problem).toContain("不要带令牌");
    expect(parseRepoURL("http://git.example.com/a/b").problem).toContain("https");
    expect(parseRepoURL("file:///srv/a.git").problem).toContain("HTTPS 和 SSH");
    expect(parseRepoURL("https://gitlab.com/onlyone").problem).toContain("两段");
    expect(parseRepoURL("not a url").ok).toBe(false);
  });
});

describe("git repo helpers", () => {
  const now = Date.parse("2026-11-20T00:00:00Z");
  const cred = (over: Partial<GitCredentialView>) => ({ id: "c", host: "gitlab.com", kind: "https_token", expires_at: null, ...over }) as GitCredentialView;
  it("counts days to expiry and labels them", () => {
    expect(daysLeft(null, now)).toBeNull();
    expect(daysLeft("2026-11-29T00:00:00Z", now)).toBe(9);
    expect(expiryText(cred({ expires_at: "2026-11-29T00:00:00Z" }), now)).toBe("9 天后到期");
    expect(expiryText(cred({ expires_at: "2026-11-01T00:00:00Z" }), now)).toBe("已过期");
    expect(expiryText(cred({}), now)).toBe("不过期");
    expect(expiryText(cred({ kind: "ssh_key" }), now)).toBe("");
  });
  it("offers only credentials of the host and the transport's kind", () => {
    const list = [cred({ id: "a" }), cred({ id: "b", kind: "ssh_key" }), cred({ id: "c", host: "other" })];
    expect(credentialsFor(list, "gitlab.com", "https").map((c) => c.id)).toEqual(["a"]);
    expect(credentialsFor(list, "gitlab.com", "ssh").map((c) => c.id)).toEqual(["b"]);
  });
  it("summarises tests and access", () => {
    expect(testSummary({ ok: true, access: "write", default_branch: "develop", branches: 3 } as GitTestResult)).toBe("连接成功 · 可读写 · 默认分支 develop · 3 个分支");
    expect(testSummary({ ok: false, message: "认证失败" } as GitTestResult)).toBe("认证失败");
    expect(accessText({ access: "", last_error: "", credential_id: "c" })).toEqual({ text: "还没测试", tone: "warn" });
    expect(accessText({ access: "read", last_error: "", credential_id: null }).text).toBe("公开仓库，只读");
    expect(accessText({ access: "write", last_error: "超时", credential_id: "c" })).toEqual({ text: "超时", tone: "bad" });
    expect(repoTitle({ name: "", slug: "gitlab.com/a/web" })).toBe("web");
  });
});
