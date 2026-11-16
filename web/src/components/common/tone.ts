export type Tone = "ok" | "info" | "warn" | "bad" | "neutral" | "primary";

export const BADGE_TONE: Record<Tone, string> = {
  neutral: "border-input text-muted-foreground",
  ok: "border-ok/50 text-ok",
  info: "border-info/50 text-info",
  warn: "border-warn/50 text-warn",
  bad: "border-bad/50 text-bad",
  primary: "border-brand/50 text-brand",
};

export const DOT_TONE: Record<Tone, string> = { ok: "bg-ok", info: "bg-info", warn: "bg-warn", bad: "bg-bad", neutral: "bg-muted-foreground", primary: "bg-brand" };
