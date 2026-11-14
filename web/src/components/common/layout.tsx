import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Logo of the platform: four tiles. */
export function Logo({ className }: { className?: string }) {
  return (
    <svg className={className} width="26" height="26" viewBox="0 0 26 26" aria-hidden="true">
      <rect x="1" y="1" width="11" height="11" rx="2" fill="#f0954a" />
      <rect x="14" y="1" width="11" height="11" rx="2" fill="#4fc39a" />
      <rect x="1" y="14" width="11" height="11" rx="2" fill="#7aa8ff" />
      <rect x="14" y="14" width="11" height="11" rx="2" fill="#3b4258" />
    </svg>
  );
}

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div className="flex min-w-0 flex-col gap-1">
        <h1 className="text-[22px] font-semibold tracking-tight">{title}</h1>
        {description && <p className="max-w-3xl text-[13px] leading-5 text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}

const WIDTH = { full: "", narrow: "max-w-3xl" };

/** Every page: centered container, header, then its content in a column. */
export function Page({
  title,
  description,
  actions,
  width = "full",
  before,
  children,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  width?: keyof typeof WIDTH;
  before?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className={cn("container flex flex-col gap-5 py-6", WIDTH[width])}>
      {before}
      {title && <PageHeader title={title} description={description} actions={actions} />}
      {children}
    </div>
  );
}

/** Main column plus a side column that stacks below it on narrow screens. */
export function SplitLayout({ main, side, sideWidth = "400px", className }: { main: ReactNode; side: ReactNode; sideWidth?: string; className?: string }) {
  return (
    <div className={cn("flex flex-wrap items-start gap-5", className)}>
      <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-4">{main}</div>
      <aside className="w-full min-w-0 lg:sticky lg:top-24" style={{ flex: `1 1 ${sideWidth}` }}>
        {side}
      </aside>
    </div>
  );
}

const PANEL_TONE = { default: "", highlight: "border-primary/60", danger: "border-destructive/30" };

/**
 * The surface every block sits on. With ``title`` it gets a header row
 * (icon, title, optional action on the right).
 */
export function Panel({
  title,
  icon,
  description,
  action,
  tone = "default",
  padding = "md",
  as: Tag = "section",
  label,
  className,
  children,
}: {
  title?: ReactNode;
  icon?: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  tone?: keyof typeof PANEL_TONE;
  padding?: "sm" | "md" | "none";
  as?: "section" | "article" | "div" | "aside" | "nav";
  label?: string;
  className?: string;
  children?: ReactNode;
}) {
  const pad = { sm: "p-4 gap-2.5", md: "p-5 gap-4", none: "" }[padding];
  return (
    <Tag aria-label={label} className={cn("flex flex-col rounded-xl border bg-panel", pad, PANEL_TONE[tone], className)}>
      {(title || action) && (
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex min-w-0 flex-col gap-1">
            {title && (
              <h2 className="flex items-center gap-2 text-[15px] font-semibold [&_svg]:size-4 [&_svg]:text-muted-foreground">
                {icon}
                {title}
              </h2>
            )}
            {description && <p className="text-[13px] leading-5 text-muted-foreground">{description}</p>}
          </div>
          {action}
        </div>
      )}
      {children}
    </Tag>
  );
}
