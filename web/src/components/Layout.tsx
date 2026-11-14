import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  FolderGit2,
  Github,
  LogOut,
  Menu,
  Moon,
  PanelLeftClose,
  Search,
  Server,
  Settings,
  Shield,
  SquarePen,
  Sun,
  Users,
} from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useMatch, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { errorText, Logo } from "@/components/common";
import { Dot } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api, type Conversation, type User } from "@/lib/api";
import { keys, useAllConversations, useNodes, useProjects } from "@/lib/queries";
import { useTheme } from "@/lib/theme";
import { cn, timeAgo } from "@/lib/utils";

const NAV = [
  { to: "/projects", label: "项目", icon: FolderGit2 },
  { to: "/team", label: "团队", icon: Users },
  { to: "/nodes", label: "节点", icon: Server },
  { to: "/settings/git", label: "Git 连接", icon: Github },
];

/** App shell: a narrow sidebar on the left, the page on the right. */
export function Layout({ me }: { me: User }) {
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
        <Sidebar me={me} onClose={() => setOpen(false)} />
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

function Sidebar({ me, onClose }: { me: User; onClose: () => void }) {
  const [query, setQuery] = useState("");
  const items = me.role === "admin" ? [...NAV, { to: "/admin", label: "管理", icon: Shield }] : NAV;
  return (
    <>
      <div className="flex h-14 items-center justify-between px-4">
        <Link to="/projects" className="flex items-center gap-2 font-semibold tracking-tight">
          <Logo className="size-5" />
          Agent Teams
        </Link>
        <button type="button" onClick={onClose} aria-label="收起侧栏" className="rounded-md p-1.5 text-muted-foreground hover:bg-raised lg:hidden">
          <PanelLeftClose className="size-4" />
        </button>
      </div>
      <div className="flex flex-col gap-1 px-3">
        <NewConversationButton />
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索对话和项目"
            aria-label="搜索对话和项目"
            className="h-9 w-full rounded-lg bg-transparent pl-8 pr-2 text-sm outline-none placeholder:text-muted-foreground hover:bg-raised focus:bg-panel focus:ring-1 focus:ring-input"
          />
        </div>
        <nav aria-label="主导航" className="mt-1 flex flex-col gap-0.5">
          {items.map((item) => (
            <SideLink key={item.to} to={item.to} icon={<item.icon />}>
              {item.label}
            </SideLink>
          ))}
        </nav>
      </div>
      <Recent query={query} />
      <AccountRow me={me} />
    </>
  );
}

function SideLink({ to, icon, children }: { to: string; icon: ReactNode; children: ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        cn(
          "flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm text-foreground/80 hover:bg-raised hover:text-foreground [&_svg]:size-4 [&_svg]:text-muted-foreground",
          isActive && "bg-accent font-medium text-foreground",
        )
      }
    >
      {icon}
      {children}
    </NavLink>
  );
}

/** New conversation in the project being viewed; elsewhere, start a project. */
function NewConversationButton() {
  const match = useMatch("/projects/:projectId/*");
  const projectId = match?.params.projectId;
  const navigate = useNavigate();
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: () => api<Conversation>("/api/conversations", { method: "POST", json: { title: "新对话", project_id: projectId } }),
    onSuccess: (c) => {
      client.invalidateQueries({ queryKey: keys.allConversations });
      client.invalidateQueries({ queryKey: keys.conversations(projectId!) });
      navigate(`/projects/${projectId}/c/${c.id}`);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const start = () => (projectId ? create.mutate() : navigate("/projects?new=1"));
  return (
    <button type="button" onClick={start} disabled={create.isPending} className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium hover:bg-raised">
      <SquarePen className="size-4" />
      {projectId ? "新建对话" : "新建项目"}
    </button>
  );
}

function Recent({ query }: { query: string }) {
  const conversations = useAllConversations();
  const projects = useProjects();
  const names = useMemo(() => new Map((projects.data ?? []).map((p) => [p.id, p.name])), [projects.data]);
  const needle = query.trim().toLowerCase();
  const list = (conversations.data ?? [])
    .filter((c) => c.project_id && names.has(c.project_id))
    .filter((c) => !needle || `${c.title} ${names.get(c.project_id!)}`.toLowerCase().includes(needle))
    .slice(0, 40);
  const match = useMatch("/projects/:projectId/c/:conversationId");
  const current = match?.params.conversationId;
  return (
    <div className="scroll-thin mt-4 min-h-0 flex-1 overflow-y-auto px-3">
      <div className="px-2.5 pb-1.5 text-xs font-medium text-muted-foreground">最近对话</div>
      {list.length === 0 && <p className="px-2.5 text-xs text-muted-foreground">{needle ? "没有匹配的对话" : "还没有对话"}</p>}
      {list.map((c) => (
        <Link
          key={c.id}
          to={`/projects/${c.project_id}/c/${c.id}`}
          aria-current={c.id === current ? "page" : undefined}
          className={cn("flex flex-col rounded-lg px-2.5 py-1.5 hover:bg-raised", c.id === current && "bg-accent")}
        >
          <span className="truncate text-sm">{c.title || "未命名对话"}</span>
          <span className="truncate text-[11px] text-muted-foreground">
            {names.get(c.project_id!)} · {timeAgo(c.created_at)}
          </span>
        </Link>
      ))}
    </div>
  );
}

function AccountRow({ me }: { me: User }) {
  const nodes = useNodes();
  const online = nodes.data?.filter((n) => n.online).length ?? 0;
  const total = nodes.data?.length ?? 0;
  const { theme, setTheme } = useTheme();
  const client = useQueryClient();
  const logout = async () => {
    await api("/auth/logout", { method: "POST" }).catch(() => undefined);
    client.clear();
    window.location.href = "/login";
  };
  return (
    <div className="border-t p-3">
      <Link to="/nodes" className="mb-2 flex items-center gap-2 rounded-lg px-2.5 py-1 text-xs text-muted-foreground hover:text-foreground">
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
          <DropdownMenuTrigger aria-label="设置" className="rounded-md p-1.5 text-muted-foreground hover:bg-raised hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <Settings className="size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" side="top">
            <DropdownMenuLabel>外观</DropdownMenuLabel>
            <DropdownMenuItem onSelect={() => setTheme("light")}>
              <Sun /> 亮色{theme === "light" && " ✓"}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setTheme("dark")}>
              <Moon /> 暗色{theme === "dark" && " ✓"}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={logout}>
              <LogOut /> 退出登录
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}
