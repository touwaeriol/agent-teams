import { gitEnv, run } from "./exec.js";

/**
 * Employees can write the shared clone's `.git` (their commits land there), so its config is
 * untrusted: core.fsmonitor, hooks, filter drivers, url rewrites or http proxies there would run
 * commands as the daemon or leak tokens. Daemon git in those repos therefore (1) never carries a
 * token (network work happens in a daemon-owned mirror, see jobs.ts) and (2) overrides every
 * config key that makes a local command execute something.
 */
export function hardenedConfig(hooksDir: string): string[] {
  const pairs = [
    "core.fsmonitor=false",
    `core.hooksPath=${hooksDir}`,
    "core.sshCommand=false",
    "credential.helper=",
    "protocol.ext.allow=never",
    "submodule.recurse=false",
    "fetch.recurseSubmodules=false",
    "gc.auto=0",
    "maintenance.auto=false",
  ];
  return pairs.flatMap((p) => ["-c", p]);
}

/** Names of the filter drivers configured for a repository (reading config runs nothing). */
export async function filterDrivers(repo: string): Promise<string[]> {
  const r = await run("git", ["-C", repo, "config", "--null", "--get-regexp", "^filter\\."], { env: gitEnv() });
  if (r.code !== 0) return [];
  return [...new Set(r.stdout.split("\0").map(driverOf).filter((d): d is string => d !== null))];
}

/** "filter.<driver>.<key>\nvalue" → driver (drivers may contain dots). */
export function driverOf(entry: string): string | null {
  const key = entry.split("\n", 1)[0] ?? "";
  if (!key.startsWith("filter.")) return null;
  const rest = key.slice("filter.".length);
  const dot = rest.lastIndexOf(".");
  return dot > 0 ? rest.slice(0, dot) : null;
}

/** An empty `process` disables both clean and smudge for a driver; not required, so git never fails on it. */
export function disableFilters(drivers: string[]): string[] {
  return drivers.flatMap((d) => ["-c", `filter.${d}.process=`, "-c", `filter.${d}.clean=`, "-c", `filter.${d}.smudge=`, "-c", `filter.${d}.required=false`]);
}

/** Full `-c` prefix for a daemon git command inside an employee-writable repository. */
export async function untrustedRepoArgs(repo: string, hooksDir: string): Promise<string[]> {
  return [...hardenedConfig(hooksDir), ...disableFilters(await filterDrivers(repo))];
}
