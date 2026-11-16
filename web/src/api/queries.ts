// react-query hooks over the API client. Query keys live in `keys` so the SSE
// layer (events.ts) and mutations update exactly the same cache entries.
import { useInfiniteQuery, useQuery, type InfiniteData } from "@tanstack/react-query";
import { client, list, unwrap } from "./client";
import type { MessageView, Page, ThreadView } from "./views";

export const keys = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  project: (pid: string) => ["project", pid] as const,
  projectScope: (pid: string) => ["p", pid] as const,
  coordinator: (pid: string) => ["p", pid, "coordinator"] as const,
  threads: (pid: string) => ["p", pid, "threads"] as const,
  waiting: (pid: string) => ["p", pid, "waiting"] as const,
  overview: (pid: string) => ["p", pid, "overview"] as const,
  deliverables: (pid: string, kind = "", q = "") => ["p", pid, "deliverables", kind, q] as const,
  allDeliverables: (pid: string) => ["p", pid, "deliverables"] as const,
  memory: (pid: string) => ["p", pid, "memory"] as const,
  schedules: (pid: string) => ["p", pid, "schedules"] as const,
  usage: (pid: string, days: number) => ["p", pid, "usage", days] as const,
  office: (pid: string) => ["p", pid, "office"] as const,
  search: (pid: string, q: string) => ["p", pid, "search", q] as const,
  legacy: (pid: string) => ["p", pid, "legacy"] as const,
  messages: (cid: string) => ["c", cid, "messages"] as const,
  questions: (cid: string) => ["c", cid, "questions"] as const,
  thread: (tid: string) => ["t", tid] as const,
  tasks: (tid: string) => ["t", tid, "tasks"] as const,
  members: (tid: string) => ["t", tid, "members"] as const,
  mail: (tid: string) => ["t", tid, "mail"] as const,
  activity: (tid: string) => ["t", tid, "activity"] as const,
  mergeQueue: (tid: string) => ["t", tid, "merge-queue"] as const,
  checks: (tid: string) => ["t", tid, "checks"] as const,
  employee: (id: string) => ["employee", id] as const,
  nodes: ["nodes"] as const,
  nodeSessions: (id: string) => ["nodes", id, "sessions"] as const,
  roles: ["roles"] as const,
  rolesYaml: ["roles", "yaml"] as const,
  rolePresets: ["roles", "presets"] as const,
  skills: ["skills"] as const,
  plugins: ["plugins"] as const,
  adminPlugins: ["admin", "plugins"] as const,
  deliverableFiles: (id: string, version = 0) => ["deliverable", id, "files", version] as const,
  deliverableComments: (id: string) => ["deliverable", id, "comments"] as const,
  connections: ["git"] as const,
  repos: (id: string, q = "") => ["git", id, "repos", q] as const,
  branches: (id: string, repo: string) => ["git", id, "branches", repo] as const,
  githubApp: ["github-app"] as const,
  adminUsers: ["admin", "users"] as const,
  adminSettings: ["admin", "settings"] as const,
  audit: ["admin", "audit"] as const,
  sessions: ["me", "sessions"] as const,
};

const path = <P extends Record<string, string>>(p: P) => ({ params: { path: p } });

export const useMe = () =>
  useQuery({ queryKey: keys.me, queryFn: () => unwrap(client.GET("/api/me")), retry: false, staleTime: 60_000 });

export const useProjects = () => useQuery({ queryKey: keys.projects, queryFn: () => list(client.GET("/api/projects")) });

export const useProject = (pid: string) =>
  useQuery({ queryKey: keys.project(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}", path({ pid }))), enabled: Boolean(pid) });

export const useCoordinator = (pid: string) =>
  useQuery({ queryKey: keys.coordinator(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}/coordinator", path({ pid }))) });

export const useThreads = (pid: string) =>
  useQuery({ queryKey: keys.threads(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}/threads", path({ pid }))) });

/** The thread with number `no` of a project, from the project's thread list. */
export function useThreadByNo(pid: string, no: number): { thread: ThreadView | undefined; isLoading: boolean } {
  const threads = useThreads(pid);
  return { thread: threads.data?.find((t) => t.no === no), isLoading: threads.isLoading };
}

export const useThread = (tid: string | undefined) =>
  useQuery({
    queryKey: keys.thread(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}", path({ tid: tid! }))),
    enabled: Boolean(tid),
  });

export const useWaiting = (pid: string) =>
  useQuery({ queryKey: keys.waiting(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}/waiting", path({ pid }))) });

export const useOverview = (pid: string) =>
  useQuery({ queryKey: keys.overview(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}/overview", path({ pid }))) });

export const useTasks = (tid: string | undefined) =>
  useQuery({
    queryKey: keys.tasks(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}/tasks", path({ tid: tid! }))),
    enabled: Boolean(tid),
  });

export const useMembers = (tid: string | undefined) =>
  useQuery({
    queryKey: keys.members(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}/members", path({ tid: tid! }))),
    enabled: Boolean(tid),
  });

export const useActivity = (tid: string | undefined, enabled = true) =>
  useQuery({
    queryKey: keys.activity(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}/activity", { params: { path: { tid: tid! }, query: { limit: 50 } } })),
    enabled: Boolean(tid) && enabled,
  });

export const useMail = (tid: string | undefined) =>
  useQuery({
    queryKey: keys.mail(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}/mail", { params: { path: { tid: tid! }, query: { limit: 50 } } })),
    enabled: Boolean(tid),
  });

export const useMergeQueue = (tid: string | undefined) =>
  useQuery({
    queryKey: keys.mergeQueue(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}/merge-queue", path({ tid: tid! }))),
    enabled: Boolean(tid),
  });

export const useChecks = (tid: string | undefined) =>
  useQuery({
    queryKey: keys.checks(tid ?? ""),
    queryFn: () => unwrap(client.GET("/api/threads/{tid}/checks", path({ tid: tid! }))),
    enabled: Boolean(tid),
  });

export const useQuestions = (cid: string | undefined) =>
  useQuery({
    queryKey: keys.questions(cid ?? ""),
    queryFn: () => unwrap(client.GET("/api/conversations/{cid}/questions", { params: { path: { cid: cid! }, query: { status: "all" } } })),
    enabled: Boolean(cid),
  });

export const MESSAGE_PAGE = 50;
export type MessagePages = InfiniteData<Page<MessageView>, string | undefined>;

/** Messages of a conversation, newest page first; each page is in chronological order. */
export const useMessages = (cid: string | undefined) =>
  useInfiniteQuery({
    queryKey: keys.messages(cid ?? ""),
    queryFn: ({ pageParam }) =>
      unwrap(client.GET("/api/conversations/{cid}/messages", { params: { path: { cid: cid! }, query: { limit: MESSAGE_PAGE, before: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    enabled: Boolean(cid),
  });

/** Flattens message pages (newest page first) into one chronological list. */
export function flattenMessages(data: { pages: Page<MessageView>[] } | undefined): MessageView[] {
  if (!data) return [];
  return [...data.pages].reverse().flatMap((p) => p.items);
}

export const useDeliverables = (pid: string, kind: string, q: string) =>
  useQuery({
    queryKey: keys.deliverables(pid, kind, q),
    queryFn: () =>
      unwrap(client.GET("/api/projects/{pid}/deliverables", { params: { path: { pid }, query: { kind: kind || undefined, q: q || undefined } } })),
  });

export const useSchedules = (pid: string, enabled = true) =>
  useQuery({ queryKey: keys.schedules(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}/schedules", path({ pid }))), enabled });

export const useProjectUsage = (pid: string, days: number) =>
  useQuery({ queryKey: keys.usage(pid, days), queryFn: () => unwrap(client.GET("/api/projects/{pid}/usage", { params: { path: { pid }, query: { days } } })) });

export const useMemory = (pid: string) =>
  useQuery({ queryKey: keys.memory(pid), queryFn: () => list(client.GET("/api/projects/{pid}/memory", path({ pid }))) });

export const useOfficeSnapshot = (pid: string) =>
  useQuery({ queryKey: keys.office(pid), queryFn: () => unwrap(client.GET("/api/projects/{pid}/office", path({ pid }))), staleTime: Infinity });

export const useEmployee = (id: string | null) =>
  useQuery({
    queryKey: keys.employee(id ?? ""),
    queryFn: () => unwrap(client.GET("/api/employees/{id}", path({ id: id! }))),
    enabled: Boolean(id),
  });

export const useSearch = (pid: string, q: string) =>
  useQuery({
    queryKey: keys.search(pid, q),
    queryFn: () => unwrap(client.GET("/api/projects/{pid}/search", { params: { path: { pid }, query: { q } } })),
    enabled: q.trim().length > 0,
  });

export const useLegacyConversations = (pid: string) =>
  useQuery({
    queryKey: keys.legacy(pid),
    queryFn: () => unwrap(client.GET("/api/conversations", { params: { query: { project_id: pid, kind: "chat" } } })),
    retry: false,
  });

export const useNodes = (refetchInterval = 15_000) =>
  useQuery({ queryKey: keys.nodes, queryFn: () => list(client.GET("/api/nodes")), refetchInterval });

export const useNodeSessions = (id: string) =>
  useQuery({ queryKey: keys.nodeSessions(id), queryFn: () => list(client.GET("/api/nodes/{id}/sessions", path({ id }))) });

export const useRoles = () => useQuery({ queryKey: keys.roles, queryFn: () => unwrap(client.GET("/api/roles")) });

export const useRolesYaml = (enabled: boolean) =>
  useQuery({ queryKey: keys.rolesYaml, queryFn: () => unwrap(client.GET("/api/roles/yaml")), enabled });

export const useRolePresets = () => useQuery({ queryKey: keys.rolePresets, queryFn: () => unwrap(client.GET("/api/roles/presets")), staleTime: 300_000 });

export const useSkills = () => useQuery({ queryKey: keys.skills, queryFn: () => unwrap(client.GET("/api/skills")) });

export const usePlugins = () => useQuery({ queryKey: keys.plugins, queryFn: () => unwrap(client.GET("/api/plugins")) });

export const useAdminPlugins = () => useQuery({ queryKey: keys.adminPlugins, queryFn: () => unwrap(client.GET("/api/admin/plugins")) });

/** Signed URLs expire after two hours: refetch well before that. */
export const useDeliverableFiles = (id: string, version = 0, enabled = true) =>
  useQuery({
    queryKey: keys.deliverableFiles(id, version),
    queryFn: () => unwrap(client.GET("/api/deliverables/{id}/files", { params: { path: { id }, query: { version: version || undefined } } })),
    enabled: enabled && Boolean(id),
    staleTime: 30 * 60_000,
    refetchInterval: 60 * 60_000,
  });

export const useDeliverableComments = (id: string) =>
  useQuery({ queryKey: keys.deliverableComments(id), queryFn: () => unwrap(client.GET("/api/deliverables/{id}/comments", path({ id }))) });

export const useConnections = () => useQuery({ queryKey: keys.connections, queryFn: () => list(client.GET("/api/git/connections")) });

export const useRepos = (id: string, q = "") =>
  useQuery({
    queryKey: keys.repos(id, q),
    queryFn: () => unwrap(client.GET("/api/git/connections/{id}/repos", { params: { path: { id }, query: { q: q || undefined, limit: 100 } } })),
    enabled: Boolean(id),
  });

export const useBranches = (id: string, repo: string) =>
  useQuery({
    queryKey: keys.branches(id, repo),
    queryFn: () => list(client.GET("/api/git/connections/{id}/branches", { params: { path: { id }, query: { repo } } })),
    enabled: Boolean(id && repo),
    retry: false,
  });

export const useGitHubApp = () => useQuery({ queryKey: keys.githubApp, queryFn: () => unwrap(client.GET("/api/git/github-app")) });

export const useAdminUsers = () => useQuery({ queryKey: keys.adminUsers, queryFn: () => list(client.GET("/api/admin/users")) });

export const useAdminSettings = () => useQuery({ queryKey: keys.adminSettings, queryFn: () => unwrap(client.GET("/api/admin/settings")) });

export const useAudit = () =>
  useQuery({ queryKey: keys.audit, queryFn: () => unwrap(client.GET("/api/admin/audit", { params: { query: { limit: 50 } } })) });

export const useSessions = () => useQuery({ queryKey: keys.sessions, queryFn: () => list(client.GET("/api/me/sessions")) });
