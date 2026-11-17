import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

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

function writeExec(path: string, body: string): void {
  writeFileSync(path, body, { mode: 0o755 });
  chmodSync(path, 0o755);
}

export function shellQuote(s: string): string {
  return /^[A-Za-z0-9_\/.:=@%+-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`;
}
