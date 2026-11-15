import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** Files the daemon owns and employees only execute: git hooks, askpass, gh wrapper (architecture.md §8.1, §10.4). */
export interface PlatformFiles {
  hooksDir: string;
  askpass: string;
  binDir: string;
}

const PRE_PUSH = `#!/bin/sh
# agent-teams: employees cannot push themselves; the platform pushes member branches.
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null)
while read -r local_ref local_sha remote_ref remote_sha; do
  case "$branch" in thread/*/*) ;; *) branch="";; esac
  if [ -z "$branch" ] || [ "$remote_ref" != "refs/heads/$branch" ]; then
    echo "agent-teams: pushing $remote_ref is not allowed. Use the push_branch tool to publish your branch." >&2
    exit 1
  fi
done
echo "agent-teams: use the push_branch tool to publish your branch." >&2
exit 1
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
