import { spawnSync } from "node:child_process";

export type Isolation = "agent_user" | "sandbox" | "none";

/**
 * Isolation level (architecture.md §10.3). The daemon runs employees as its own user, so it
 * never claims agent_user; sandbox = Claude Code's Bash sandbox can run here (bubblewrap on
 * Linux, sandbox-exec on macOS); otherwise none.
 */
export function probeIsolation(): Isolation {
  if (process.platform === "linux" && onPath("bwrap")) return "sandbox";
  if (process.platform === "darwin" && onPath("sandbox-exec")) return "sandbox";
  return "none";
}

export function onPath(cmd: string): boolean {
  return spawnSync("sh", ["-c", `command -v ${cmd}`], { encoding: "utf8" }).status === 0;
}
