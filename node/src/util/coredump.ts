import { spawnSync } from "node:child_process";

export type CoreDumps = "off" | "kept" | "failed";

type Run = (cmd: string, args: string[]) => { status: number | null };

const defaultRun: Run = (cmd, args) => spawnSync(cmd, args, { stdio: "ignore" });

/**
 * Turns core dumps off for the daemon and everything it starts (employee sessions, their builds and
 * test runs inherit RLIMIT_CORE). A crashing process in a worktree otherwise leaves a core file of
 * several GB that `git add -A` commits and GitHub rejects (files over 100 MB). Linux only, through
 * util-linux prlimit; AGENT_TEAMS_CORE_DUMPS=on keeps the limit the daemon was started with.
 */
export function disableCoreDumps(env: NodeJS.ProcessEnv = process.env, platform: string = process.platform,
  pid: number = process.pid, run: Run = defaultRun): CoreDumps {
  if (env.AGENT_TEAMS_CORE_DUMPS === "on" || platform !== "linux") return "kept";
  return run("prlimit", ["--pid", String(pid), "--core=0:0"]).status === 0 ? "off" : "failed";
}
