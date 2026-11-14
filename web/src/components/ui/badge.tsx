import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium whitespace-nowrap",
  {
    variants: {
      tone: {
        neutral: "border-input text-muted-foreground",
        ok: "border-ok/50 text-ok",
        info: "border-info/50 text-info",
        warn: "border-warn/50 text-warn",
        bad: "border-bad/50 text-bad",
        primary: "border-primary/50 text-primary",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

function Badge({ className, tone, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ tone }), className)} {...props} />;
}

function Dot({ tone = "neutral", className }: { tone?: "ok" | "info" | "warn" | "bad" | "neutral"; className?: string }) {
  const color = { ok: "bg-ok", info: "bg-info", warn: "bg-warn", bad: "bg-bad", neutral: "bg-muted-foreground" }[tone];
  return <span aria-hidden className={cn("inline-block size-2 shrink-0 rounded-full", color, className)} />;
}

export { Badge, Dot };
