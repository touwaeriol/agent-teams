import { useQuery } from "@tanstack/react-query";
import { api, type Conversation, type GitConnection, type NodeInfo, type Project, type TeamState, type User } from "./api";

export const keys = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  conversations: (projectId: string) => ["conversations", projectId] as const,
  nodes: ["nodes"] as const,
  team: ["team"] as const,
  connections: ["git-connections"] as const,
  repos: (id: string) => ["git-connections", id, "repos"] as const,
};

export const useMe = () => useQuery({ queryKey: keys.me, queryFn: () => api<User>("/api/me"), retry: false });
export const useProjects = () => useQuery({ queryKey: keys.projects, queryFn: () => api<Project[]>("/api/projects") });
export const useProject = (id: string) =>
  useQuery({ queryKey: keys.project(id), queryFn: () => api<Project>(`/api/projects/${id}`) });
export const useConversations = (projectId: string) =>
  useQuery({
    queryKey: keys.conversations(projectId),
    queryFn: () => api<Conversation[]>(`/api/conversations?project_id=${encodeURIComponent(projectId)}`),
  });
export const useNodes = (refetchInterval = 10_000) =>
  useQuery({ queryKey: keys.nodes, queryFn: () => api<NodeInfo[]>("/api/nodes"), refetchInterval });
export const useTeam = () => useQuery({ queryKey: keys.team, queryFn: () => api<TeamState>("/api/team") });
export const useConnections = () =>
  useQuery({ queryKey: keys.connections, queryFn: () => api<GitConnection[]>("/api/git/connections") });
