// Where a link goes: stays in the app (same origin as the loaded site), opens in the
// system browser (other http/https sites) or nowhere (any other scheme). Pure.
import { parseHttpUrl } from "./config";

export type LinkTarget = "app" | "browser" | "deny";

export function isSameOrigin(url: string, appOrigin: string): boolean {
  const parsed = parseHttpUrl(url);
  return parsed !== null && parsed.origin === appOrigin;
}

export function classifyLink(url: string, appOrigin: string): LinkTarget {
  const parsed = parseHttpUrl(url);
  if (!parsed) return "deny";
  return parsed.origin === appOrigin ? "app" : "browser";
}
