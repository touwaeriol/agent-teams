import type { SlashCommand } from "@anthropic-ai/claude-agent-sdk";
import { SKILLS_PLUGIN } from "../skills.js";

/** A slash command as session.opened reports it (protocol SessionInit.commands). */
export interface SessionCommand {
  name: string;
  description?: string;
  argument_hint?: string;
}

const MAX_COMMANDS = 200;

const clip = (s: string | undefined, n: number) => (s ? s.slice(0, n) : undefined);

/** The session's own commands (role skills, plugin skills and commands): Claude Code's built-ins are left out. */
export function sessionCommands(list: SlashCommand[] | undefined): SessionCommand[] {
  return (list ?? [])
    .filter((c) => !c.builtin && c.name)
    .slice(0, MAX_COMMANDS)
    .map((c) => ({
      name: c.name.slice(0, 200),
      ...(c.description ? { description: clip(c.description, 2000) } : {}),
      ...(c.argumentHint ? { argument_hint: clip(c.argumentHint, 200) } : {}),
    }));
}

const SLASH = /^\/([A-Za-z0-9_.-]+)(?=\s|$)/;

/**
 * "/design args" → "/agent-teams-skills:design args": a bare name the user picked from the
 * session's commands runs the session's command, not a Claude Code built-in of the same name
 * (the CLI has its own /design). Qualified names, exact matches and unknown names stay as typed.
 */
export function qualifySlash(text: string, names: string[]): string {
  const m = SLASH.exec(text);
  if (!m || names.includes(m[1])) return text;
  const matches = names.filter((n) => n.endsWith(`:${m[1]}`));
  const pick = matches.length === 1 ? matches[0] : matches.find((n) => n === `${SKILLS_PLUGIN}:${m[1]}`);
  return pick ? `/${pick}${text.slice(m[0].length)}` : text;
}
