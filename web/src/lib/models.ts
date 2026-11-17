// Model lists (pure): the choices /model, the role model field and the project model fields
// offer for one runtime, from GET /api/models (the platform list in order, then the user's own).
import type { components } from "@/api/schema";

export type ModelEntry = components["schemas"]["Model"];
export type Runtime = "claude" | "codex";

export interface ModelChoice {
  value: string;
  label: string;
  /** The platform default of the runtime. */
  isDefault: boolean;
  /** The user's own entry. */
  mine: boolean;
  /** Not in any list (a typed id that is saved now). */
  custom: boolean;
}

/** Same rule as the server (composer.ValidModel). */
export const MODEL_ID = /^[A-Za-z0-9][A-Za-z0-9._:/@[\]-]{0,99}$/;

export const runtimeLabel = (r: string) => (r === "codex" ? "Codex" : "Claude Code");

/** The runtime's choices, each id once (platform first); `current` is added when no list has it. */
export function modelChoices(list: ModelEntry[], runtime: Runtime, current = ""): ModelChoice[] {
  const out: ModelChoice[] = [];
  for (const m of list) {
    if (m.runtime !== runtime || out.some((c) => c.value === m.model)) continue;
    out.push({ value: m.model, label: m.label || m.model, isDefault: Boolean(m.default), mine: m.mine, custom: false });
  }
  if (current && !out.some((c) => c.value === current)) out.push({ value: current, label: current, isDefault: false, mine: false, custom: true });
  return out;
}

/** The display name of a model id (its label in the lists, else the id). */
export function modelName(list: ModelEntry[], id: string): string {
  return list.find((m) => m.model === id)?.label || id;
}

/** The platform default of a runtime ("" when the admin list names none). */
export function platformDefault(list: ModelEntry[], runtime: Runtime): string {
  return list.find((m) => !m.mine && m.runtime === runtime && m.default)?.model ?? "";
}

/** The rows of an owner's editable list, as PUT expects them. */
export function editable(list: ModelEntry[], mine: boolean): ModelEntry[] {
  return list.filter((m) => m.mine === mine).map((m) => ({ ...m }));
}

/** Local check of an edited list (the server is the authority). */
export function modelProblems(list: Pick<ModelEntry, "runtime" | "model" | "default">[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const defaults = new Set<string>();
  for (const m of list) {
    if (!MODEL_ID.test(m.model)) out.push(`模型 ID “${m.model}” 只能用字母、数字和 . _ - : / @ [ ]`);
    if (seen.has(`${m.runtime}/${m.model}`)) out.push(`${m.model} 重复了`);
    if (m.default && defaults.has(m.runtime)) out.push(`${runtimeLabel(m.runtime)} 只能有一个默认模型`);
    seen.add(`${m.runtime}/${m.model}`);
    if (m.default) defaults.add(m.runtime);
  }
  return out;
}
