import { mkdirSync, mkdtempSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { PathOutsideRoot, resolveInRoot } from "../src/util/paths.js";

let dir: string;
let root: string;
let outside: string;
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "at-paths-"));
  root = join(dir, "root");
  outside = join(dir, "outside");
  mkdirSync(join(root, "projects/p1"), { recursive: true });
  mkdirSync(outside);
});
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe("resolveInRoot", () => {
  it("resolves root-relative paths, existing or not", () => {
    expect(resolveInRoot(root, "projects/p1")).toBe(join(root, "projects/p1"));
    expect(resolveInRoot(root, "projects/p1/threads/1/dev")).toBe(join(root, "projects/p1/threads/1/dev"));
  });

  it("refuses absolute, escaping, empty and NUL paths and the root itself", () => {
    for (const rel of ["", "/etc", "../x", "projects/../../x", ".", "a\0b"]) expect(() => resolveInRoot(root, rel), rel).toThrow(PathOutsideRoot);
  });

  it("refuses a path that leads outside the root through a symlink", () => {
    symlinkSync(outside, join(root, "projects/p1/link"));
    expect(() => resolveInRoot(root, "projects/p1/link")).toThrow(PathOutsideRoot);
    expect(() => resolveInRoot(root, "projects/p1/link/new/dir")).toThrow(PathOutsideRoot);
  });

  it("accepts paths under a root that does not exist yet behind a symlinked ancestor", () => {
    const alias = join(dir, "alias");
    symlinkSync(root, alias);
    const fresh = join(alias, "agent-skills", "dev");
    expect(resolveInRoot(fresh, "SKILL.md")).toBe(join(fresh, "SKILL.md"));
    expect(resolveInRoot(join(alias, "agent-plugins"), "p@1/x.json")).toBe(join(alias, "agent-plugins", "p@1/x.json"));
    expect(() => resolveInRoot(fresh, "../../../outside/x")).toThrow(PathOutsideRoot);
    symlinkSync(outside, join(root, "escape"));
    expect(() => resolveInRoot(join(alias, "new-root"), "../escape/x")).toThrow(PathOutsideRoot);
  });

  it("refuses a dangling symlink whose target cannot be checked", () => {
    symlinkSync(join(outside, "missing"), join(root, "projects/p1/dangling"));
    expect(() => resolveInRoot(root, "projects/p1/dangling/x")).toThrow(PathOutsideRoot);
  });

  it("allows symlinks that stay inside the root, and a root that is itself a symlink", () => {
    mkdirSync(join(root, "projects/p2"));
    symlinkSync(join(root, "projects/p2"), join(root, "projects/p1/to-p2"));
    expect(resolveInRoot(root, "projects/p1/to-p2/x")).toBe(join(root, "projects/p1/to-p2/x"));
    const alias = join(dir, "alias");
    symlinkSync(root, alias);
    expect(resolveInRoot(alias, "projects/p1")).toBe(join(alias, "projects/p1"));
    symlinkSync(root, join(root, "projects/p1/self"));
    expect(() => resolveInRoot(root, "projects/p1/self")).toThrow(PathOutsideRoot);
  });
});
