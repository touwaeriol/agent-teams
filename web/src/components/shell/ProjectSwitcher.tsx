// The project name in the header opens this: search, every project, 新建项目.
import { Check, ChevronDown, Plus, Search } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useProjects } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import { ProjectIcon } from "@/components/app/ProjectIcon";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { matchesProject } from "./ProjectSidebar";

export function ProjectSwitcher({ project }: { project: ProjectView }) {
  const navigate = useNavigate();
  const projects = useProjects();
  const [q, setQ] = useState("");
  const list = (projects.data ?? []).filter((p) => !p.archived || p.id === project.id).filter((p) => matchesProject(p, q));
  return (
    <DropdownMenu onOpenChange={(open) => !open && setQ("")}>
      <DropdownMenuTrigger asChild>
        <button type="button" aria-label="切换项目" className="flex h-9 min-w-0 items-center gap-2 rounded-lg px-1.5 hover:bg-raised data-[state=open]:bg-raised">
          <ProjectIcon project={project} size={24} />
          <span className="truncate text-[15px] font-semibold">{project.name}</span>
          <ChevronDown className="size-4 shrink-0 text-muted-foreground" strokeWidth={1.8} />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72 p-1.5">
        <label className="mb-1 flex h-8 items-center gap-2 rounded-md border border-input px-2 text-muted-foreground" onKeyDown={(e) => e.stopPropagation()}>
          <Search className="size-3.5" strokeWidth={1.8} />
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索项目" className="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" />
        </label>
        <div className="scroll-thin max-h-72 overflow-y-auto">
          {list.map((p) => (
            <DropdownMenuItem key={p.id} onSelect={() => navigate(`/projects/${p.id}`)} className="gap-2.5">
              <ProjectIcon project={p} size={20} />
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              {p.id === project.id && <Check className="text-muted-foreground" />}
            </DropdownMenuItem>
          ))}
          {list.length === 0 && <p className="px-2 py-3 text-xs text-muted-foreground">没有匹配的项目</p>}
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => navigate("/projects/new")}>
          <Plus /> 新建项目
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
