import { Check, Copy } from "lucide-react";
import { useId, useState, type FormEvent, type ReactElement, type ReactNode } from "react";
import { cloneElement, isValidElement } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { Spinner } from "./feedback";

/**
 * A labelled form control. The single child gets an ``id`` so the label is
 * bound to it, unless ``htmlFor`` is given.
 */
export function Field({ label, hint, htmlFor, className, children }: { label: ReactNode; hint?: ReactNode; htmlFor?: string; className?: string; children: ReactNode }) {
  const generated = useId();
  const element = isValidElement<{ id?: string }>(children) ? children : null;
  const id = htmlFor ?? element?.props.id ?? generated;
  const control = element && !element.props.id && !htmlFor ? cloneElement(element as ReactElement<{ id?: string }>, { id }) : children;
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <label htmlFor={id} className="text-[13px] font-medium text-foreground/85">
        {label}
      </label>
      {control}
      {hint && <p className="text-xs leading-5 text-muted-foreground">{hint}</p>}
    </div>
  );
}

/** A group of fields with a caption, on a slightly raised surface. */
export function FieldGroup({ legend, children, className }: { legend: ReactNode; children: ReactNode; className?: string }) {
  return (
    <fieldset className={cn("flex flex-col gap-3 rounded-xl border bg-raised/40 p-4", className)}>
      <legend className="px-1 text-[13px] font-medium text-foreground/85">{legend}</legend>
      {children}
    </fieldset>
  );
}

/** Advanced options folded away by default. */
export function Disclosure({ summary, children, defaultOpen }: { summary: ReactNode; children: ReactNode; defaultOpen?: boolean }) {
  return (
    <details className="group rounded-lg border bg-raised/40 p-3" open={defaultOpen}>
      <summary className="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{summary}</summary>
      <div className="mt-3 flex flex-col gap-4">{children}</div>
    </details>
  );
}

/** Read-only text to copy (commands, tokens). */
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

/** A modal form: title, fields, one submit button that shows progress. */
export function FormDialog({
  open,
  onOpenChange,
  title,
  description,
  submitLabel,
  busy,
  disabled,
  onSubmit,
  className,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  submitLabel: ReactNode;
  busy?: boolean;
  disabled?: boolean;
  onSubmit: () => void;
  className?: string;
  children: ReactNode;
}) {
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit();
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={className}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription>{description}</DialogDescription>}
          </DialogHeader>
          {children}
          <DialogFooter>
            <Button type="submit" disabled={disabled || busy}>
              {busy && <Spinner />} {submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** A dialog that only shows information, closed with one button. */
export function InfoDialog({ open, onClose, title, description, children, className }: { open: boolean; onClose: () => void; title: ReactNode; description?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={className}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {children}
        <DialogFooter>
          <Button onClick={onClose}>完成</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
