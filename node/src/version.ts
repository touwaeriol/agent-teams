// Versions reported in hello (protocol.md §3). The SDK and Codex CLI are pinned exactly in package.json.
import { fileURLToPath } from "node:url";
import { buildOf, releaseId } from "./release.js";

export const DAEMON_VERSION = "2.0.0";
/** First 8 hex of sha256 of the running bundle (dist/node.mjs); the release id is <version>+<build>. */
export const DAEMON_BUILD = selfBuild();
export const DAEMON_RELEASE = releaseId(DAEMON_VERSION, DAEMON_BUILD);
export const CLAUDE_SDK_VERSION = "0.3.325";
export const CODEX_CLI_VERSION = "0.169.0";
export const PROTOCOLS = [2] as const;

function selfBuild(): string {
  try {
    return buildOf(fileURLToPath(import.meta.url));
  } catch {
    return "";
  }
}
