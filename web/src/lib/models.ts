// Model lists (pure): the choices /model, the role model field and the project model fields
// offer for one runtime, from GET /api/models (the user's own list, in order; set in 设置 → 团队).
import type { components } from "@/api/schema";

export type ModelEntry = components["schemas"]["Model"];
export type Runtime = "claude" | "codex";

export interface ModelChoice {
  value: string;
  label: string;
  /** The user's default of the runtime. */
  isDefault: boolean;
  /** Not in any list (a typed id that is saved now). */
  custom: boolean;
}

/** Same rule as the server (composer.ValidModel). */
export const MODEL_ID = /^[A-Za-z0-9][A-Za-z0-9._:/@[\]-]{0,99}$/;

/** Same limit as PUT /api/models/mine. */
export const MAX_MODELS = 50;

export const runtimeLabel = (r: string) => (r === "codex" ? "Codex" : "Claude Code");

/** The runtime's choices in list order, each id once; `current` is added when the list lacks it. */
export function modelChoices(list: ModelEntry[], runtime: Runtime, current = ""): ModelChoice[] {
  const out: ModelChoice[] = [];
  for (const m of list) {
    if (m.runtime !== runtime || out.some((c) => c.value === m.model)) continue;
    out.push({ value: m.model, label: m.label || m.model, isDefault: Boolean(m.default), custom: false });
  }
  if (current && !out.some((c) => c.value === current)) out.push({ value: current, label: current, isDefault: false, custom: true });
  return out;
}

/** The display name of a model id (its label in the lists, else the id). */
export function modelName(list: ModelEntry[], id: string): string {
  return list.find((m) => m.model === id)?.label || id;
}

/** The user's default of a runtime ("" when the list names none). */
export function defaultModel(list: ModelEntry[], runtime: Runtime): string {
  return list.find((m) => m.runtime === runtime && m.default)?.model ?? "";
}

/** Local check of an edited list (the server is the authority). */
export function modelProblems(list: Pick<ModelEntry, "runtime" | "model" | "default">[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const defaults = new Set<string>();
  if (list.length === 0) out.push("至少保留一个模型");
  if (list.length > MAX_MODELS) out.push(`最多 ${MAX_MODELS} 个模型`);
  for (const m of list) {
    if (!MODEL_ID.test(m.model)) out.push(`模型 ID “${m.model}” 只能用字母、数字和 . _ - : / @ [ ]`);
    if (seen.has(`${m.runtime}/${m.model}`)) out.push(`${m.model} 重复了`);
    if (m.default && defaults.has(m.runtime)) out.push(`${runtimeLabel(m.runtime)} 只能有一个默认模型`);
    seen.add(`${m.runtime}/${m.model}`);
    if (m.default) defaults.add(m.runtime);
  }
  return out;
}

/** The "" choice of a role's model field: follow the user's default of the runtime. */
export function followLabel(list: ModelEntry[], runtime: Runtime): string {
  const id = defaultModel(list, runtime);
  return `跟随我的默认（${id ? `当前：${modelName(list, id)}` : "未设置"}）`;
}

/** Whether a role may keep `model` with `runtime` ("" always fits; otherwise it must be in the runtime's list). */
export function modelFits(list: ModelEntry[], runtime: Runtime, model: string): boolean {
  return !model || list.some((m) => m.runtime === runtime && m.model === model);
}
