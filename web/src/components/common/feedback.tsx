import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Inline loading indicator; inherits the text color. */
export function Spinner({ className }: { className?: string }) {
  return (
    <span
      className={cn("inline-block size-4 animate-spin rounded-full border-2 border-current border-r-transparent", className)}
      role="status"
      aria-label="加载中"
    />
  );
}

/** A block that stands in for content while it loads. */
export function Loading({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center justify-center p-10 text-muted-foreground", className)}>
      <Spinner />
    </div>
  );
}

/** Nothing to show yet, with an optional next step. */
export function EmptyState({ icon, title, description, action }: { icon?: ReactNode; title: string; description?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed bg-panel/50 px-6 py-12 text-center">
      {icon && <div className="text-muted-foreground [&_svg]:size-8">{icon}</div>}
      <div className="text-[15px] font-semibold">{title}</div>
      {description && <p className="max-w-md text-[13px] leading-5 text-muted-foreground">{description}</p>}
      {action}
    </div>
  );
}

const CALLOUT = {
  info: "border-info/40 bg-info/10 text-info",
  warn: "border-warn/40 bg-warn/10 text-warn",
  bad: "border-bad/40 bg-bad/10 text-bad",
};

/** A one-line notice above content: hints, warnings and errors. */
export function Callout({ tone = "info", icon, children, className }: { tone?: keyof typeof CALLOUT; icon?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn("flex items-start gap-2 rounded-lg border px-3 py-2 text-[13px] leading-5 [&_a]:underline [&_svg]:mt-0.5 [&_svg]:size-4 [&_svg]:shrink-0", CALLOUT[tone], className)}>
      {icon}
      <div className="min-w-0">{children}</div>
    </div>
  );
}

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
