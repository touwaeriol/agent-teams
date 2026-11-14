import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, FolderGit2, LogOut, Plus, Shield } from "lucide-react";
import { Link, NavLink, Outlet, useMatch, useNavigate } from "react-router-dom";
import { Logo } from "@/components/common";
import { Dot } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api, type User } from "@/lib/api";
import { useNodes, useProjects } from "@/lib/queries";
import { cn } from "@/lib/utils";

const NAV = [
  { to: "/projects", label: "项目" },
  { to: "/team", label: "团队" },
  { to: "/nodes", label: "节点" },
  { to: "/settings/git", label: "Git 连接" },
];

export function Layout({ me }: { me: User }) {
  const nodes = useNodes();
  const online = nodes.data?.filter((n) => n.online).length ?? 0;
  const total = nodes.data?.length ?? 0;
  const items = me.role === "admin" ? [...NAV, { to: "/admin", label: "管理" }] : NAV;

  return (
    <div className="flex min-h-full flex-col">
      <header className="sticky top-0 z-40 border-b bg-well/95 backdrop-blur">
        <div className="flex min-h-16 flex-wrap items-center gap-x-5 gap-y-2 px-5 py-2.5">
          <Link to="/projects" className="flex items-center gap-2.5 text-base font-bold text-foreground">
            <Logo />
            Agent Teams
          </Link>
          <ProjectSwitcher />
          <nav aria-label="主导航" className="flex flex-1 flex-wrap gap-1">
            {items.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                className={({ isActive }) =>
                  cn(
                    "flex h-10 items-center rounded-lg px-3 text-[#c9cbd8] transition-colors hover:bg-raised hover:text-foreground",
                    isActive && "bg-accent font-semibold text-accent-foreground hover:bg-accent hover:text-accent-foreground",
                  )
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
          <div className="flex items-center gap-3">
            <Link to="/nodes" className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground">
              <Dot tone={online > 0 ? "ok" : total > 0 ? "bad" : "neutral"} />
              节点 {online}/{total} 在线
            </Link>
            <AccountMenu me={me} />
          </div>
        </div>
      </header>
      <main className="flex-1">
        <Outlet />
      </main>
    </div>
  );
}

function ProjectSwitcher() {
  const projects = useProjects();
  const match = useMatch("/projects/:projectId/*");
  const navigate = useNavigate();
  const current = projects.data?.find((p) => p.id === match?.params.projectId);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="flex h-10 max-w-[260px] items-center gap-2 rounded-lg border bg-panel px-3 text-sm hover:border-input focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <span className="text-muted-foreground">项目</span>
        <span className="truncate font-medium">{current?.name ?? "选择项目"}</span>
        <ChevronDown className="size-3.5 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        <DropdownMenuLabel>切换项目</DropdownMenuLabel>
        {projects.data?.length === 0 && <div className="px-2 py-2 text-xs text-muted-foreground">还没有项目</div>}
        {projects.data?.map((p) => (
          <DropdownMenuItem key={p.id} onSelect={() => navigate(`/projects/${p.id}`)}>
            <FolderGit2 className="text-muted-foreground" />
            <span className="flex min-w-0 flex-col">
              <span className="truncate">{p.name}</span>
              {p.repo && <span className="truncate font-mono text-[11px] text-muted-foreground">{p.repo}</span>}
            </span>
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => navigate("/projects?new=1")}>
          <Plus /> 新建项目
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function AccountMenu({ me }: { me: User }) {
  const client = useQueryClient();
  const logout = async () => {
    await api("/auth/logout", { method: "POST" }).catch(() => undefined);
    client.clear();
    window.location.href = "/login";
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="账户"
        className="flex size-9 items-center justify-center rounded-full bg-input text-sm font-semibold uppercase hover:bg-input/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {me.email.slice(0, 1)}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>
          <div className="truncate text-sm text-foreground">{me.email}</div>
          <div className="flex items-center gap-1">{me.role === "admin" ? <><Shield className="size-3" /> 管理员</> : "成员"}</div>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={logout}>
          <LogOut /> 退出登录
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
