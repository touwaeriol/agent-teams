import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  ArrowLeft,
  ChevronRight,
  FolderGit2,
  Github,
  LogOut,
  Menu,
  Moon,
  MoreHorizontal,
  Palette,
  PanelLeftClose,
  Pencil,
  Pin,
  PinOff,
  Plus,
  Search,
  Server,
  Settings,
  Shield,
  Sun,
  Trash2,
  Users,
} from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useMatch, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { errorText, Field, FormDialog, Logo } from "@/components/common";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Dot } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api, type Conversation, type Project, type User } from "@/lib/api";
import { pendingForUser } from "@/lib/comms";
import { keys, useAllConversations, useNodes, useProjects, useQuestionsOf } from "@/lib/queries";
import { useAction } from "@/lib/mutations";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

/** Sidebar + page; the sidebar collapses into a menu on small screens. */
function Shell({ sidebar }: { sidebar: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  const location = useLocation();
  useEffect(() => setOpen(false), [location.pathname]);
  return (
    <div className="flex h-full">
      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-40 flex w-[264px] shrink-0 flex-col border-r bg-sidebar transition-transform lg:static lg:translate-x-0",
          open ? "translate-x-0 shadow-xl" : "-translate-x-full",
        )}
      >
        {sidebar(() => setOpen(false))}
      </aside>
      {open && <div className="fixed inset-0 z-30 bg-black/30 lg:hidden" onClick={() => setOpen(false)} aria-hidden />}
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-12 items-center gap-2 border-b px-3 lg:hidden">
          <button type="button" onClick={() => setOpen(true)} aria-label="打开侧栏" className="rounded-md p-2 hover:bg-raised">
            <Menu className="size-5" />
          </button>
          <Logo className="size-5" />
          <span className="font-semibold">Agent Teams</span>
        </div>
        <main className="min-h-0 flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function Brand({ onClose }: { onClose: () => void }) {
  return (
    <div className="flex h-14 shrink-0 items-center justify-between px-4">
      <Link to="/projects" className="flex items-center gap-2 font-semibold tracking-tight">
        <Logo className="size-5" />
        Agent Teams
      </Link>
      <button type="button" onClick={onClose} aria-label="收起侧栏" className="rounded-md p-1.5 text-muted-foreground hover:bg-raised lg:hidden">
        <PanelLeftClose className="size-4" />
      </button>
    </div>
  );
}

// ---------------------------------------------------------------- projects

/** The project workspace: projects (and the current one's conversations) on the left. */
export function ProjectLayout({ me }: { me: User }) {
  return (
    <Shell
      sidebar={(close) => (
        <>
          <Brand onClose={close} />
          <ProjectSidebar />
          <AccountRow me={me} />
        </>
      )}
    />
  );
}

function ProjectSidebar() {
  const [query, setQuery] = useState("");
  const projects = useProjects();
  const conversations = useAllConversations();
  const match = useMatch("/projects/:projectId/*");
  const currentProject = match?.params.projectId;
  const chat = useMatch("/projects/:projectId/c/:conversationId");
  const currentConversation = chat?.params.conversationId;
  const byProject = useMemo(() => {
    const map = new Map<string, Conversation[]>();
    for (const c of conversations.data ?? []) {
      if (!c.project_id) continue;
      map.set(c.project_id, [...(map.get(c.project_id) ?? []), c]);
    }
    return map;
  }, [conversations.data]);
  const needle = query.trim().toLowerCase();
  const list = (projects.data ?? []).filter(
    (p) => !needle || p.name.toLowerCase().includes(needle) || (byProject.get(p.id) ?? []).some((c) => c.title.toLowerCase().includes(needle)),
  );

  return (
    <>
      <div className="flex flex-col gap-1 px-3">
        <Link to="/projects/new" className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium hover:bg-raised">
          <Plus className="size-4" /> 新建项目
        </Link>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索项目和对话"
            aria-label="搜索项目和对话"
            className="h-9 w-full rounded-lg bg-transparent pl-8 pr-2 text-sm outline-none placeholder:text-muted-foreground hover:bg-raised focus:bg-panel focus:ring-1 focus:ring-input"
          />
        </div>
      </div>
      <nav aria-label="项目" className="scroll-thin mt-3 min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        {projects.data?.length === 0 && <p className="px-2.5 text-xs text-muted-foreground">还没有项目</p>}
        {needle && list.length === 0 && <p className="px-2.5 text-xs text-muted-foreground">没有匹配的项目或对话</p>}
        {GROUPS.map((group) => {
          const items = list.filter(group.has);
          if (items.length === 0) return null;
          return (
            <ProjectGroup key={group.title} title={group.title} collapsible={group.collapsible} defaultOpen={!group.collapsible || Boolean(needle)}>
              {items.map((p) => (
                <ProjectItem
                  key={p.id}
                  project={p}
                  conversations={byProject.get(p.id) ?? []}
                  open={p.id === currentProject || Boolean(needle)}
                  currentConversation={currentConversation}
                  needle={needle}
                />
              ))}
            </ProjectGroup>
          );
        })}
      </nav>
    </>
  );
}

const GROUPS = [
  { title: "已置顶", has: (p: Project) => p.pinned && !p.archived, collapsible: false },
  { title: "项目", has: (p: Project) => !p.pinned && !p.archived, collapsible: false },
  { title: "已归档", has: (p: Project) => p.archived, collapsible: true },
];

function ProjectGroup({ title, collapsible, defaultOpen, children }: { title: string; collapsible: boolean; defaultOpen: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen);
  useEffect(() => setOpen(defaultOpen), [defaultOpen]);
  return (
    <div className="mb-3">
      <button
        type="button"
        disabled={!collapsible}
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-1 px-2.5 pb-1.5 text-xs font-medium text-muted-foreground enabled:hover:text-foreground"
      >
        {title}
        {collapsible && <ChevronRight className={cn("size-3 transition-transform", open && "rotate-90")} />}
      </button>
      {open && children}
    </div>
  );
}

/** Pin, rename, settings, archive and delete for one project. */
function ProjectMenu({ project }: { project: Project }) {
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [name, setName] = useState(project.name);
  const navigate = useNavigate();
  const match = useMatch("/projects/:projectId/*");
  const refresh = [keys.projects, keys.project(project.id), keys.allConversations];
  const patch = useAction((changes: Record<string, unknown>) => api<Project>(`/api/projects/${project.id}`, { method: "PATCH", json: changes }), {
    invalidate: refresh,
  });
  const remove = useAction(() => api(`/api/projects/${project.id}`, { method: "DELETE" }), {
    invalidate: refresh,
    success: `已删除「${project.name}」`,
    onSuccess: () => match?.params.projectId === project.id && navigate("/projects"),
  });
  const rename = useAction(() => api<Project>(`/api/projects/${project.id}`, { method: "PATCH", json: { name: name.trim() } }), {
    invalidate: refresh,
    onSuccess: () => setRenaming(false),
  });
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label={`${project.name} 的更多操作`}
          className="rounded-md p-1.5 text-muted-foreground opacity-0 hover:bg-raised hover:text-foreground focus:opacity-100 focus:outline-none data-[state=open]:bg-raised data-[state=open]:opacity-100 group-hover:opacity-100"
        >
          <MoreHorizontal className="size-3.5" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-[10rem]">
          <DropdownMenuItem onSelect={() => patch.mutate({ pinned: !project.pinned })}>
            {project.pinned ? <PinOff /> : <Pin />} {project.pinned ? "取消置顶" : "置顶"}
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => {
              setName(project.name);
              setRenaming(true);
            }}
          >
            <Pencil /> 重命名
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => navigate(`/projects/${project.id}/settings`)}>
            <Settings /> 设置
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => patch.mutate({ archived: !project.archived, ...(project.archived ? {} : { pinned: false }) })}>
            {project.archived ? <ArchiveRestore /> : <Archive />} {project.archived ? "取消归档" : "归档"}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setDeleting(true)} className="text-destructive focus:text-destructive">
            <Trash2 /> 删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <FormDialog
        open={renaming}
        onOpenChange={setRenaming}
        title="重命名项目"
        submitLabel="保存"
        busy={rename.isPending}
        disabled={!name.trim() || name.trim() === project.name}
        onSubmit={() => rename.mutate()}
      >
        <Field label="项目名称">
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      </FormDialog>
      <AlertDialog open={deleting} onOpenChange={setDeleting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除「{project.name}」？</AlertDialogTitle>
            <AlertDialogDescription>项目下的对话和任务记录会一起删除，无法恢复。只想先收起来的话，用“归档”。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => remove.mutate()}>删除</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function ProjectItem({
  project,
  conversations,
  open,
  currentConversation,
  needle,
}: {
  project: Project;
  conversations: Conversation[];
  open: boolean;
  currentConversation?: string;
  needle: string;
}) {
  const [all, setAll] = useState(false);
  const shown = conversations.filter((c) => !needle || c.title.toLowerCase().includes(needle) || project.name.toLowerCase().includes(needle));
  const visible = all ? shown : shown.slice(0, 8);
  const waiting = useWaiting(open ? visible : shown.slice(0, 3));
  return (
    <div className="mb-0.5">
      <div className="group flex items-center">
        <NavLink
          to={`/projects/${project.id}`}
          end
          className={cn(
            "flex h-9 min-w-0 flex-1 items-center gap-2 rounded-lg px-2.5 text-sm hover:bg-raised",
            open && "font-medium",
          )}
        >
          <ChevronRight className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
          <FolderGit2 className="size-4 shrink-0 text-muted-foreground" />
          <span className="truncate">{project.name}</span>
          {!open && waiting.size > 0 && <WaitingDot className="ml-auto" />}
        </NavLink>
        <NewConversationButton projectId={project.id} />
        <ProjectMenu project={project} />
      </div>
      {open && (
        <div className="ml-[22px] flex flex-col border-l pl-2">
          {visible.length === 0 && <span className="px-2.5 py-1.5 text-xs text-muted-foreground">还没有对话</span>}
          {visible.map((c) => (
            <Link
              key={c.id}
              to={`/projects/${project.id}/c/${c.id}`}
              aria-current={c.id === currentConversation ? "page" : undefined}
              className={cn(
                "flex items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] text-foreground/80 hover:bg-raised hover:text-foreground",
                c.id === currentConversation && "bg-accent font-medium text-foreground",
              )}
            >
              <span className="min-w-0 flex-1 truncate">{c.title || "未命名对话"}</span>
              {waiting.has(c.id) && <WaitingDot />}
            </Link>
          ))}
          {shown.length > 8 && (
            <button type="button" onClick={() => setAll(!all)} className="px-2.5 py-1 text-left text-xs text-muted-foreground hover:text-foreground">
              {all ? "收起" : `还有 ${shown.length - 8} 个对话`}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/** Conversations (of those given) with a question or approval waiting for the user. */
function useWaiting(conversations: Conversation[]): Set<string> {
  const results = useQuestionsOf(conversations.map((c) => c.id));
  const waiting = new Set<string>();
  results.forEach((r, i) => {
    if (pendingForUser(r.data).length > 0) waiting.add(conversations[i].id);
  });
  return waiting;
}

function WaitingDot({ className }: { className?: string }) {
  return (
    <span title="有问题等你处理" className={cn("flex shrink-0 items-center", className)}>
      <Dot tone="warn" />
      <span className="sr-only">有问题等你处理</span>
    </span>
  );
}

function NewConversationButton({ projectId }: { projectId: string }) {
  const navigate = useNavigate();
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: () => api<Conversation>("/api/conversations", { method: "POST", json: { title: "新对话", project_id: projectId } }),
    onSuccess: (c) => {
      client.invalidateQueries({ queryKey: keys.allConversations });
      client.invalidateQueries({ queryKey: keys.conversations(projectId) });
      navigate(`/projects/${projectId}/c/${c.id}`);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <button
      type="button"
      onClick={() => create.mutate()}
      disabled={create.isPending}
      aria-label="在这个项目里新建对话"
      title="新建对话"
      className="rounded-md p-1.5 text-muted-foreground opacity-0 hover:bg-raised hover:text-foreground focus:opacity-100 group-hover:opacity-100"
    >
      <Plus className="size-3.5" />
    </button>
  );
}

function AccountRow({ me }: { me: User }) {
  const nodes = useNodes();
  const online = nodes.data?.filter((n) => n.online).length ?? 0;
  const total = nodes.data?.length ?? 0;
  const { theme, setTheme } = useTheme();
  const navigate = useNavigate();
  return (
    <div className="shrink-0 border-t p-3">
      <Link to="/settings/nodes" className="mb-2 flex items-center gap-2 rounded-lg px-2.5 py-1 text-xs text-muted-foreground hover:text-foreground">
        <Dot tone={online > 0 ? "ok" : total > 0 ? "bad" : "neutral"} />
        节点 {online}/{total} 在线
      </Link>
      <div className="flex items-center gap-2 px-1">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-brand/15 text-sm font-semibold uppercase text-brand">{me.email.slice(0, 1)}</span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm">{me.email}</span>
          <span className="block text-[11px] text-muted-foreground">{me.role === "admin" ? "管理员" : "成员"}</span>
        </span>
        <DropdownMenu>
          <DropdownMenuTrigger aria-label="账户菜单" className="rounded-md p-1.5 text-muted-foreground hover:bg-raised hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <Settings className="size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" side="top">
            <DropdownMenuItem onSelect={() => navigate("/settings")}>
              <Settings /> 设置
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setTheme(theme === "dark" ? "light" : "dark")}>
              {theme === "dark" ? <Sun /> : <Moon />} {theme === "dark" ? "切换到亮色" : "切换到暗色"}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void logout()}>
              <LogOut /> 退出登录
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}

async function logout() {
  await api("/auth/logout", { method: "POST" }).catch(() => undefined);
  window.location.href = "/login";
}

// ---------------------------------------------------------------- settings

const SETTINGS = [
  { to: "/settings/team", label: "团队", icon: Users, hint: "负责人和员工" },
  { to: "/settings/nodes", label: "节点", icon: Server, hint: "运行员工的机器" },
  { to: "/settings/git", label: "Git 连接", icon: Github, hint: "授权 GitHub 仓库" },
  { to: "/settings/appearance", label: "外观", icon: Palette, hint: "亮色 / 暗色" },
];

/** Settings: their own menu, and a way back to the projects. */
export function SettingsLayout({ me }: { me: User }) {
  const items = me.role === "admin" ? [...SETTINGS, { to: "/settings/admin", label: "管理", icon: Shield, hint: "用户和平台" }] : SETTINGS;
  return (
    <Shell
      sidebar={(close) => (
        <>
          <Brand onClose={close} />
          <div className="px-3">
            <Link to="/projects" className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm text-muted-foreground hover:bg-raised hover:text-foreground">
              <ArrowLeft className="size-4" /> 返回项目
            </Link>
            <div className="px-2.5 pb-1.5 pt-4 text-xs font-medium text-muted-foreground">设置</div>
            <nav aria-label="设置" className="flex flex-col gap-0.5">
              {items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  className={({ isActive }) =>
                    cn("flex items-center gap-2.5 rounded-lg px-2.5 py-2 hover:bg-raised", isActive && "bg-accent")
                  }
                >
                  <item.icon className="size-4 shrink-0 text-muted-foreground" />
                  <span className="flex flex-col">
                    <span className="text-sm">{item.label}</span>
                    <span className="text-[11px] text-muted-foreground">{item.hint}</span>
                  </span>
                </NavLink>
              ))}
            </nav>
          </div>
          <div className="flex-1" />
          <AccountRow me={me} />
        </>
      )}
    />
  );
}
