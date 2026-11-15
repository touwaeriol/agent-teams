// Settings sidebar (SettingsNav board): back to projects, then the settings pages.
import { ArrowLeft } from "lucide-react";
import { Link, NavLink } from "react-router-dom";
import type { UserView } from "@/api/views";
import { Logo } from "@/components/common";
import { cn } from "@/lib/utils";
import { Initial } from "../app/bits";

const ITEMS = [
  { to: "/settings/team", label: "团队岗位", hint: "统筹、组长和成员模板" },
  { to: "/settings/nodes", label: "节点", hint: "运行成员的机器" },
  { to: "/settings/git", label: "GitHub 连接", hint: "授权你的仓库" },
  { to: "/settings/appearance", label: "外观", hint: "亮色 / 暗色" },
  { to: "/settings/admin", label: "管理", hint: "用户和平台", admin: true },
];

export function SettingsNav({ me, onNavigate }: { me: UserView; onNavigate?: () => void }) {
  const items = ITEMS.filter((i) => !i.admin || me.role === "admin");
  const name = me.display_name || me.email.split("@")[0];
  return (
    <nav aria-label="设置" className="flex h-full w-[264px] shrink-0 flex-col border-r bg-sidebar">
      <div className="flex h-14 items-center gap-2.5 px-[18px] font-semibold">
        <Logo className="size-5" />
        Agent Teams
      </div>
      <div className="flex flex-col gap-0.5 px-3">
        <Link to="/projects" onClick={onNavigate} className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[#4A4843] hover:bg-accent/60">
          <ArrowLeft className="size-4" strokeWidth={1.8} />
          返回项目
        </Link>
        <span className="block px-2.5 pb-1.5 pt-4 text-xs text-muted-foreground">设置</span>
        {items.map((i) => (
          <NavLink key={i.to} to={i.to} onClick={onNavigate} className={({ isActive }) => cn("flex flex-col gap-px rounded-lg px-2.5 py-2", isActive ? "bg-accent font-semibold" : "hover:bg-accent/60")}>
            <span className="text-sm">{i.label}</span>
            <span className="text-[11px] font-normal text-muted-foreground">{i.hint}</span>
          </NavLink>
        ))}
      </div>
      <div className="flex-1" />
      <div className="flex items-center gap-2.5 border-t p-3">
        <Initial name={name} bg="#F4E3D7" fg="#A24E1E" size={32} round />
        <span className="flex flex-col">
          <span className="text-[13px]">{name}</span>
          <span className="text-[11px] text-muted-foreground">{me.role === "admin" ? "管理员" : "成员"}</span>
        </span>
      </div>
    </nav>
  );
}
