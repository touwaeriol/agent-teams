// Models (pure). There is no model list: every role (coordinator and lead too) and the project
// settings name their model by hand. The fields suggest the models the roles already use plus a
// few common ids, and accept any id in the server's format (composer.ValidModel).
import { runtimeLabel, runtimeSpec, type Runtime } from "@/lib/runtimes";

export { runtimeLabel, type Runtime };

/** Same rule as the server (composer.ValidModel). */
export const MODEL_ID = /^[A-Za-z0-9][A-Za-z0-9._:/@[\]-]{0,99}$/;

/** What a Claude session runs when neither the project nor the role names a model (orchestrator.DefaultModel). */
export const BUILTIN_MODEL = runtimeSpec("claude").builtinModel;

/** Common ids offered first, per runtime, with their display names. */
export const COMMON_MODELS: Record<Runtime, { id: string; label: string }[]> = {
  claude: [
    { id: "claude-opus-5-5", label: "Opus 5.5" },
    { id: "claude-fable-5-2", label: "Fable 5.2" },
    { id: "claude-sonnet-5-5", label: "Sonnet 5.5" },
  ],
  codex: [],
  antigravity: [],
};

/** Whether `id` may be saved as a model: "" (not set) or the server's format. */
export function validModel(id: string): boolean {
  return id === "" || MODEL_ID.test(id);
}

export const MODEL_FORMAT_HINT = "模型 ID 只能用字母、数字和 . _ - : / @ [ ]，最多 100 个字符";

/** The display name of a model id: the common name when it is one, else the id. */
export function modelName(id: string): string {
  for (const list of Object.values(COMMON_MODELS)) {
    const hit = list.find((m) => m.id === id);
    if (hit) return hit.label;
  }
  return id;
}

/** What a role's empty model falls back to (the built-in default), short enough for the placeholder. */
export function emptyRoleModel(runtime: Runtime): string {
  const s = runtimeSpec(runtime);
  return s.builtinModel ? `留空 = ${s.builtinModel}` : `留空 = ${s.label} 默认`;
}

interface ScopedRole {
  tier: string;
  runtime: string;
  runtimes?: string[] | null;
  models?: Record<string, string> | null;
  model?: string;
  title?: string;
  role?: string;
}

/**
 * The suggestions of a model field of one runtime: the models the roles of that runtime already
 * use (in role order), then the common ids, each once. `current` (a typed id) is kept first-come.
 */
export function modelSuggestions(roles: ScopedRole[], runtime: Runtime, current = ""): string[] {
  const out: string[] = [];
  const add = (id: string | undefined) => {
    const v = (id ?? "").trim();
    if (v && !out.includes(v)) out.push(v);
  };
  for (const r of roles) if (runtimesOf(r).includes(runtime)) add(scopedModel(r, runtime));
  for (const m of COMMON_MODELS[runtime]) add(m.id);
  add(current);
  return out;
}

/** Which project model field covers a role: coordinator_model, or the thread field of its runtime (leads and members). */
export type ProjectField = "coordinator" | Runtime;

/** The roles a project model field covers: the coordinator; or the leads and members of one runtime. */
export function scopeRoles<R extends ScopedRole>(roles: R[], field: ProjectField): R[] {
  if (field === "coordinator") return roles.filter((r) => r.tier === "coordinator");
  return roles.filter((r) => r.tier !== "coordinator" && runtimesOf(r).includes(field));
}

/** The model names the covered roles run with (each role's model, else the built-in default), once each. */
export function roleModelNames(roles: ScopedRole[], field: ProjectField): string[] {
  const out: string[] = [];
  for (const r of scopeRoles(roles, field)) {
    const runtime = field === "coordinator" ? (r.runtime as Runtime) || "claude" : field;
    const m = scopedModel(r, runtime);
    const name = m ? modelName(m) : builtinName(runtime);
    if (!out.includes(name)) out.push(name);
  }
  return out;
}

/** A role's runtimes (the old single runtime when it has no list). */
function runtimesOf(r: ScopedRole): string[] {
  const one = r.runtime || "claude";
  const list = r.runtimes ?? [];
  return list.length === 0 || (list.length === 1 && list[0] !== one) ? [one] : list;
}

/** A role's model for one of its runtimes: model for the first, models[rt] for the others. */
function scopedModel(r: ScopedRole, runtime: string): string | undefined {
  return runtime === runtimesOf(r)[0] ? r.model : (r.models?.[runtime] ?? undefined);
}

/** The name of a runtime's built-in model, for the role lists. */
function builtinName(runtime: Runtime): string {
  const s = runtimeSpec(runtime);
  return s.builtinModel ? `内置默认 ${modelName(s.builtinModel)}` : `${s.label} 默认`;
}

/** The effective state of a project model field: 项目指定：X, or 跟随岗位配置（岗位：…）. */
export function projectModelStatus(value: string, roles: ScopedRole[], field: ProjectField): string {
  if (value) return `项目指定：${modelName(value)}`;
  const names = roleModelNames(roles, field);
  return `跟随岗位配置（${names.length ? `岗位：${names.join("、")}` : "没有这类岗位"}）`;
}

/** Whether to offer the Codex field: there is a Codex role, or the field is set. */
export function offersCodex(roles: ScopedRole[], value = ""): boolean {
  return Boolean(value) || scopeRoles(roles, "codex").length > 0;
}
