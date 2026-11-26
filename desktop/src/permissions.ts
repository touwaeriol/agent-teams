// Which permission requests the shell grants: microphone (audio only), notifications and
// clipboard writes, and only to the loaded site itself (not to iframes of other origins). Pure.
import { isSameOrigin } from "./navigation";

const GRANTED = new Set(["media", "notifications", "clipboard-sanitized-write"]);

export interface PermissionAsk {
  permission: string;
  /** URL or origin asking (requestingUrl / requestingOrigin; the embedding page for iframes). */
  origin: string;
  /** For media: the device kinds asked for (request: mediaTypes; check: mediaType). */
  mediaTypes?: readonly string[];
}

export function allowPermission(ask: PermissionAsk, appOrigin: string): boolean {
  if (!GRANTED.has(ask.permission) || !isSameOrigin(ask.origin, appOrigin)) return false;
  if (ask.permission !== "media") return true;
  // A media check without a type (device enumeration) is fine; video is not something the site uses.
  return (ask.mediaTypes ?? []).every((t) => t === "audio" || t === "unknown");
}
