import { X } from "lucide-react";
import { useState, type KeyboardEvent } from "react";
import { cn } from "@/lib/utils";

/** Free-form list of short strings: Enter or comma adds, Backspace removes the last. */
export function TagInput({ values, onChange, placeholder, id, className }: { values: string[]; onChange: (v: string[]) => void; placeholder?: string; id?: string; className?: string }) {
  const [text, setText] = useState("");
  const commit = () => {
    const items = text.split(/[,，]/).map((s) => s.trim()).filter(Boolean);
    if (items.length) onChange([...values, ...items.filter((i) => !values.includes(i))]);
    setText("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && !e.nativeEvent.isComposing) {
      e.preventDefault();
      commit();
    } else if (e.key === "Backspace" && !text && values.length) {
      onChange(values.slice(0, -1));
    }
  };
  return (
    <div className={cn("flex min-h-10 flex-wrap items-center gap-1.5 rounded-lg border border-input bg-well px-2 py-1.5 focus-within:ring-2 focus-within:ring-ring/60", className)}>
      {values.map((v) => (
        <span key={v} className="flex items-center gap-1 rounded-md bg-raised px-2 py-0.5 font-mono text-xs">
          {v}
          <button type="button" onClick={() => onChange(values.filter((x) => x !== v))} aria-label={`移除 ${v}`} className="text-muted-foreground hover:text-foreground">
            <X className="size-3" />
          </button>
        </span>
      ))}
      <input
        id={id}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKey}
        onBlur={commit}
        placeholder={values.length ? "" : placeholder}
        className="min-w-[140px] flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-muted-foreground/70"
      />
    </div>
  );
}
