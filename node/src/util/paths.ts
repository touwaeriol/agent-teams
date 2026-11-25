import { lstatSync, realpathSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";

export class PathOutsideRoot extends Error {
  readonly code = "path_outside_root";
  constructor(path: string) {
    super(`path is outside the node root: ${path}`);
  }
}

/**
 * Resolves a root-relative path; anything absolute or escaping the root is refused (architecture.md §10.3).
 * The check is made on the literal path and again after resolving symbolic links (the deepest part of the
 * path that exists is realpath'd), so a symlink inside the root cannot lead outside it. The literal path
 * is returned.
 */
export function resolveInRoot(root: string, rel: string): string {
  if (!rel || isAbsolute(rel) || rel.includes("\0")) throw new PathOutsideRoot(rel);
  const abs = resolve(root, rel);
  if (!isInside(root, abs) || abs === resolve(root)) throw new PathOutsideRoot(rel);
  // Both sides resolved the same way: a root that does not exist yet (skills/plugins directories
  // on first use) still has its existing ancestors (a symlinked HOME, /tmp on macOS) resolved.
  const realRoot = realpathLoose(resolve(root)) ?? resolve(root);
  const real = realpathLoose(abs);
  if (real === null || !isInside(realRoot, real) || real === realRoot) throw new PathOutsideRoot(rel);
  return abs;
}

export function isInside(parent: string, child: string): boolean {
  const r = relative(resolve(parent), resolve(child));
  return r === "" || (!r.startsWith("..") && !isAbsolute(r) && !r.startsWith(`..${sep}`));
}

/**
 * realpath of p when it exists; otherwise realpath of its deepest existing ancestor joined with the
 * rest. null when that ancestor is a dangling symlink (its target cannot be checked).
 */
function realpathLoose(p: string): string | null {
  const rest: string[] = [];
  let cur = p;
  for (;;) {
    let exists = true;
    try {
      lstatSync(cur);
    } catch {
      exists = false;
    }
    if (exists) {
      try {
        return join(realpathSync(cur), ...rest.reverse());
      } catch {
        return null;
      }
    }
    const parent = dirname(cur);
    if (parent === cur) return p;
    rest.push(cur.slice(parent.length).replace(/^[\\/]+/, ""));
    cur = parent;
  }
}
