import { Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button, type ButtonProps } from "@/components/ui/button";

/**
 * A button that asks before doing something that cannot be undone.
 * ``children`` is the trigger's content; defaults to a delete button.
 */
export function ConfirmAction({
  title,
  description,
  confirmLabel = "删除",
  onConfirm,
  variant = "destructive",
  size,
  iconOnly,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  confirmLabel?: string;
  onConfirm: () => void;
  variant?: ButtonProps["variant"];
  size?: ButtonProps["size"];
  iconOnly?: string; // aria-label: render a bare trash icon button
  children?: ReactNode;
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        {iconOnly ? (
          <Button variant="ghost" size="icon" aria-label={iconOnly}>
            <Trash2 />
          </Button>
        ) : (
          <Button variant={variant} size={size}>
            {children ?? (
              <>
                <Trash2 /> {confirmLabel}
              </>
            )}
          </Button>
        )}
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && <AlertDialogDescription>{description}</AlertDialogDescription>}
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>{confirmLabel}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** A danger-zone row: explanation on the left, the confirmed action on the right. */
export function DangerZone({ title, description, children }: { title: ReactNode; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/30 bg-panel p-5">
      <div>
        <h2 className="font-semibold">{title}</h2>
        {description && <p className="text-[13px] text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  );
}
