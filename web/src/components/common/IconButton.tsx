import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const ICON = "flex size-8 shrink-0 items-center justify-center rounded-lg text-[#4A4843] transition-colors hover:bg-raised dark:text-foreground/80 [&_svg]:size-[17px] data-[state=open]:bg-raised";

function WithTip({ label, children }: { label: string; children: ReactNode }) {
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>{children}</TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

type Props = ButtonHTMLAttributes<HTMLButtonElement> & { label: string; active?: boolean; children: ReactNode };

/** A compact 32px icon button whose label shows as a tooltip (and is its accessible name). */
export const IconButton = forwardRef<HTMLButtonElement, Props>(function IconButton({ label, active, className, children, ...props }, ref) {
  return (
    <WithTip label={label}>
      <button ref={ref} type="button" aria-label={label} aria-pressed={active} className={cn(ICON, active && "bg-raised text-foreground", className)} {...props}>
        {children}
      </button>
    </WithTip>
  );
});

/** The same look for a link to another page. */
export function IconLink({ label, to, className, children }: { label: string; to: string; className?: string; children: ReactNode }) {
  return (
    <WithTip label={label}>
      <Link to={to} aria-label={label} className={cn(ICON, className)}>
        {children}
      </Link>
    </WithTip>
  );
}
