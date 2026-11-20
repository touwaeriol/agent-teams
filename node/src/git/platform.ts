import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { delimiter, join } from "node:path";

/** Files the daemon owns and employees only execute: git hooks, askpass, gh wrapper (architecture.md §8.1, §10.4). */
export interface PlatformFiles {
  hooksDir: string;
  askpass: string;
  binDir: string;
}

/**
 * Employees push with their own short-lived token (§10.4); this hook keeps them off the branches
 * the platform owns: AT_PROTECTED_REFS (thread, base and default branches, set by the console),
 * main/master and origin/HEAD as a fallback, other employees' thread/* branches, tags and
 * anything that is not a branch. Merging stays with the merge queue and user acceptance.
 */
const PRE_PUSH = `#!/bin/sh
# agent-teams platform hook (architecture.md §8.1, §10.4)
own=$(git rev-parse --abbrev-ref HEAD 2>/dev/null)
default=$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null)
default=\${default#origin/}
protected="$AT_PROTECTED_REFS main master $default"
refuse() {
  echo "agent-teams: pushing $1 is not allowed: $2" >&2
  exit 1
}
while read -r local_ref local_sha remote_ref remote_sha; do
  branch=\${remote_ref#refs/heads/}
  [ "$branch" != "$remote_ref" ] || refuse "$remote_ref" "only branches can be pushed."
  for p in $protected; do
    [ "$branch" != "$p" ] || refuse "$branch" "it is a protected branch (base, default or thread branch). Push your own branch; merging goes through the merge queue (enqueue_merge / open_pr) and user acceptance."
  done
  case "$branch" in
    thread/*) [ "$branch" = "$own" ] || refuse "$branch" "thread/* branches belong to the platform and other employees; push your own branch ($own) or use push_branch." ;;
  esac
done
exit 0
`;

const ASKPASS = `#!/bin/sh
case "$1" in
  *[Uu]sername*) printf '%s\\n' "$AT_GIT_USERNAME" ;;
  *) printf '%s\\n' "$AT_GIT_PASSWORD" ;;
esac
`;

export function installPlatformFiles(dir: string, nodeCmd: string[]): PlatformFiles {
  const hooksDir = join(dir, "hooks");
  const binDir = join(dir, "bin");
  mkdirSync(hooksDir, { recursive: true, mode: 0o755 });
  mkdirSync(binDir, { recursive: true, mode: 0o755 });
  writeExec(join(hooksDir, "pre-push"), PRE_PUSH);
  const askpass = join(dir, "askpass");
  writeExec(askpass, ASKPASS);
  writeExec(join(binDir, "gh"), `#!/bin/sh\nexec ${nodeCmd.map(shellQuote).join(" ")} gh "$@"\n`);
  return { hooksDir, askpass, binDir };
}

/** OpenSSH client programs the platform bin wraps (remote-servers.md §7.3). */
export const SSH_PROGRAMS = ["ssh", "scp", "sftp"] as const;

/** The first `name` on PATH outside the skipped directories (the platform bin itself). */
export function findOnPath(name: string, pathVar: string, skip: string[] = []): string | null {
  for (const dir of pathVar.split(delimiter)) {
    if (!dir || skip.includes(dir)) continue;
    const p = join(dir, name);
    if (existsSync(p)) return p;
  }
  return null;
}

/**
 * ssh / scp / sftp wrappers: with AT_SSH_CONFIG (a session with remote servers) the real
 * program runs with -F <session ssh_config>; without it, exactly as if unwrapped. rsync, git,
 * ansible and docker -H ssh:// call `ssh` from PATH and so go through the wrapper too.
 */
export function sshWrapper(name: string, real: string | null): string {
  if (!real) return `#!/bin/sh\necho "agent-teams: ${name} (OpenSSH client) is not installed on this node" >&2\nexit 127\n`;
  const q = shellQuote(real);
  return `#!/bin/sh
# agent-teams platform wrapper (remote-servers.md §7.3)
if [ -n "$AT_SSH_CONFIG" ] && [ -f "$AT_SSH_CONFIG" ]; then
  exec ${q} -F "$AT_SSH_CONFIG" "$@"
fi
exec ${q} "$@"
`;
}

/** Writes the ssh / scp / sftp wrappers into binDir; returns the real programs found (null: missing). */
export function installSshWrappers(binDir: string, pathVar: string): Record<(typeof SSH_PROGRAMS)[number], string | null> {
  const out = {} as Record<(typeof SSH_PROGRAMS)[number], string | null>;
  for (const name of SSH_PROGRAMS) {
    out[name] = findOnPath(name, pathVar, [binDir]);
    writeExec(join(binDir, name), sshWrapper(name, out[name]));
  }
  return out;
}

/** `ssh -V` of an OpenSSH client (e.g. OpenSSH_9.6p1), or null. */
export function sshClientVersion(ssh: string | null): string | null {
  if (!ssh) return null;
  const r = spawnSync(ssh, ["-V"], { encoding: "utf8", timeout: 5000 });
  const m = /(OpenSSH_[^\s,]+)/.exec(`${r.stderr ?? ""}${r.stdout ?? ""}`);
  return m ? m[1]! : null;
}

function writeExec(path: string, body: string): void {
  writeFileSync(path, body, { mode: 0o755 });
  chmodSync(path, 0o755);
}

export function shellQuote(s: string): string {
  return /^[A-Za-z0-9_\/.:=@%+-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`;
}
