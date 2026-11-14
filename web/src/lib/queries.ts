import { useQueries, useQuery } from "@tanstack/react-query";
import { api, type Conversation, type GitConnection, type GitHubAppStatus, type Mail, type NodeInfo, type Plan, type Question, type Project, type TeamState, type User } from "./api";

export const keys = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  conversations: (projectId: string) => ["conversations", projectId] as const,
  allConversations: ["conversations"] as const,
  nodes: ["nodes"] as const,
  team: ["team"] as const,
  connections: ["git-connections"] as const,
  repos: (id: string) => ["git-connections", id, "repos"] as const,
  githubApp: ["github-app"] as const,
  questions: (conversationId: string) => ["questions", conversationId] as const,
  mail: (conversationId: string) => ["mail", conversationId] as const,
  plan: (conversationId: string) => ["plan", conversationId] as const,
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
export const useGitHubApp = () =>
  useQuery({ queryKey: keys.githubApp, queryFn: () => api<GitHubAppStatus>("/api/git/github-app") });
export const useAllConversations = () =>
  useQuery({ queryKey: keys.allConversations, queryFn: () => api<Conversation[]>("/api/conversations") });

/** Every question of a conversation (open and handled); SSE keeps it current while the chat is open. */
const questionsQuery = (conversationId: string, refetchInterval?: number) => ({
  queryKey: keys.questions(conversationId),
  queryFn: () => api<Question[]>(`/api/conversations/${conversationId}/questions?status=all`),
  refetchInterval,
});
export const useQuestions = (conversationId: string) => useQuery(questionsQuery(conversationId));
/** Questions of several conversations at once (the sidebar's "needs you" dots). */
export const useQuestionsOf = (conversationIds: string[], refetchInterval = 30_000) =>
  useQueries({ queries: conversationIds.map((id) => questionsQuery(id, refetchInterval)) });
export const useMail = (conversationId: string) =>
  useQuery({ queryKey: keys.mail(conversationId), queryFn: () => api<Mail[]>(`/api/conversations/${conversationId}/mail`) });
export const usePlan = (conversationId: string) =>
  useQuery({ queryKey: keys.plan(conversationId), queryFn: () => api<Plan>(`/api/conversations/${conversationId}/plan`) });
