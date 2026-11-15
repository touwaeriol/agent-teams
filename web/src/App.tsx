// v2 routes (docs/v2/api.md §4).
import { lazy, Suspense, type ComponentType } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { useMe } from "@/api/queries";
import { Loading } from "@/components/common";
import { ProjectShell, SettingsShell } from "@/components/shell/Shell";
import { DeliverablesPage } from "@/pages/Deliverables";
import { LegacyPage } from "@/pages/Legacy";
import { ChangePasswordPage, LoginPage } from "@/pages/Login";
import { MainPage } from "@/pages/Main";
import { NewProjectPage } from "@/pages/NewProject";
import { OverviewPage } from "@/pages/Overview";
import { ProjectSettingsPage } from "@/pages/ProjectSettings";
import { ProjectsIndex } from "@/pages/Projects";
import { ThreadPage } from "@/pages/Thread";

/** Pages that pull in the office renderer (ajv, canvas) or are rarely opened load on demand. */
function lazyPage(load: () => Promise<{ default: ComponentType }>) {
  const Page = lazy(load);
  return function LazyPage() {
    return (
      <Suspense fallback={<Loading className="h-full" />}>
        <Page />
      </Suspense>
    );
  };
}

const OfficePage = lazyPage(() => import("@/pages/Office").then((x) => ({ default: x.OfficePage })));
const AdminPage = lazyPage(() => import("@/pages/settings/Admin").then((x) => ({ default: x.AdminPage })));
const AppearancePage = lazyPage(() => import("@/pages/settings/Appearance").then((x) => ({ default: x.AppearancePage })));
const GitHubPage = lazyPage(() => import("@/pages/settings/GitHub").then((x) => ({ default: x.GitHubPage })));
const NodesPage = lazyPage(() => import("@/pages/settings/Nodes").then((x) => ({ default: x.NodesPage })));
const SetupGitHubPage = lazyPage(() => import("@/pages/settings/SetupGitHub").then((x) => ({ default: x.SetupGitHubPage })));
const TeamPage = lazyPage(() => import("@/pages/settings/Team").then((x) => ({ default: x.TeamPage })));
const DesignViewerPage = lazyPage(() => import("@/pages/DesignViewer").then((x) => ({ default: x.DesignViewerPage })));

export function App() {
  const me = useMe();
  const location = useLocation();
  if (me.isLoading) return <Loading className="h-full" />;
  if (!me.data) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace state={{ from: location.pathname }} />} />
      </Routes>
    );
  }
  if (me.data.must_change_password) return <ChangePasswordPage />;
  const admin = me.data.role === "admin";
  const from = (location.state as { from?: string } | null)?.from;
  return (
    <Routes>
      <Route path="/login" element={<Navigate to={from && from !== "/login" ? from : "/projects"} replace />} />
      <Route path="/projects/:pid/office" element={<OfficePage />} />
      <Route path="/projects/:pid/deliverables/:id" element={<DesignViewerPage />} />
      <Route element={<ProjectShell me={me.data} />}>
        <Route index element={<Navigate to="/projects" replace />} />
        <Route path="/projects" element={<ProjectsIndex />} />
        <Route path="/projects/new" element={<NewProjectPage />} />
        <Route path="/projects/:pid" element={<MainPage />} />
        <Route path="/projects/:pid/overview" element={<OverviewPage />} />
        <Route path="/projects/:pid/threads/:no" element={<ThreadPage />} />
        <Route path="/projects/:pid/deliverables" element={<DeliverablesPage />} />
        <Route path="/projects/:pid/settings" element={<ProjectSettingsPage />} />
        <Route path="/projects/:pid/legacy/:cid" element={<LegacyPage />} />
      </Route>
      <Route element={<SettingsShell me={me.data} />}>
        <Route path="/settings" element={<Navigate to="/settings/team" replace />} />
        <Route path="/settings/team" element={<TeamPage />} />
        <Route path="/settings/nodes" element={<NodesPage />} />
        <Route path="/settings/git" element={<GitHubPage />} />
        <Route path="/settings/appearance" element={<AppearancePage />} />
        {admin && <Route path="/settings/admin" element={<AdminPage />} />}
        {admin && <Route path="/setup/github" element={<SetupGitHubPage />} />}
      </Route>
      <Route path="*" element={<Navigate to="/projects" replace />} />
    </Routes>
  );
}
