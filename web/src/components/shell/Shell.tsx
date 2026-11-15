// App shells: a fixed sidebar on desktop, a drawer on phones. Pages open the
// drawer through `useShell().openNav` (the ☰ button of their mobile header).
import { Menu } from "lucide-react";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { Outlet, useLocation, useParams } from "react-router-dom";
import { useProjectEvents, useUserEvents } from "@/api/sse";
import type { UserView } from "@/api/views";
import { cn } from "@/lib/utils";
import { ProjectSidebar } from "./ProjectSidebar";
import { SettingsNav } from "./SettingsNav";

const ShellContext = createContext<{ openNav: () => void }>({ openNav: () => undefined });
export const useShell = () => useContext(ShellContext);

function Drawer({ open, onClose, children }: { open: boolean; onClose: () => void; children: ReactNode }) {
  return (
    <div className={cn("fixed inset-0 z-40 md:hidden", open ? "" : "pointer-events-none")} aria-hidden={!open}>
      <div className={cn("absolute inset-0 bg-black/30 transition-opacity", open ? "opacity-100" : "opacity-0")} onClick={onClose} />
      <div className={cn("absolute inset-y-0 left-0 transition-transform", open ? "translate-x-0" : "-translate-x-full")}>{children}</div>
    </div>
  );
}

function Shell({ nav }: { nav: (close?: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  const location = useLocation();
  useEffect(() => setOpen(false), [location.pathname]);
  return (
    <ShellContext.Provider value={{ openNav: () => setOpen(true) }}>
      <div className="flex h-full min-h-0">
        <div className="hidden h-full md:block">{nav()}</div>
        <Drawer open={open} onClose={() => setOpen(false)}>
          {nav(() => setOpen(false))}
        </Drawer>
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto">
          <Outlet />
        </div>
      </div>
    </ShellContext.Provider>
  );
}

/** Project pages: the project sidebar, plus the project's live event stream. */
export function ProjectShell({ me }: { me: UserView }) {
  const { pid } = useParams();
  useUserEvents(true);
  useProjectEvents(pid);
  return <Shell nav={(close) => <ProjectSidebar me={me} onNavigate={close} />} />;
}

export function SettingsShell({ me }: { me: UserView }) {
  useUserEvents(true);
  return <Shell nav={(close) => <SettingsNav me={me} onNavigate={close} />} />;
}

/** The ☰ button pages put in their header; only visible on phones. */
export function NavButton({ className }: { className?: string }) {
  const { openNav } = useShell();
  return (
    <button type="button" aria-label="打开侧栏" onClick={openNav} className={cn("-ml-2 flex size-11 items-center justify-center md:hidden", className)}>
      <Menu className="size-5" strokeWidth={1.8} />
    </button>
  );
}
