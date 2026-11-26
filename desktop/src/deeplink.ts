// agentteams://<path> → <site>/<path>. Only paths on the loaded site are accepted. Pure.

export const PROTOCOL = "agentteams";
const PREFIX = `${PROTOCOL}:`;
const MAX_LENGTH = 2048;

/** The site URL for a deep link, or null when it is not one of ours or leaves the site. */
export function deepLinkToUrl(link: string, appOrigin: string): string | null {
  if (link.length > MAX_LENGTH || !link.toLowerCase().startsWith(PREFIX)) return null;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f\\]/.test(link)) return null;
  const path = link.slice(PREFIX.length).replace(/^\/+/, "");
  if (/^[a-z][a-z0-9+.-]*:/i.test(path)) return null; // agentteams://https://evil.example
  const url = new URL(`/${path}`, appOrigin);
  return url.origin === appOrigin ? url.toString() : null;
}

/** The first deep link among command-line arguments (Windows passes it to the second instance). */
export const findDeepLink = (argv: readonly string[]): string | undefined =>
  argv.find((arg) => arg.toLowerCase().startsWith(PREFIX));
