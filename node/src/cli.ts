import { readFileSync } from "node:fs";
import { ConfigError, loadConfig } from "./config.js";
import { gitSshMain } from "./cred/gitssh.js";
import { ghMain, gitCredentialMain } from "./cred/helper.js";
import { Daemon } from "./daemon.js";
import { sshProxyMain } from "./ssh/proxy.js";
import { createLogger } from "./util/log.js";
import { DAEMON_RELEASE, DAEMON_VERSION } from "./version.js";

const USAGE = `agent-teams-node ${DAEMON_VERSION}

usage:
  agent-teams-node run [--console URL] [--token TOKEN] [--root DIR] [--state DIR] [--max-sessions N]
                       [--env-file FILE] [--log-level LEVEL]
  agent-teams-node version

Defaults come from ~/.config/agent-teams/node.env (AGENT_TEAMS_CONSOLE, AGENT_TEAMS_NODE_TOKEN,
AGENT_TEAMS_ROOT, AGENT_TEAMS_MAX_SESSIONS, AGENT_TEAMS_PASS_ENV). Prefer AGENT_TEAMS_NODE_TOKEN over --token
(visible in the process list). The console URL must be https:// unless it is localhost or
AGENT_TEAMS_ALLOW_INSECURE=1 is set. Internal helpers: git-credential, git-ssh, gh, ssh-proxy.
`;

async function main(argv: string[]): Promise<number> {
  const [cmd, ...rest] = argv;
  switch (cmd) {
    case "run":
      return runDaemon(rest);
    case "git-credential":
      return gitCredential(rest);
    case "gh":
      return ghMain(rest, process.env);
    case "git-ssh":
      return gitSshMain(rest, process.env);
    case "ssh-proxy":
      return sshProxyMain(rest, process.env);
    case "version":
    case "--version":
      process.stdout.write(`${DAEMON_RELEASE}\n`);
      return 0;
    default:
      process.stderr.write(USAGE);
      return cmd === "help" || cmd === "--help" ? 0 : 2;
  }
}

async function runDaemon(argv: string[]): Promise<number> {
  const config = loadConfig(argv);
  const log = createLogger(config.logLevel);
  for (const w of config.warnings) log.warn(w);
  return new Promise<number>((resolve) => {
    let daemon: Daemon;
    const exit = (code: number) => void daemon.stop().finally(() => resolve(code));
    daemon = new Daemon({ ...config, url: config.wsUrl, log, nodeCmd: [process.execPath, process.argv[1]!], onExit: exit });
    process.once("SIGTERM", () => exit(0));
    process.once("SIGINT", () => exit(0));
    daemon.start().then(
      () => log.info({ console: config.consoleUrl, root: config.root }, "agent-teams node daemon started"),
      (err: Error) => {
        log.error({ err: err.message }, "daemon failed to start");
        resolve(1);
      },
    );
  });
}

async function gitCredential(argv: string[]): Promise<number> {
  const op = argv.at(-1) ?? "";
  const args: Record<string, string> = {};
  for (let i = 0; i < argv.length - 1; i += 2) args[argv[i]!.replace(/^--/, "")] = argv[i + 1] ?? "";
  const stdin = op === "get" ? readFileSync(0, "utf8") : "";
  const r = await gitCredentialMain(args, op, stdin);
  process.stdout.write(r.out);
  return r.code;
}

main(process.argv.slice(2)).then(
  (code) => process.exit(code),
  (err: Error) => {
    process.stderr.write(`${err instanceof ConfigError ? "" : "fatal: "}${err.message}\n`);
    process.exit(err instanceof ConfigError ? 2 : 1);
  },
);
