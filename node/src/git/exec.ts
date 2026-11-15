import { spawn } from "node:child_process";

export interface RunResult {
  code: number;
  stdout: string;
  stderr: string;
}

export interface RunOptions {
  cwd?: string;
  env?: NodeJS.ProcessEnv;
  timeoutMs?: number;
}

/** Runs a command without a shell; never throws for a non-zero exit. */
export function run(cmd: string, args: string[], opts: RunOptions = {}): Promise<RunResult> {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, { cwd: opts.cwd, env: opts.env ?? process.env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    const timer = opts.timeoutMs ? setTimeout(() => child.kill("SIGKILL"), opts.timeoutMs) : null;
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("error", (err) => resolve({ code: 127, stdout, stderr: stderr + err.message }));
    child.on("close", (code) => {
      if (timer) clearTimeout(timer);
      resolve({ code: code ?? 1, stdout, stderr });
    });
  });
}

/**
 * Non-interactive git environment. AGENT_TEAMS_* (the node token when it came from the
 * environment) never reaches git or anything git starts.
 */
export function gitEnv(extra: Record<string, string> = {}, base: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [k, v] of Object.entries(base)) if (!k.startsWith("AGENT_TEAMS_") && !k.startsWith("GIT_CONFIG")) env[k] = v;
  return { ...env, GIT_TERMINAL_PROMPT: "0", GCM_INTERACTIVE: "never", ...extra };
}
