import { isAbsolute, relative, resolve, sep } from "node:path";

export class PathOutsideRoot extends Error {
  readonly code = "path_outside_root";
  constructor(path: string) {
    super(`path is outside the node root: ${path}`);
  }
}

/** Resolves a root-relative path; anything absolute or escaping the root is refused (architecture.md §10.3). */
export function resolveInRoot(root: string, rel: string): string {
  if (!rel || isAbsolute(rel) || rel.includes("\0")) throw new PathOutsideRoot(rel);
  const abs = resolve(root, rel);
  if (!isInside(root, abs) || abs === resolve(root)) throw new PathOutsideRoot(rel);
  return abs;
}

export function isInside(parent: string, child: string): boolean {
  const r = relative(resolve(parent), resolve(child));
  return r === "" || (!r.startsWith("..") && !isAbsolute(r) && !r.startsWith(`..${sep}`));
}
