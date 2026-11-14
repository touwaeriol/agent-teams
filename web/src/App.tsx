import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { Layout } from "@/components/Layout";
import { Spinner } from "@/components/shared";
import { useMe } from "@/lib/queries";
import { AdminPage } from "@/pages/Admin";
import { GitConnectionsPage } from "@/pages/GitConnections";
import { LoginPage } from "@/pages/Login";
import { NodesPage } from "@/pages/Nodes";
import { ProjectChatPage } from "@/pages/ProjectChat";
import { ProjectSettingsPage } from "@/pages/ProjectSettings";
import { ProjectsPage } from "@/pages/Projects";
import { TeamPage } from "@/pages/Team";

export function App() {
  const me = useMe();
  const location = useLocation();

  if (me.isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        <Spinner />
      </div>
    );
  }
  if (!me.data) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace state={{ from: location.pathname }} />} />
      </Routes>
    );
  }
  return (
    <Routes>
      <Route path="/login" element={<Navigate to="/projects" replace />} />
      <Route element={<Layout me={me.data} />}>
        <Route index element={<Navigate to="/projects" replace />} />
        <Route path="/projects" element={<ProjectsPage />} />
        <Route path="/projects/:projectId" element={<ProjectChatPage />} />
        <Route path="/projects/:projectId/c/:conversationId" element={<ProjectChatPage />} />
        <Route path="/projects/:projectId/settings" element={<ProjectSettingsPage />} />
        <Route path="/team" element={<TeamPage />} />
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/settings/git" element={<GitConnectionsPage />} />
        {me.data.role === "admin" && <Route path="/admin" element={<AdminPage />} />}
        <Route path="*" element={<Navigate to="/projects" replace />} />
      </Route>
    </Routes>
  );
}
