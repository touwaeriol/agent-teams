import { lazy, Suspense } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { ProjectLayout, SettingsLayout } from "@/components/Layout";
import { Loading } from "@/components/common";
import { useMe } from "@/lib/queries";
import { AdminPage } from "@/pages/Admin";
import { GitConnectionsPage } from "@/pages/GitConnections";
import { LoginPage } from "@/pages/Login";
import { NodesPage } from "@/pages/Nodes";
import { SetupGitHubPage } from "@/pages/SetupGitHub";
import { ProjectChatPage } from "@/pages/ProjectChat";
import { ProjectSettingsPage } from "@/pages/ProjectSettings";
import { NewProjectPage, ProjectsPage } from "@/pages/Projects";
import { AppearancePage } from "@/pages/Appearance";
import { TeamPage } from "@/pages/Team";

// The component catalogue is a development aid: only `npm run dev` serves it.
const ComponentsPage = import.meta.env.DEV ? lazy(() => import("@/pages/Components").then((m) => ({ default: m.ComponentsPage }))) : null;

export function App() {
  const me = useMe();
  const location = useLocation();

  if (me.isLoading) {
    return <Loading className="h-full" />;
  }
  if (!me.data) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace state={{ from: location.pathname }} />} />
      </Routes>
    );
  }
  const admin = me.data.role === "admin";
  return (
    <Routes>
      <Route path="/login" element={<Navigate to="/projects" replace />} />
      <Route element={<ProjectLayout me={me.data} />}>
        <Route index element={<Navigate to="/projects" replace />} />
        <Route path="/projects" element={<ProjectsPage />} />
        <Route path="/projects/new" element={<NewProjectPage />} />
        <Route path="/projects/:projectId" element={<ProjectChatPage />} />
        <Route path="/projects/:projectId/c/:conversationId" element={<ProjectChatPage />} />
        <Route path="/projects/:projectId/settings" element={<ProjectSettingsPage />} />
        {ComponentsPage && <Route path="/ui" element={<Suspense fallback={<Loading />}><ComponentsPage /></Suspense>} />}
      </Route>
      <Route element={<SettingsLayout me={me.data} />}>
        <Route path="/settings" element={<Navigate to="/settings/team" replace />} />
        <Route path="/settings/team" element={<TeamPage />} />
        <Route path="/settings/nodes" element={<NodesPage />} />
        <Route path="/settings/git" element={<GitConnectionsPage />} />
        <Route path="/settings/appearance" element={<AppearancePage />} />
        {admin && <Route path="/settings/admin" element={<AdminPage />} />}
        {admin && <Route path="/setup/github" element={<SetupGitHubPage />} />}
      </Route>
      {/* old addresses */}
      <Route path="/team" element={<Navigate to="/settings/team" replace />} />
      <Route path="/nodes" element={<Navigate to="/settings/nodes" replace />} />
      <Route path="/admin" element={<Navigate to="/settings/admin" replace />} />
      <Route path="*" element={<Navigate to="/projects" replace />} />
    </Routes>
  );
}
