// Small building blocks of the v2 screens (pills, dots, checklist marks,
// composers). Colours come from lib/present.ts so states look the same everywhere.
import { ArrowUp } from "lucide-react";
import { useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { toast } from "sonner";
import { AttachButton, attachmentHandlers, AttachmentStrip, type Attachments } from "./attachments";
import { Spinner } from "@/components/common";
import { useDraft } from "@/lib/persist";
import { cn } from "@/lib/utils";
import type { MainTaskStatus } from "@/api/views";

export function Dot({ color, size = 7, className }: { color: string; size?: number; className?: string }) {
  return <span aria-hidden className={cn("inline-block shrink-0 rounded-full", className)} style={{ width: size, height: size, background: color }} />;
}

/** The in-progress mark: a ring with a rotating arc (`.at-spin`; a gentle pulse with reduced motion). */
export function Spin({ color, size = 8, className, label }: { color: string; size?: number; className?: string; label?: string }) {
  const px = Math.max(size, 8);
  return (
    <svg
      width={px}
      height={px}
      viewBox="0 0 16 16"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      data-spin=""
      className={cn("at-spin shrink-0", className)}
    >
      <circle cx="8" cy="8" r="6" fill="none" stroke={color} strokeOpacity="0.22" strokeWidth="2.6" />
      <path d="M8 2a6 6 0 0 1 6 6" fill="none" stroke={color} strokeWidth="2.6" strokeLinecap="round" />
    </svg>
  );
}

/** Someone working: a spinner; otherwise a static dot. */
export function LiveDot({ color, on, size = 8, className }: { color: string; on: boolean; size?: number; className?: string }) {
  if (on) return <Spin color={color} size={size + 2} className={className} />;
  return <Dot color={color} size={size} className={className} />;
}

/** A state's mark in lists: spinner while in progress, static dot otherwise. */
export function StateMark({ look, size = 7 }: { look: { tone: string; spin?: boolean }; size?: number }) {
  return look.spin ? <Spin color={look.tone} size={size + 3} /> : <Dot color={look.tone} size={size} />;
}

/** Outlined status pill (进行中 / 等你 / 待验收 …); in-progress pills lead with a spinner. */
export function StatusPill({ tone, label, className, spin }: { tone: string; label: ReactNode; className?: string; spin?: boolean }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs", className)} style={{ borderColor: tone, color: tone }}>
      {spin && <Spin color={tone} size={10} />}
      {label}
    </span>
  );
}

/** Grey tag (branch, worktree, node…). */
export function Tag({ children, mono, className }: { children: ReactNode; mono?: boolean; className?: string }) {
  return <span className={cn("rounded-md bg-sidebar px-2 py-[3px] text-xs text-[#4A4843]", mono && "font-mono", className)}>{children}</span>;
}

export function SectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("text-xs text-muted-foreground", className)}>{children}</span>;
}

/** The checklist mark of a main task: filled tick, spinning arc, dashed circle. */
export function CheckMark({ status, size = 15 }: { status: MainTaskStatus; size?: number }) {
  if (status === "done")
    return (
      <svg width={size} height={size} viewBox="0 0 16 16" aria-label="已完成" className="shrink-0">
        <circle cx="8" cy="8" r="8" fill="#8A877F" />
        <path d="M4.6 8.2l2.2 2.2 4.6-4.8" fill="none" stroke="#FFFFFF" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    );
  if (status === "doing")
    return (
      <svg width={size} height={size} viewBox="0 0 16 16" role="img" aria-label="进行中" data-spin="" className="at-spin shrink-0">
        <circle cx="8" cy="8" r="6.5" fill="none" stroke="#E7E5E0" strokeWidth="1.6" />
        <path d="M8 1.5a6.5 6.5 0 0 1 6.5 6.5" fill="none" stroke="#4A4843" strokeWidth="1.6" strokeLinecap="round" />
      </svg>
    );
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-label="未开始" className="shrink-0">
      <circle cx="8" cy="8" r="6.5" fill="none" stroke="#B7B3AA" strokeWidth="1.4" strokeDasharray="2 2.4" />
    </svg>
  );
}

/** Thin progress bar. */
export function Bar({ pct, tone, height = 4 }: { pct: string; tone: string; height?: number }) {
  return (
    <span className="flex flex-1 rounded-sm bg-[#EFEDE8]" style={{ height }}>
      <span className="rounded-sm" style={{ width: pct, background: tone }} />
    </span>
  );
}

/** Ink square send button. */
export function SendButton({ label, busy, disabled, size = 32 }: { label: string; busy?: boolean; disabled?: boolean; size?: number }) {
  return (
    <button
      type="submit"
      aria-label={label}
      disabled={disabled || busy}
      className="flex shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground transition-opacity disabled:opacity-40"
      style={{ width: size, height: size }}
    >
      {busy ? <Spinner className="size-4" /> : <ArrowUp className="size-4" strokeWidth={2} />}
    </button>
  );
}

interface ComposerProps {
  label: string;
  placeholder: string;
  onSend: (text: string, images: string[]) => Promise<unknown> | void;
  busy?: boolean;
  /** Image input (paste, drop, picker); omit for text-only boxes. */
  attachments?: Attachments;
  hint?: ReactNode;
  extra?: ReactNode;
  variant?: "large" | "inline";
  className?: string;
  id?: string;
  /** Keeps the unsent text across reloads (localStorage), e.g. draft:<conversation>. */
  draftKey?: string;
}

/** Message box. `large`: the coordinator chat (textarea + hint row); `inline`: one-line box for the lead. Enter sends, Shift+Enter breaks. */
export function Composer({ label, placeholder, onSend, busy, hint, extra, variant = "large", className, id, attachments, draftKey }: ComposerProps) {
  const [text, setText] = useDraft(draftKey);
  const [dragging, setDragging] = useState(false);
  const imageCount = attachments?.items.filter((x) => x.id).length ?? 0;
  const empty = !text.trim() && imageCount === 0;
  const send = async (e?: FormEvent) => {
    e?.preventDefault();
    const value = text.trim();
    if (empty || busy) return;
    const images = attachments ? attachments.ids() : [];
    if (images === null) return void toast.info("图片还在上传，稍等一下");
    setText("");
    try {
      await onSend(value, images);
      attachments?.clear();
    } catch {
      setText(value);
    }
  };
  const drop = attachmentHandlers(attachments, setDragging);
  const ring = dragging ? "ring-2 ring-[#2F6FDB]/40 border-[#2F6FDB]" : "";
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement | HTMLInputElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void send();
    }
  };
  const inputId = id ?? `composer-${label}`;
  if (variant === "inline")
    return (
      <form onSubmit={send} {...drop} className={cn("flex flex-col gap-2 rounded-xl border border-[#E2DFD8] bg-panel py-1.5 pl-3 pr-1.5", ring, className)}>
        {attachments && <AttachmentStrip a={attachments} className="pt-1" />}
        <span className="flex items-center gap-2">
          <label htmlFor={inputId} className="sr-only">
            {label}
          </label>
          <input
            id={inputId}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            placeholder={placeholder}
            className="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground/80"
          />
          {attachments && <AttachButton a={attachments} size={30} />}
          <SendButton label={label} busy={busy || attachments?.uploading} disabled={empty} size={30} />
        </span>
      </form>
    );
  return (
    <form onSubmit={send} {...drop} className={cn("rounded-2xl border border-[#E2DFD8] bg-panel shadow-[0_2px_12px_-4px_rgba(0,0,0,0.08)]", ring, className)}>
      {attachments && <AttachmentStrip a={attachments} className="px-4 pt-3" />}
      <label htmlFor={inputId} className="sr-only">
        {label}
      </label>
      <textarea
        id={inputId}
        rows={2}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKey}
        placeholder={placeholder}
        className="block w-full resize-none bg-transparent px-4 pt-3.5 text-[15px] leading-6 outline-none placeholder:text-muted-foreground/80"
      />
      <div className="flex items-center justify-between gap-2 px-3 pb-3 pt-1.5">
        <span className="text-xs text-muted-foreground">{hint}</span>
        <span className="flex items-center gap-2">
          {attachments && <AttachButton a={attachments} />}
          {extra}
          <SendButton label="发送" busy={busy || attachments?.uploading} disabled={empty} />
        </span>
      </div>
    </form>
  );
}

/** A white card with the design's hairline border. */
export function Card({ children, className, tone, as: Tag = "section" }: { children: ReactNode; className?: string; tone?: "ask" | "danger"; as?: "section" | "div" | "article" | "aside" }) {
  const border = tone === "ask" ? "border-[#F0C9A8]" : tone === "danger" ? "border-[#F1C8C4]" : "border-border";
  return <Tag className={cn("flex flex-col rounded-[14px] border bg-panel p-5", border, className)}>{children}</Tag>;
}

export function CardTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <h2 className="text-[15px] font-semibold">{children}</h2>
      {action}
    </div>
  );
}

/** Segmented choice used for work mode (worktree / clone). */
export function ChoiceCards<T extends string>({ value, onChange, options, label }: { value: T; onChange: (v: T) => void; options: { id: T; title: string; desc: string }[]; label: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="grid gap-3 sm:grid-cols-2">
      {options.map((o) => {
        const on = o.id === value;
        return (
          <button
            key={o.id}
            type="button"
            role="radio"
            aria-checked={on}
            onClick={() => onChange(o.id)}
            className={cn("flex flex-col gap-1.5 rounded-xl border-[1.5px] px-4 py-3.5 text-left", on ? "border-foreground bg-background" : "border-border bg-panel hover:border-input")}
          >
            <span className="font-semibold">{o.title}</span>
            <span className="text-xs leading-[19px] text-[#4A4843]">{o.desc}</span>
          </button>
        );
      })}
    </div>
  );
}

export const WORK_MODES = [
  { id: "worktree" as const, title: "worktree（推荐）", desc: "每个节点只克隆一份仓库；每个讨论串一个 worktree，讨论串里的每个成员再各开一个。省磁盘、开工快，并行互不干扰。" },
  { id: "clone" as const, title: "独立克隆", desc: "每个成员一份完整克隆。最稳，但占磁盘，适合超大仓库或子模块很多的情况。" },
];

/** Initial-letter tile. */
export function Initial({ name, bg, fg, size = 22, round }: { name: string; bg: string; fg: string; size?: number; round?: boolean }) {
  return (
    <span
      aria-hidden
      className={cn("flex shrink-0 items-center justify-center font-semibold", round ? "rounded-full" : "rounded-md")}
      style={{ width: size, height: size, background: bg, color: fg, fontSize: size >= 30 ? 14 : 12 }}
    >
      {(Array.from(name.trim())[0] ?? "?").toUpperCase()}
    </span>
  );
}
