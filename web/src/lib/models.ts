// Models (pure). There is no model list: every role (coordinator and lead too) and the project
// settings name their model by hand. The fields suggest the models the roles already use plus a
// few common ids, and accept any id in the server's format (composer.ValidModel).
export type Runtime = "claude" | "codex";

/** Same rule as the server (composer.ValidModel). */
export const MODEL_ID = /^[A-Za-z0-9][A-Za-z0-9._:/@[\]-]{0,99}$/;

/** What a Claude session runs when neither the project nor the role names a model (orchestrator.DefaultModel). */
export const BUILTIN_MODEL = "claude-opus-5-5";

/** Common ids offered first, per runtime, with their display names. */
export const COMMON_MODELS: Record<Runtime, { id: string; label: string }[]> = {
  claude: [
    { id: "claude-opus-5-5", label: "Opus 5.5" },
    { id: "claude-fable-5-2", label: "Fable 5.2" },
    { id: "claude-sonnet-5-5", label: "Sonnet 5.5" },
  ],
  codex: [],
};

export const runtimeLabel = (r: string) => (r === "codex" ? "Codex" : "Claude Code");

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

/** What a role's empty model falls back to, for the placeholder. */
export function emptyRoleModel(runtime: Runtime): string {
  return runtime === "codex" ? "留空 = 使用 Codex 自己的默认模型" : `留空 = 内置默认 ${BUILTIN_MODEL}`;
}

interface ScopedRole {
  tier: string;
  runtime: string;
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
  for (const r of roles) if ((r.runtime || "claude") === runtime) add(r.model);
  for (const m of COMMON_MODELS[runtime]) add(m.id);
  add(current);
  return out;
}

/** Which project model field covers a role: coordinator_model, or the thread field of its runtime (leads and members). */
export type ProjectField = "coordinator" | Runtime;

/** The roles a project model field covers: the coordinator; or the leads and members of one runtime. */
export function scopeRoles<R extends ScopedRole>(roles: R[], field: ProjectField): R[] {
  if (field === "coordinator") return roles.filter((r) => r.tier === "coordinator");
  return roles.filter((r) => r.tier !== "coordinator" && (r.runtime || "claude") === field);
}

/** The model names the covered roles run with (each role's model, else the built-in default), once each. */
export function roleModelNames(roles: ScopedRole[], field: ProjectField): string[] {
  const out: string[] = [];
  for (const r of scopeRoles(roles, field)) {
    const runtime = field === "coordinator" ? (r.runtime as Runtime) || "claude" : field;
    const name = r.model ? modelName(r.model) : runtime === "codex" ? "Codex 默认" : `内置默认 ${modelName(BUILTIN_MODEL)}`;
    if (!out.includes(name)) out.push(name);
  }
  return out;
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
