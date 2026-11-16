// The office layout in effect (default unless a project picks another); shared by every avatar on
// the page, so the Team page shows exactly what the office will draw.
import { useQuery } from "@tanstack/vue-query";
import { computed, type ComputedRef } from "vue";
import { BUILTIN_LAYOUT, composeFrame, loadLayout, resolveLook, type OfficeLayout } from "@/office";
import type { Look } from "@/api/views";

export function useOfficeLayout(id?: string): ComputedRef<OfficeLayout> {
  const q = useQuery({ queryKey: ["office-layout", id ?? "default"], queryFn: () => loadLayout(id), staleTime: 60_000, retry: false });
  return computed(() => q.data.value?.layout ?? BUILTIN_LAYOUT);
}

export interface AvatarInput {
  layout: OfficeLayout;
  look: Partial<Look>;
  role: string;
  tier: string;
  label: string;
  raised?: boolean;
}

/** The standing frame's pixels and the art size (at least 11 × 14). */
export function avatarPixels(a: AvatarInput) {
  const resolved = resolveLook(a.layout, { id: a.label, name: a.label, tier: a.tier, role: a.role, look: { ...a.look, hair: a.look.hair ?? undefined } });
  const pixels = composeFrame(a.layout, resolved, "stand", { overlay: a.raised ? "raiseHand" : undefined });
  const w = Math.max(11, ...pixels.map((p) => p.x + 1));
  const h = Math.max(14, ...pixels.map((p) => p.y + 1));
  return { pixels, w, h };
}
