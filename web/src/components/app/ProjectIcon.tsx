import type { ProjectView } from "@/api/views";
import { iconLook } from "@/lib/projectIcon";
import { cn } from "@/lib/utils";

/** The project's icon: an emoji, or its initial on a soft tile. */
export function ProjectIcon({ project, size = 24, className }: { project: Pick<ProjectView, "name"> & { icon?: string }; size?: number; className?: string }) {
  const look = iconLook(project.icon, project.name);
  const style = { width: size, height: size, fontSize: Math.round(size * (look.kind === "emoji" ? 0.62 : 0.5)) };
  if (look.kind === "emoji")
    return (
      <span aria-hidden className={cn("flex shrink-0 items-center justify-center rounded-md bg-raised leading-none", className)} style={style}>
        {look.emoji}
      </span>
    );
  return (
    <span aria-hidden className={cn("flex shrink-0 items-center justify-center rounded-md font-semibold", className)} style={{ ...style, background: look.bg, color: look.fg }}>
      {look.letter}
    </span>
  );
}
