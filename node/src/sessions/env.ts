/** Default auth/network variables passed from node.env into employee sessions (architecture.md §6.1). */
export const DEFAULT_PASS_ENV = [
  "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
  "AWS_*", "CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_APPLICATION_CREDENTIALS",
  "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE",
];
export const CODEX_PASS_ENV = ["OPENAI_API_KEY", "OPENAI_BASE_URL"];

export interface EnvInput {
  base: NodeJS.ProcessEnv;
  passList: string[];
  home: string;
  extra?: Record<string, string>;
  fixed?: Record<string, string>;
  pathPrefix?: string[];
}

/**
 * Builds a session environment item by item (the SDK env replaces, it does not merge).
 * AGENT_TEAMS_* (including the node token) is never passed, whatever the lists say.
 */
export function buildEnv(input: EnvInput): Record<string, string> {
  const env: Record<string, string> = {
    PATH: [...(input.pathPrefix ?? []), input.base.PATH ?? "/usr/local/bin:/usr/bin:/bin"].join(":"),
    HOME: input.home,
    LANG: input.base.LANG ?? "C.UTF-8",
  };
  Object.assign(env, pickPassEnv(input.base, input.passList));
  Object.assign(env, input.extra ?? {}, input.fixed ?? {});
  for (const k of Object.keys(env)) if (k.startsWith("AGENT_TEAMS_")) delete env[k];
  return env;
}

export function pickPassEnv(base: NodeJS.ProcessEnv, passList: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(base)) {
    if (v === undefined || k.startsWith("AGENT_TEAMS_")) continue;
    if (passList.some((p) => (p.endsWith("*") ? k.startsWith(p.slice(0, -1)) : k === p))) out[k] = v;
  }
  return out;
}
