import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { SessionOpen, SkillBundle } from "../protocol.js";
import { isInside, resolveInRoot } from "../util/paths.js";

/** Name of the local plugin that carries an employee's skills; skills are "<plugin>:<skill>". */
export const SKILLS_PLUGIN = "agent-teams-skills";
const SAFE = /^[A-Za-z0-9_-]+$/;

export interface MaterializedSkills {
  /** Plugin directory for SDK options.plugins. */
  pluginDir: string;
  /** Qualified names for SDK options.skills (only these are listed to the model). */
  names: string[];
}

/**
 * Writes the session's skills (session.open.skills) as a local Claude Code plugin in a
 * per-employee directory under <skillsRoot> — outside every worktree, so skill files are
 * never committed — and returns what the SDK needs to load them (architecture.md §6.5).
 * The directory is rebuilt on each open so removed skills disappear.
 */
export function materializeSkills(skillsRoot: string, spec: Pick<SessionOpen, "employee" | "skills">): MaterializedSkills | null {
  const skills = spec.skills ?? [];
  if (skills.length === 0) return null;
  const pluginDir = employeeDir(skillsRoot, spec.employee);
  rmSync(pluginDir, { recursive: true, force: true });
  mkdirSync(join(pluginDir, ".claude-plugin"), { recursive: true, mode: 0o700 });
  writeFileSync(join(pluginDir, ".claude-plugin", "plugin.json"), JSON.stringify({ name: SKILLS_PLUGIN, version: "1.0.0", description: "Skills of this agent-teams role" }));
  for (const skill of skills) writeSkill(pluginDir, skill);
  return { pluginDir, names: skills.map((s) => `${SKILLS_PLUGIN}:${s.name}`) };
}

/** <root>/<project>/<thread no or "project">/<employee>, every segment made safe. */
export function employeeDir(skillsRoot: string, e: SessionOpen["employee"]): string {
  const parts = [e.project_id, e.thread_no ? String(e.thread_no) : "project", e.name].map(safeSegment);
  return resolveInRoot(skillsRoot, join(...parts));
}

/** One directory name: unsafe characters become _, and "." / ".." never navigate. */
function safeSegment(p: string): string {
  const s = p.replace(/[^A-Za-z0-9_.-]/g, "_");
  return /^\.*$/.test(s) ? `_${s}` : s;
}

function writeSkill(pluginDir: string, skill: SkillBundle): void {
  if (!SAFE.test(skill.name)) throw new Error(`bad skill name ${skill.name}`);
  const dir = join(pluginDir, "skills", skill.name);
  for (const f of skill.files) {
    const abs = resolveInRoot(dir, f.path);
    if (!isInside(dir, abs) || f.path.split("/").some((p) => p.startsWith("."))) throw new Error(`bad skill file ${f.path}`);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, f.content);
  }
}

