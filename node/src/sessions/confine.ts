import { spawn, spawnSync } from "node:child_process";
import type { SpawnOptions as SdkSpawnOptions, SpawnedProcess } from "@anthropic-ai/claude-agent-sdk";
import { onPath } from "../isolation.js";

/**
 * Wraps an employee session's command (Claude Code, codex app-server, agy) so it cannot gain
 * privileges the daemon keeps: no_new_privs (setuid programs such as fusermount3 or sudo, and file
 * capabilities, give nothing), and when the daemon runs as root, an empty capability bounding set
 * too. The daemon itself keeps them: it mounts the shared folder with geesefs through the setuid
 * fusermount3 (Docker nodes add SYS_ADMIN to the container's bounding set for that). Opt-in per
 * node (AGENT_TEAMS_CONFINE=on).
 */
export type Confine = (cmd: string, args: string[]) => { cmd: string; args: string[] };

/** No wrapping (no setpriv here, or not Linux). */
export const unconfined: Confine = (cmd, args) => ({ cmd, args });

/** setpriv (util-linux) with the flags for this process: --no-new-privs, and the capability drops as root. */
export function setprivConfine(setpriv: string, root: boolean): Confine {
  const flags = ["--no-new-privs", ...(root ? ["--bounding-set=-all", "--inh-caps=-all"] : [])];
  return (cmd, args) => ({ cmd: setpriv, args: [...flags, "--", cmd, ...args] });
}

/**
 * The wrapper this node uses: off unless AGENT_TEAMS_CONFINE=on (employees may sudo to install
 * software, which no_new_privs would stop), and only on Linux with setpriv.
 */
export function probeConfine(env: NodeJS.ProcessEnv = process.env): Confine {
  if (env.AGENT_TEAMS_CONFINE !== "on" || process.platform !== "linux" || !onPath("setpriv")) return unconfined;
  const root = process.geteuid?.() === 0;
  // Check that this setpriv takes the flags (an old util-linux, or a kernel without them, fails here
  // rather than at every session start).
  const probe = setprivConfine("setpriv", root)("true", []);
  return spawnSync(probe.cmd, probe.args, { stdio: "ignore" }).status === 0 ? setprivConfine("setpriv", root) : unconfined;
}

/** spawnClaudeCodeProcess for the Agent SDK: the SDK's command run under confine; stderr goes to onStderr. */
export function confinedClaudeSpawn(confine: Confine, onStderr?: (d: string) => void): (o: SdkSpawnOptions) => SpawnedProcess {
  return (o) => {
    const c = confine(o.command, o.args);
    const child = spawn(c.cmd, c.args, { cwd: o.cwd, env: o.env, signal: o.signal, stdio: ["pipe", "pipe", "pipe"] });
    child.stderr?.setEncoding("utf8");
    child.stderr?.on("data", (d: string) => onStderr?.(d));
    return child as unknown as SpawnedProcess;
  };
}
