// Drag handle between the chat and the right panel: drag to resize, arrow keys nudge,
// double-click resets. The width is clamped and remembered per user (lib/panelWidth.ts).
import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { clampWidth, dragWidth, loadWidth, PANEL_DEFAULT, saveWidth } from "@/lib/panelWidth";
import { cn } from "@/lib/utils";

/** The panel width for a user, kept within the window as it resizes. */
export function usePanelWidth(userId: string): [number, (w: number) => void] {
  const viewport = () => (typeof window === "undefined" ? 1440 : window.innerWidth);
  const [width, setWidth] = useState(() => clampWidth(loadWidth(userId), viewport()));
  useEffect(() => {
    const onResize = () => setWidth((w) => clampWidth(w, viewport()));
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  const set = (w: number) => {
    const next = clampWidth(w, viewport());
    setWidth(next);
    saveWidth(userId, next);
  };
  return [width, set];
}

export function ResizeHandle({ width, onChange, label }: { width: number; onChange: (w: number) => void; label: string }) {
  const drag = useRef<{ x: number; w: number } | null>(null);
  const [active, setActive] = useState(false);
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    drag.current = { x: e.clientX, w: width };
    setActive(true);
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (drag.current) onChange(dragWidth(drag.current.w, drag.current.x, e.clientX, window.innerWidth));
  };
  const end = () => {
    drag.current = null;
    setActive(false);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 64 : 16;
    if (e.key === "ArrowLeft") onChange(width + step);
    else if (e.key === "ArrowRight") onChange(width - step);
    else if (e.key === "Home" || e.key === "Enter") onChange(PANEL_DEFAULT);
    else return;
    e.preventDefault();
  };
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={width}
      tabIndex={0}
      title="拖动调整宽度，双击恢复默认"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={end}
      onPointerCancel={end}
      onDoubleClick={() => onChange(PANEL_DEFAULT)}
      onKeyDown={onKeyDown}
      className="group relative z-10 -mr-[3px] hidden w-[6px] shrink-0 cursor-col-resize touch-none outline-none lg:block"
    >
      <span className={cn("absolute inset-y-0 left-[2px] w-[2px] transition-colors group-hover:bg-[#2F6FDB]/50 group-focus-visible:bg-[#2F6FDB]", active && "bg-[#2F6FDB]")} />
    </div>
  );
}
