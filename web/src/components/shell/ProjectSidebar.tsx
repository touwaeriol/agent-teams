// The project sidebar of the design (Sidebar board): projects only, plus
// 新建项目 / 搜索 / 交付物, archived projects folded, nodes and the user at the bottom.
import { Archive, ChevronRight, LayoutGrid, MoreHorizontal, Pencil, Pin, PinOff, Plus, Search, Settings, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, NavLink, useNavigate, useParams } from "react-router-dom";
import { projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useNodes, useProjects } from "@/api/queries";
import type { ProjectView, UserView } from "@/api/views";
import { ConfirmAction, Field, FormDialog, Logo } from "@/components/common";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { projectTint } from "@/lib/present";
import { cn } from "@/lib/utils";
import { Dot, Initial } from "../app/bits";
import { summarizeNodes } from "./nodes";

export function matchesProject(p: Pick<ProjectView, "name" | "description" | "repos">, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return [p.name, p.description, ...(p.repos ?? []).map((r) => r.repo)].some((s) => s.toLowerCase().includes(needle));
}

export function ProjectSidebar({ me, onNavigate }: { me: UserView; onNavigate?: () => void }) {
  const { pid } = useParams();
  const projects = useProjects();
  const [q, setQ] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const list = (projects.data ?? []).filter((p) => matchesProject(p, q));
  const pinned = list.filter((p) => p.pinned && !p.archived);
  const rest = list.filter((p) => !p.pinned && !p.archived);
  const archived = list.filter((p) => p.archived);
  const groups = [
    { title: "已置顶", items: pinned },
    { title: "项目", items: rest },
  ].filter((g) => g.items.length > 0);
  const current = (projects.data ?? []).find((p) => p.id === pid);
  return (
    <nav aria-label="项目" className="flex h-full w-[248px] shrink-0 flex-col border-r bg-sidebar">
      <Link to={current ? `/projects/${current.id}/overview` : "/projects"} onClick={onNavigate} className="flex h-14 items-center gap-2.5 px-[18px] font-semibold">
        <Logo className="size-5" />
        Agent Teams
      </Link>
      <div className="flex flex-col gap-0.5 px-3">
        <SideLink to="/projects/new" icon={<Plus />} onClick={onNavigate} strong>
          新建项目
        </SideLink>
        <label className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-muted-foreground">
          <Search className="size-4" strokeWidth={1.8} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索项目" className="min-w-0 flex-1 bg-transparent text-foreground outline-none placeholder:text-muted-foreground" />
        </label>
        {current && (
          <SideLink to={`/projects/${current.id}/deliverables`} icon={<LayoutGrid />} onClick={onNavigate}>
            交付物
          </SideLink>
        )}
      </div>
      <div className="scroll-thin flex-1 overflow-y-auto px-3 py-4">
        {groups.map((g) => (
          <div key={g.title} className="mb-3.5">
            <span className="block px-2.5 pb-1.5 text-xs text-muted-foreground">{g.title}</span>
            <div className="flex flex-col gap-px">
              {g.items.map((p) => (
                <ProjectRow key={p.id} project={p} current={p.id === pid} onNavigate={onNavigate} />
              ))}
            </div>
          </div>
        ))}
        {projects.data && list.length === 0 && <p className="px-2.5 text-xs text-muted-foreground">{q ? "没有匹配的项目" : "还没有项目"}</p>}
        {archived.length > 0 && (
          <button type="button" onClick={() => setShowArchived(!showArchived)} className="flex items-center gap-1.5 px-2.5 text-xs text-muted-foreground hover:text-foreground">
            已归档 {archived.length} <ChevronRight className={cn("size-2.5 transition-transform", showArchived && "rotate-90")} strokeWidth={2} />
          </button>
        )}
        {showArchived && (
          <div className="mt-1.5 flex flex-col gap-px opacity-80">
            {archived.map((p) => (
              <ProjectRow key={p.id} project={p} current={p.id === pid} onNavigate={onNavigate} />
            ))}
          </div>
        )}
      </div>
      <SidebarFooter me={me} onNavigate={onNavigate} />
    </nav>
  );
}

function SideLink({ to, icon, children, onClick, strong }: { to: string; icon: React.ReactNode; children: React.ReactNode; onClick?: () => void; strong?: boolean }) {
  return (
    <NavLink
      to={to}
      end
      onClick={onClick}
      className={({ isActive }) => cn("flex h-9 items-center gap-2.5 rounded-lg px-2.5 [&_svg]:size-4", strong && "font-medium", isActive ? "bg-accent" : "hover:bg-accent/60")}
    >
      {icon}
      {children}
    </NavLink>
  );
}

function ProjectRow({ project, current, onNavigate }: { project: ProjectView; current: boolean; onNavigate?: () => void }) {
  const [bg, fg] = projectTint(project.name);
  return (
    <div className={cn("group relative flex h-9 items-center rounded-lg", current ? "bg-accent font-semibold" : "hover:bg-accent/60")}>
      <Link to={`/projects/${project.id}`} onClick={onNavigate} aria-current={current ? "page" : undefined} className="flex h-full min-w-0 flex-1 items-center gap-2.5 px-2.5">
        <Initial name={project.name} bg={bg} fg={fg} />
        <span className="min-w-0 flex-1 truncate">{project.name}</span>
        {project.waiting > 0 && (
          <span className="flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-[#C2570C] px-[5px] text-[11px] font-semibold text-white group-hover:hidden">{project.waiting}</span>
        )}
      </Link>
      <ProjectMenu project={project} />
    </div>
  );
}

/** 置顶 / 重命名 / 归档 / 删除 (the "项目菜单" of thread #11 in the design). */
function ProjectMenu({ project }: { project: ProjectView }) {
  const navigate = useNavigate();
  const [renaming, setRenaming] = useState(false);
  const [name, setName] = useState(project.name);
  const [deleting, setDeleting] = useState(false);
  const invalidate = [keys.projects, keys.project(project.id)];
  const update = useAction((body: { pinned?: boolean; archived?: boolean; name?: string }) => projectActions.update(project.id, body), { invalidate });
  const remove = useAction(() => projectActions.remove(project.id), {
    invalidate: [keys.projects],
    success: "项目已删除",
    onSuccess: () => navigate("/projects"),
  });
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label={`${project.name} 的菜单`} className="mr-1 hidden size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background group-hover:flex data-[state=open]:flex">
            <MoreHorizontal className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem onSelect={() => update.mutate({ pinned: !project.pinned })}>
            {project.pinned ? <PinOff /> : <Pin />} {project.pinned ? "取消置顶" : "置顶"}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setRenaming(true)}>
            <Pencil /> 重命名
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => update.mutate({ archived: !project.archived })}>
            <Archive /> {project.archived ? "恢复" : "归档"}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem className="text-destructive" onSelect={() => setDeleting(true)}>
            <Trash2 /> 删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <FormDialog
        open={renaming}
        onOpenChange={setRenaming}
        title="重命名项目"
        submitLabel="保存"
        busy={update.isPending}
        disabled={!name.trim()}
        onSubmit={() => update.mutate({ name: name.trim() }, { onSuccess: () => setRenaming(false) })}
      >
        <Field label="项目名称">
          <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
      </FormDialog>
      <ConfirmAction
        open={deleting}
        onOpenChange={setDeleting}
        title={`删除项目「${project.name}」？`}
        description="会关闭全部会话，连同讨论串记录一起删掉，不能恢复。"
        confirmLabel="删除"
        onConfirm={() => remove.mutate()}
      />
    </>
  );
}

function SidebarFooter({ me, onNavigate }: { me: UserView; onNavigate?: () => void }) {
  const nodes = useNodes();
  const s = summarizeNodes(nodes.data ?? []);
  const tone = s.total === 0 ? "#B7B3AA" : s.online === s.total ? "#2E7D4F" : s.online > 0 ? "#C2570C" : "#B42318";
  const name = me.display_name || me.email.split("@")[0];
  return (
    <div className="border-t p-3">
      <Link to="/settings/nodes" onClick={onNavigate} className="flex items-center gap-2 px-2.5 pb-2.5 pt-1 text-xs text-muted-foreground hover:text-foreground">
        <Dot color={tone} />
        {s.total === 0 ? "还没有节点，去添加" : `节点 ${s.online} / ${s.total} 在线 · 槽位 ${s.active} / ${s.slots}`}
      </Link>
      <div className="flex items-center gap-2.5 px-1">
        <Initial name={name} bg="#F4E3D7" fg="#A24E1E" size={32} round />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-[13px]">{name}</span>
          <span className="text-[11px] text-muted-foreground">{me.role === "admin" ? "管理员" : "成员"}</span>
        </span>
        <Link to="/settings/team" onClick={onNavigate} aria-label="设置" className="flex size-8 items-center justify-center rounded-lg text-[#4A4843] hover:bg-accent">
          <Settings className="size-4" strokeWidth={1.8} />
        </Link>
      </div>
    </div>
  );
}
