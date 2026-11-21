// The agent runtimes employees run on (pure), mirroring the control plane's registry
// (server/internal/runtimes). Pages read a runtime's capability here instead of comparing ids, so
// a new runtime is one more entry (plus its own texts where a page has some).
export type Runtime = "claude" | "codex" | "antigravity";

export interface RuntimeSpec {
  id: Runtime;
  label: string;
  /** Runs "/name" as a command (passthrough commands, slash skills); otherwise prompt templates only. */
  slashCommands: boolean;
  /** The model when no level names one ("" = the runtime's own default). */
  builtinModel: string;
  /** hello.runtimes auth value of a subscription login that has a quota to show. */
  subscriptionAuth: string;
  /** What to do on the nodes page when the runtime is not installed. */
  installHint: string;
  /** May be a coordinator or a thread lead (server runtimes.CanLead). */
  canLead: boolean;
  /** The nodes page's text when the runtime is installed but not logged in. */
  loginHint: string;
  /** Shown on a read-only role of this runtime when its read-only is not a sandbox. */
  readOnlyCaveat: string;
}

export const RUNTIME_SPECS: RuntimeSpec[] = [
  { id: "claude", label: "Claude Code", slashCommands: true, builtinModel: "claude-opus-5-5", subscriptionAuth: "subscription", installHint: "安装后登录", canLead: true, loginHint: "未登录", readOnlyCaveat: "" },
  { id: "codex", label: "Codex", slashCommands: false, builtinModel: "", subscriptionAuth: "chatgpt", installHint: "安装后登录或设置 CODEX_API_KEY", canLead: false, loginHint: "未登录", readOnlyCaveat: "" },
  // Google Antigravity CLI (agy over ACP): no usage API, so no subscription quota to show.
  { id: "antigravity", label: "Antigravity", slashCommands: false, builtinModel: "", subscriptionAuth: "", installHint: "安装 agy 后运行 agy acp --login 登录",
    // The daemon re-checks agy's login when its token files change (node.runtimes), no restart needed.
    canLead: false, loginHint: "未登录 · 用节点的 agy HOME 运行 agy acp --login", readOnlyCaveat: "Antigravity 的只读靠禁用写类工具和拒绝写权限实现，不是沙箱级保证" },
];

/** The spec of a runtime id; "" or an unknown id is the default runtime (the first). */
export function runtimeSpec(id: string | null | undefined): RuntimeSpec {
  return RUNTIME_SPECS.find((r) => r.id === id) ?? RUNTIME_SPECS[0]!;
}

export const runtimeLabel = (id: string | null | undefined) => runtimeSpec(id).label;

type RuntimesOf = { runtime: Runtime; runtimes?: Runtime[] | null };

/**
 * A role's runtimes: runtimes, or just runtime for a role saved before roles had several or one
 * whose runtime was changed next to a single-entry list (the server's rule, roles.normalizeRuntimes).
 */
export function roleRuntimes(r: RuntimesOf): Runtime[] {
  const list = r.runtimes ?? [];
  if (list.length === 0 || (list.length === 1 && list[0] !== r.runtime)) return [r.runtime];
  return list;
}

/** The runtimes a role of this tier may pick: members any, the coordinator and the lead only those that can lead. */
export function runtimesFor(tier: string): RuntimeSpec[] {
  return tier === "member" ? RUNTIME_SPECS : RUNTIME_SPECS.filter((r) => r.canLead);
}

/** The first runtime of the role without slash commands (undefined: all have them). */
export const promptRuntime = (r: RuntimesOf) => roleRuntimes(r).find((id) => !runtimeSpec(id).slashCommands);

/** Whether every runtime of the role runs slash commands. */
export const allSlash = (r: RuntimesOf) => roleRuntimes(r).every((id) => runtimeSpec(id).slashCommands);

type ModelsOf = RuntimesOf & { model?: string | null; models?: Record<string, string> | null };

/** The role's model for one of its runtimes: model for the first, models[rt] for the others. */
export function modelOf(r: ModelsOf, rt: Runtime): string {
  return (rt === roleRuntimes(r)[0] ? r.model : r.models?.[rt]) ?? "";
}

/** The patch that sets a role's runtimes (at least one, in this order); each runtime keeps its model. */
export function runtimesPatch(r: ModelsOf, list: Runtime[]): { runtime: Runtime; runtimes: Runtime[]; model: string; models: Record<string, string> } | null {
  if (list.length === 0) return null;
  const models: Record<string, string> = {};
  for (const rt of list.slice(1)) {
    const m = modelOf(r, rt);
    if (m) models[rt] = m;
  }
  return { runtime: list[0]!, runtimes: list, model: modelOf(r, list[0]!), models };
}

/** The patch that sets the role's model for one runtime. */
export function modelPatch(r: ModelsOf, rt: Runtime, model: string): { model: string } | { models: Record<string, string> } {
  if (rt === roleRuntimes(r)[0]) return { model };
  return { models: { ...(r.models ?? {}), [rt]: model } };
}
