// /projects: open the most recent project, or invite to create the first one.
import { FolderPlus } from "lucide-react";
import { Link, Navigate } from "react-router-dom";
import { useProjects } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import { EmptyState, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { NavButton } from "@/components/shell/Shell";

/** Pinned first, then most recently updated; archived projects only if nothing else. */
export function recentProject(list: ProjectView[]): ProjectView | undefined {
  const live = list.filter((p) => !p.archived);
  const byRecent = (a: ProjectView, b: ProjectView) => Number(b.pinned) - Number(a.pinned) || Date.parse(b.updated_at) - Date.parse(a.updated_at);
  return live.sort(byRecent)[0] ?? list[0];
}

export function ProjectsIndex() {
  const projects = useProjects();
  if (projects.isLoading) return <Loading className="h-full" />;
  const recent = recentProject(projects.data ?? []);
  if (recent) return <Navigate to={`/projects/${recent.id}`} replace />;
  return (
    <div className="p-4">
      <NavButton />
      <div className="mx-auto max-w-xl pt-16">
        <EmptyState
          icon={<FolderPlus />}
          title="还没有项目"
          description="项目是入口：关联一个 GitHub 仓库，和项目统筹说需求，它会开讨论串让团队去做。"
          action={
            <Button asChild>
              <Link to="/projects/new">新建项目</Link>
            </Button>
          }
        />
      </div>
    </div>
  );
}
