// Design prototype data for docs/v2/git-repos.md (dev only, no API). Shapes follow §3 of that doc.

export type Transport = "https" | "ssh";
export type CredKind = "https_token" | "ssh_key";
export type Provider = "github" | "gitlab" | "gitea" | "bitbucket" | "generic";

export interface ProtoCredential {
  id: string;
  name: string;
  host: string;
  kind: CredKind;
  username: string;
  key_fingerprint?: string;
  public_key?: string;
  generated?: boolean;
  expires_at?: string;
  repos: number;
  cred_updated_at: string;
}

export interface ProtoRepo {
  id: string;
  slug: string;
  name: string;
  url: string;
  transport: Transport;
  provider: Provider;
  credential_id: string | null;
  default_branch: string;
  access: "" | "read" | "write";
  host_key_fp?: string;
  projects: string[];
  last_checked_at: string;
}

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const later = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString();

export const PROVIDER_LABEL: Record<Provider, string> = { github: "GitHub", gitlab: "GitLab", gitea: "Gitea", bitbucket: "Bitbucket", generic: "自建 Git" };
export const CRED_KIND_LABEL: Record<CredKind, string> = { https_token: "HTTPS 令牌", ssh_key: "SSH 私钥" };

export const credentials: ProtoCredential[] = [
  { id: "gc_1", name: "GitLab 个人令牌", host: "gitlab.com", kind: "https_token", username: "oauth2", expires_at: later(9), repos: 2, cred_updated_at: ago(60 * 24 * 80) },
  {
    id: "gc_2",
    name: "infra 部署密钥",
    host: "git.prophey.ai:2222",
    kind: "ssh_key",
    username: "git",
    generated: true,
    key_fingerprint: "SHA256:q3V0m8bP9Lr2xS1kZ7nE4yWcT6aJ5hD0uFgR8iOeK2s",
    public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7pX2c9vQe4nWm1tYb8sLr3uJd6hA0fGzP5kE2oNiQx agent-teams",
    repos: 1,
    cred_updated_at: ago(60 * 24 * 3),
  },
];

export const repos: ProtoRepo[] = [
  {
    id: "gr_7f2a",
    slug: "gitlab.com/prophey/sup2api",
    name: "sup2api",
    url: "https://gitlab.com/prophey/sup2api.git",
    transport: "https",
    provider: "gitlab",
    credential_id: "gc_1",
    default_branch: "develop",
    access: "write",
    projects: ["sup2api and newapi"],
    last_checked_at: ago(12),
  },
  {
    id: "gr_19c0",
    slug: "git.prophey.ai:2222/infra/deploy",
    name: "deploy",
    url: "ssh://git@git.prophey.ai:2222/infra/deploy.git",
    transport: "ssh",
    provider: "generic",
    credential_id: "gc_2",
    default_branch: "main",
    access: "write",
    host_key_fp: "ED25519 SHA256:4mS2pQx8wE1rT7yU0iO9aZ3sD6fG5hJ2kL8zX1cV4bN",
    projects: ["agent team"],
    last_checked_at: ago(60 * 3),
  },
  {
    id: "gr_a311",
    slug: "gitlab.com/prophey/docs-site",
    name: "",
    url: "https://gitlab.com/prophey/docs-site.git",
    transport: "https",
    provider: "gitlab",
    credential_id: "gc_1",
    default_branch: "main",
    access: "read",
    projects: [],
    last_checked_at: ago(60 * 24 * 2),
  },
];

export const credentialOf = (r: ProtoRepo) => credentials.find((c) => c.id === r.credential_id) ?? null;

/** §3.5: what the add dialog infers from a pasted URL. */
export interface Parsed {
  ok: boolean;
  transport?: Transport;
  host?: string;
  path?: string;
  provider?: Provider;
  username?: string;
  problem?: string;
}

const PROVIDERS: Record<string, { provider: Provider; user: string }> = {
  "gitlab.com": { provider: "gitlab", user: "oauth2" },
  "github.com": { provider: "github", user: "x-access-token" },
  "bitbucket.org": { provider: "bitbucket", user: "x-token-auth" },
  "gitea.com": { provider: "gitea", user: "" },
};

export function parseUrl(text: string): Parsed {
  const t = text.trim();
  if (!t) return { ok: false };
  if (/^https?:\/\/[^/]*@/.test(t)) return { ok: false, problem: "地址里不要带令牌，请填到下面的“令牌”框里" };
  if (/^(file|git):\/\//.test(t)) return { ok: false, problem: "只支持 HTTPS 和 SSH 地址" };
  const scp = t.match(/^([\w.-]+)@([\w.-]+):(?!\/)(.+?)(\.git)?$/);
  const url = t.match(/^(https|ssh):\/\/(?:([\w.-]+)@)?([\w.-]+(?::\d+)?)\/(.+?)(\.git)?\/?$/);
  const parts = scp ? { transport: "ssh" as const, user: scp[1], host: scp[2], path: scp[3] } : url ? { transport: url[1] === "https" ? ("https" as const) : ("ssh" as const), user: url[2], host: url[3], path: url[4] } : null;
  if (!parts) return { ok: false, problem: "看不懂这个地址：请贴 https://… 或 git@…:… 形式的克隆地址" };
  const host = parts.host.toLowerCase().replace(/:443$|:22$/, "");
  const known = PROVIDERS[host.replace(/:\d+$/, "")];
  const username = parts.transport === "ssh" ? parts.user || "git" : (known?.user ?? "");
  return { ok: true, transport: parts.transport, host, path: parts.path, provider: known?.provider ?? "generic", username };
}
