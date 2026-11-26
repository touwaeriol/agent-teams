// Which site the shell loads. Pure: tested in config.test.ts.

export const DEFAULT_APP_URL = "https://agent.prophey.ai";

/** The site to load: AGENT_TEAMS_URL when it is a valid http(s) URL, else the production site. */
export function resolveAppUrl(env: Record<string, string | undefined>): URL {
  const raw = env.AGENT_TEAMS_URL?.trim();
  const url = raw ? parseHttpUrl(raw) : null;
  return url ?? new URL(DEFAULT_APP_URL);
}

/** `raw` as an http(s) URL, or null. */
export function parseHttpUrl(raw: string): URL | null {
  try {
    const url = new URL(raw);
    return url.protocol === "http:" || url.protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}

/** The User-Agent the site sees: Electron's own plus `AgentTeamsDesktop/<version>`. */
export const desktopUserAgent = (base: string, version: string) => `${base} AgentTeamsDesktop/${version}`;
