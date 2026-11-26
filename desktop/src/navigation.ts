// Where a link goes: stays in the app (same origin as the loaded site), stays in the window as part
// of a sign-in or install flow (GitHub: the app install and App creation forms post back and forth
// with github.com and end on the site again), opens in the system browser (other http/https sites,
// mailto:) or nowhere (any other scheme). Pure.
import { parseHttpUrl } from "./config";

export type LinkTarget = "app" | "flow" | "browser" | "deny";

/** Origins whose pages a flow started on the site walks through inside the window. */
export const FLOW_ORIGINS: readonly string[] = ["https://github.com"];

export function isSameOrigin(url: string, appOrigin: string): boolean {
  const parsed = parseHttpUrl(url);
  return parsed !== null && parsed.origin === appOrigin;
}

export function classifyLink(url: string, appOrigin: string): LinkTarget {
  if (/^mailto:/i.test(url)) return "browser";
  const parsed = parseHttpUrl(url);
  if (!parsed) return "deny";
  if (parsed.origin === appOrigin) return "app";
  return FLOW_ORIGINS.includes(parsed.origin) ? "flow" : "browser";
}

/** In-window navigation (will-navigate) is allowed for the site and for flow pages. */
export const staysInWindow = (target: LinkTarget) => target === "app" || target === "flow";
