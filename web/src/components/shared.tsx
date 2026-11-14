import { Check, Copy } from "lucide-react";
import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

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

export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
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

export function CopyBlock({ text, className }: { text: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    toast.success("已复制");
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <div className={cn("relative rounded-lg border bg-well", className)}>
      <pre className="scroll-thin overflow-x-auto whitespace-pre-wrap break-all p-3 pr-12 font-mono text-xs leading-5">{text}</pre>
      <Button type="button" variant="ghost" size="icon" className="absolute right-1.5 top-1.5" onClick={copy} aria-label="复制">
        {copied ? <Check /> : <Copy />}
      </Button>
    </div>
  );
}

export function Field({ label, hint, children, htmlFor }: { label: string; hint?: ReactNode; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="text-[13px] font-medium text-foreground/85">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs leading-5 text-muted-foreground">{hint}</p>}
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <span className={cn("inline-block size-4 animate-spin rounded-full border-2 border-current border-r-transparent", className)} aria-label="加载中" />;
}

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
