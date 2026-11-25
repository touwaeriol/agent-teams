// Standalone Git repositories (docs/v2/git-repos.md): what the 添加仓库 dialog infers from a pasted
// URL, labels, credential expiry and test results, as plain functions (tested in gitRepos.test.ts).
// Credentials are write-only: the page never has a token or private key after saving.
import type { GitCredentialView, GitRepoView, GitTestResult } from "@/api/views";

export type Transport = "https" | "ssh";
export type CredKind = "https_token" | "ssh_key";
export type Provider = GitRepoView["provider"];

export const PROVIDER_LABEL: Record<Provider, string> = { github: "GitHub", gitlab: "GitLab", gitea: "Gitea", bitbucket: "Bitbucket", generic: "自建 Git" };
export const CRED_KIND_LABEL: Record<CredKind, string> = { https_token: "HTTPS 令牌", ssh_key: "SSH 私钥" };

/** What a pasted URL says (the server parses it again and decides). */
export interface ParsedURL {
  ok: boolean;
  transport?: Transport;
  host?: string; // host[:port], the port only when not the default
  path?: string;
  provider?: Provider;
  username?: string;
  problem?: string;
}

const KNOWN: Record<string, { provider: Provider; user: string }> = {
  "github.com": { provider: "github", user: "x-access-token" },
  "gitlab.com": { provider: "gitlab", user: "oauth2" },
  "bitbucket.org": { provider: "bitbucket", user: "x-token-auth" },
  "gitea.com": { provider: "gitea", user: "" },
  "codeberg.org": { provider: "gitea", user: "" },
};

export function parseRepoURL(text: string): ParsedURL {
  const t = text.trim();
  if (!t) return { ok: false };
  if (/^https?:\/\/[^/]*@/.test(t)) return { ok: false, problem: "地址里不要带令牌，请填到下面的“令牌”框里" };
  if (/^http:\/\//.test(t)) return { ok: false, problem: "只支持 https://（http 会明文传输凭据）" };
  if (/^(file|git):\/\//.test(t)) return { ok: false, problem: "只支持 HTTPS 和 SSH 地址" };
  const scp = t.match(/^([\w.-]+)@([\w.-]+):(?!\/)(.+?)(\.git)?\/?$/);
  const url = t.match(/^(https|ssh):\/\/(?:([\w.-]+)@)?([\w.-]+(?::\d+)?)\/(.+?)(\.git)?\/?$/);
  const parts = scp
    ? { transport: "ssh" as const, user: scp[1], host: scp[2], path: scp[3] }
    : url
      ? { transport: url[1] === "https" ? ("https" as const) : ("ssh" as const), user: url[2], host: url[3], path: url[4] }
      : null;
  if (!parts) return { ok: false, problem: "看不懂这个地址：请贴 https://… 或 git@…:… 形式的克隆地址" };
  if (!parts.path.includes("/")) return { ok: false, problem: "仓库路径至少要有两段，例如 group/project" };
  const host = parts.host.toLowerCase().replace(parts.transport === "https" ? /:443$/ : /:22$/, "");
  const known = KNOWN[host];
  const username = parts.transport === "ssh" ? parts.user || "git" : (known?.user ?? "");
  return { ok: true, transport: parts.transport, host, path: parts.path, provider: known?.provider ?? "generic", username };
}

/**
 * The risk of a standalone repository credential (audit #55 M7): the platform decrypts it and hands
 * it to the node running the project's sessions and to the AI sessions' git (an SSH key loses its
 * passphrase first). Shown wherever a token or SSH key is entered or picked for a repository.
 */
export const CREDENTIAL_WARNING =
  "这个凭据会解密后交给运行本项目会话的节点和 AI 会话，用于 git 操作（SSH 私钥会去掉口令后下发）。请使用只读部署密钥，或只能访问这一个仓库、设置了过期时间的令牌；需要推送时，只给这一个仓库的部署密钥开写权限。";

/** The repository's display title: its name, else the last path segment. */
export const repoTitle = (r: Pick<GitRepoView, "name" | "slug">) => r.name || r.slug.split("/").pop() || r.slug;

/** Whole days until an expiry (negative once past); null without one. */
export function daysLeft(iso: string | null | undefined, now = Date.now()): number | null {
  if (!iso) return null;
  return Math.ceil((new Date(iso).getTime() - now) / 86_400_000);
}

/** "9 天后到期" / "已过期" / "不过期" for a token credential. */
export function expiryText(c: Pick<GitCredentialView, "kind" | "expires_at">, now = Date.now()): string {
  if (c.kind !== "https_token") return "";
  const d = daysLeft(c.expires_at, now);
  if (d === null) return "不过期";
  return d <= 0 ? "已过期" : `${d} 天后到期`;
}

/** Credentials a repository at host may use (same host, and the kind its transport needs). */
export function credentialsFor(list: GitCredentialView[], host: string | undefined, transport: Transport | undefined): GitCredentialView[] {
  const kind: CredKind = transport === "ssh" ? "ssh_key" : "https_token";
  return list.filter((c) => c.host === host && c.kind === kind);
}

/** One line for a test result. */
export function testSummary(r: GitTestResult): string {
  if (!r.ok) return r.message || "连接失败";
  const access = r.access === "write" ? "可读写" : "只读（员工推送会失败，请给凭据写权限）";
  return `连接成功 · ${access} · 默认分支 ${r.default_branch || "main"} · ${r.branches} 个分支`;
}

/** The access line of a saved repository. */
export function accessText(r: Pick<GitRepoView, "access" | "last_error" | "credential_id">): { text: string; tone: "ok" | "warn" | "bad" } {
  if (r.last_error) return { text: r.last_error, tone: "bad" };
  if (r.access === "write") return { text: "可读写", tone: "ok" };
  if (r.access === "read") return { text: r.credential_id ? "只读：员工推送会失败，请给凭据写权限" : "公开仓库，只读", tone: "warn" };
  return { text: "还没测试", tone: "warn" };
}
