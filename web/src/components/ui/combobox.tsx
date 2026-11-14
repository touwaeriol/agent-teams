import { Check, ChevronsUpDown } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cn } from "@/lib/utils";

export interface ComboOption {
  value: string;
  label?: ReactNode;
  hint?: ReactNode;
  keywords?: string;
}

/**
 * A select you can type into: the list filters as you type, arrows and Enter
 * pick, and (with ``allowCustom``) whatever was typed can be used as is.
 */
export function Combobox({
  value,
  onChange,
  options,
  placeholder,
  loading,
  empty = "没有匹配的选项",
  allowCustom,
  disabled,
  id,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  options: ComboOption[];
  placeholder?: string;
  loading?: boolean;
  empty?: ReactNode;
  allowCustom?: boolean;
  disabled?: boolean;
  id?: string;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState(value);
  const [active, setActive] = useState(0);
  const list = useRef<HTMLUListElement>(null);
  useEffect(() => setText(value), [value]);

  const shown = useMemo(() => {
    const needle = text.trim().toLowerCase();
    const typing = needle && needle !== value.toLowerCase();
    return options.filter((o) => !typing || `${o.value} ${o.keywords ?? ""}`.toLowerCase().includes(needle)).slice(0, 100);
  }, [options, text, value]);

  useEffect(() => setActive(0), [text, open]);
  useEffect(() => {
    list.current?.children[active]?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const pick = (next: string) => {
    onChange(next);
    setText(next);
    setOpen(false);
  };
  const close = () => {
    setOpen(false);
    if (allowCustom && text.trim() && text !== value) onChange(text.trim());
    else setText(value);
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    const moves: Record<string, () => void> = {
      ArrowDown: () => (open ? setActive((a) => Math.min(a + 1, shown.length - 1)) : setOpen(true)),
      ArrowUp: () => setActive((a) => Math.max(a - 1, 0)),
      Enter: () => (shown[active] ? pick(shown[active].value) : allowCustom && text.trim() && pick(text.trim())),
      Escape: () => close(),
    };
    const move = moves[e.key];
    if (!move) return;
    e.preventDefault();
    move();
  };

  return (
    <div className={cn("relative", className)}>
      <input
        id={id}
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        autoComplete="off"
        disabled={disabled}
        value={text}
        placeholder={placeholder}
        onChange={(e) => {
          setText(e.target.value);
          setOpen(true);
        }}
        onFocus={(e) => {
          e.target.select();
          setOpen(true);
        }}
        onBlur={close}
        onKeyDown={onKey}
        className="flex h-10 w-full rounded-lg border border-input bg-well py-2 pl-3 pr-9 text-sm text-foreground placeholder:text-muted-foreground/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50"
      />
      <ChevronsUpDown className="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
      {open && !disabled && (
        <ul
          ref={list}
          role="listbox"
          className="scroll-thin absolute z-50 mt-1 max-h-64 w-full overflow-y-auto rounded-lg border bg-popover p-1 text-popover-foreground shadow-lg"
        >
          {loading && <li className="px-2.5 py-2 text-xs text-muted-foreground">加载中……</li>}
          {!loading && shown.length === 0 && (
            <li className="px-2.5 py-2 text-xs text-muted-foreground">{allowCustom && text.trim() ? <>按 Enter 使用“{text.trim()}”</> : empty}</li>
          )}
          {shown.map((o, i) => (
            <li
              key={o.value}
              role="option"
              aria-selected={o.value === value}
              onMouseDown={(e) => {
                e.preventDefault();
                pick(o.value);
              }}
              onMouseEnter={() => setActive(i)}
              className={cn("flex cursor-default items-center gap-2 rounded-md px-2.5 py-2 text-sm", i === active && "bg-raised")}
            >
              <span className="min-w-0 flex-1 truncate">{o.label ?? o.value}</span>
              {o.hint && <span className="shrink-0 text-xs text-muted-foreground">{o.hint}</span>}
              {o.value === value && <Check className="size-4 shrink-0" />}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
