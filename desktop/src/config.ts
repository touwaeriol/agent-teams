// Which site the shell loads. Pure: tested in config.test.ts.

export const DEFAULT_APP_URL = "https://agent.prophey.ai";

/**
 * The site to load: AGENT_TEAMS_URL when it is a valid http(s) URL, else the production site. A
 * packaged (signed) build ignores it, so that nothing that can set the environment can point the app,
 * and the permissions it grants its own site, at another origin.
 */
export function resolveAppUrl(env: Record<string, string | undefined>, packaged = false): URL {
  const raw = packaged ? undefined : env.AGENT_TEAMS_URL?.trim();
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

/** Whether this build can install updates itself: not an unsigned Mac build (macSigned is written into package.json by electron-builder). */
export function canInstallUpdates(platform: string, meta: { macSigned?: boolean }): boolean {
  return platform !== "darwin" || meta.macSigned === true;
}
