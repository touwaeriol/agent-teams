// The agent runtimes employees run on (pure), mirroring the control plane's registry
// (server/internal/runtimes). Pages read a runtime's capability here instead of comparing ids, so
// a new runtime is one more entry (plus its own texts where a page has some).
export type Runtime = "claude" | "codex";

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
}

export const RUNTIME_SPECS: RuntimeSpec[] = [
  { id: "claude", label: "Claude Code", slashCommands: true, builtinModel: "claude-opus-5-5", subscriptionAuth: "subscription", installHint: "安装后登录" },
  { id: "codex", label: "Codex", slashCommands: false, builtinModel: "", subscriptionAuth: "chatgpt", installHint: "安装后登录或设置 CODEX_API_KEY" },
];

/** The spec of a runtime id; "" or an unknown id is the default runtime (the first). */
export function runtimeSpec(id: string | null | undefined): RuntimeSpec {
  return RUNTIME_SPECS.find((r) => r.id === id) ?? RUNTIME_SPECS[0]!;
}

export const runtimeLabel = (id: string | null | undefined) => runtimeSpec(id).label;
