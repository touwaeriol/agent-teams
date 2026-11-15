// Employee look preview (Avatar board). Drawn from the office layout's
// character and looks with the office renderer's own composer, so the Team
// page shows exactly what the office will draw.
import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { BUILTIN_LAYOUT, composeFrame, loadLayout, resolveLook, type OfficeLayout } from "@/office";
import type { Look } from "@/api/views";

/** The layout in effect (default unless a project picks another); shared by every avatar on the page. */
export function useOfficeLayout(id?: string): OfficeLayout {
  const q = useQuery({ queryKey: ["office-layout", id ?? "default"], queryFn: () => loadLayout(id), staleTime: 60_000, retry: false });
  return q.data?.layout ?? BUILTIN_LAYOUT;
}

export interface AvatarProps {
  layout: OfficeLayout;
  look: Partial<Look>;
  role: string;
  tier: string;
  label: string;
  /** Art pixel size in CSS px. */
  scale?: number;
  raised?: boolean;
}

export function Avatar({ layout, look, role, tier, label, scale = 4, raised }: AvatarProps) {
  const pixels = useMemo(() => {
    const resolved = resolveLook(layout, { id: label, name: label, tier, role, look: { ...look, hair: look.hair ?? undefined } });
    return composeFrame(layout, resolved, "stand", { overlay: raised ? "raiseHand" : undefined });
  }, [layout, look, role, tier, label, raised]);
  const w = Math.max(11, ...pixels.map((p) => p.x + 1));
  const h = Math.max(14, ...pixels.map((p) => p.y + 1));
  const tag = look.color ?? "#4A4843";
  return (
    <span className="relative flex flex-col items-center" style={{ width: Math.max(56, w * scale) }}>
      <svg width={w * scale} height={h * scale} viewBox={`0 0 ${w} ${h}`} shapeRendering="crispEdges" role="img" aria-label={`${label} 的形象`}>
        {pixels.map((p, i) => (
          <rect key={i} x={p.x} y={p.y} width={1} height={1} fill={p.color} />
        ))}
      </svg>
      <span className="-mt-0.5 max-w-[64px] truncate rounded border bg-white px-[5px] py-px text-[10px] leading-3 text-foreground" style={{ borderColor: tag }}>
        {label}
      </span>
    </span>
  );
}
