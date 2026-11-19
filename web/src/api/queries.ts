// vue-query composables over the API client. Query keys live in `keys` (same arrays as
// web/src/api/queries.ts) so the SSE layer (events.ts) and mutations update exactly the same
// cache entries. Arguments are MaybeRefOrGetter: pass a ref, a getter or a plain value.
import { useInfiniteQuery, useQuery, type InfiniteData } from "@tanstack/vue-query";
import { computed, toValue, type MaybeRefOrGetter } from "vue";
import { client, list, unwrap } from "./client";
import type { MessageView, Page, ThreadView } from "./views";

type Arg<T = string> = MaybeRefOrGetter<T>;

export const keys = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  project: (pid: string) => ["project", pid] as const,
  projectScope: (pid: string) => ["p", pid] as const,
  coordinator: (pid: string) => ["p", pid, "coordinator"] as const,
  threads: (pid: string) => ["p", pid, "threads"] as const,
  waiting: (pid: string) => ["p", pid, "waiting"] as const,
  overview: (pid: string) => ["p", pid, "overview"] as const,
  deliverables: (pid: string, kind = "", q = "", noImages = false) => ["p", pid, "deliverables", kind, q, noImages] as const,
  allDeliverables: (pid: string) => ["p", pid, "deliverables"] as const,
  memory: (pid: string) => ["p", pid, "memory"] as const,
  sharedFiles: (pid: string) => ["p", pid, "shared-files"] as const,
  memoryFile: (pid: string, name: string) => ["p", pid, "memory", name] as const,
  schedules: (pid: string) => ["p", pid, "schedules"] as const,
  usage: (pid: string, days: number) => ["p", pid, "usage", days] as const,
  office: (pid: string) => ["p", pid, "office"] as const,
  search: (pid: string, q: string) => ["p", pid, "search", q] as const,
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
  models: ["models"] as const,
  builtinCommands: ["commands", "builtin"] as const,
  conversationModel: (cid: string) => ["c", cid, "model"] as const,
  conversationCommands: (cid: string) => ["c", cid, "commands"] as const,
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
  feedback: ["admin", "feedback"] as const,
  sessions: ["me", "sessions"] as const,
};

const path = <P extends Record<string, string>>(p: P) => ({ params: { path: p } });

/** A query whose key and enabled flag follow reactive arguments. */
function useKeyed<T>(key: () => readonly unknown[], fn: () => Promise<T>, extra: { enabled?: () => boolean; staleTime?: number; refetchInterval?: number; retry?: boolean } = {}) {
  return useQuery({
    queryKey: computed(() => key()),
    queryFn: fn,
    enabled: computed(() => (extra.enabled ? extra.enabled() : true)),
    ...(extra.staleTime !== undefined ? { staleTime: extra.staleTime } : {}),
    ...(extra.refetchInterval !== undefined ? { refetchInterval: extra.refetchInterval } : {}),
    ...(extra.retry !== undefined ? { retry: extra.retry } : {}),
  });
}

const has = (v: Arg<string | undefined | null>) => () => Boolean(toValue(v));
const val = (v: Arg<string | undefined | null>) => toValue(v) ?? "";

export const useMe = () =>
  useQuery({ queryKey: keys.me, queryFn: () => unwrap(client.GET("/api/me")), retry: false, staleTime: 60_000 });

export const useProjects = () => useQuery({ queryKey: keys.projects, queryFn: () => list(client.GET("/api/projects")) });

export const useProject = (pid: Arg) =>
  useKeyed(() => keys.project(val(pid)), () => unwrap(client.GET("/api/projects/{pid}", path({ pid: val(pid) }))), { enabled: has(pid) });

export const useCoordinator = (pid: Arg) =>
  useKeyed(() => keys.coordinator(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/coordinator", path({ pid: val(pid) }))), { enabled: has(pid) });

export const useThreads = (pid: Arg) =>
  useKeyed(() => keys.threads(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/threads", path({ pid: val(pid) }))), { enabled: has(pid) });

/** The thread with number `no` of a project, from the project's thread list. */
export function useThreadByNo(pid: Arg, no: Arg<number>) {
  const threads = useThreads(pid);
  return {
    thread: computed<ThreadView | undefined>(() => threads.data.value?.find((t) => t.no === toValue(no))),
    isLoading: threads.isLoading,
  };
}

export const useThread = (tid: Arg<string | undefined>) =>
  useKeyed(() => keys.thread(val(tid)), () => unwrap(client.GET("/api/threads/{tid}", path({ tid: val(tid) }))), { enabled: has(tid) });

export const useWaiting = (pid: Arg) =>
  useKeyed(() => keys.waiting(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/waiting", path({ pid: val(pid) }))), { enabled: has(pid) });

export const useOverview = (pid: Arg) =>
  useKeyed(() => keys.overview(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/overview", path({ pid: val(pid) }))), { enabled: has(pid) });

export const useTasks = (tid: Arg<string | undefined>) =>
  useKeyed(() => keys.tasks(val(tid)), () => unwrap(client.GET("/api/threads/{tid}/tasks", path({ tid: val(tid) }))), { enabled: has(tid) });

export const useMembers = (tid: Arg<string | undefined>) =>
  useKeyed(() => keys.members(val(tid)), () => unwrap(client.GET("/api/threads/{tid}/members", path({ tid: val(tid) }))), { enabled: has(tid) });

export const useActivity = (tid: Arg<string | undefined>, enabled: Arg<boolean> = true) =>
  useKeyed(
    () => keys.activity(val(tid)),
    () => unwrap(client.GET("/api/threads/{tid}/activity", { params: { path: { tid: val(tid) }, query: { limit: 50 } } })),
    { enabled: () => Boolean(toValue(tid)) && toValue(enabled) },
  );

export const useMail = (tid: Arg<string | undefined>) =>
  useKeyed(
    () => keys.mail(val(tid)),
    () => unwrap(client.GET("/api/threads/{tid}/mail", { params: { path: { tid: val(tid) }, query: { limit: 50 } } })),
    { enabled: has(tid) },
  );

export const useMergeQueue = (tid: Arg<string | undefined>) =>
  useKeyed(() => keys.mergeQueue(val(tid)), () => unwrap(client.GET("/api/threads/{tid}/merge-queue", path({ tid: val(tid) }))), { enabled: has(tid) });

export const useChecks = (tid: Arg<string | undefined>) =>
  useKeyed(() => keys.checks(val(tid)), () => unwrap(client.GET("/api/threads/{tid}/checks", path({ tid: val(tid) }))), { enabled: has(tid) });

export const useQuestions = (cid: Arg<string | undefined>) =>
  useKeyed(
    () => keys.questions(val(cid)),
    () => unwrap(client.GET("/api/conversations/{cid}/questions", { params: { path: { cid: val(cid) }, query: { status: "all" } } })),
    { enabled: has(cid) },
  );

export const MESSAGE_PAGE = 50;
export type MessagePages = InfiniteData<Page<MessageView>, string | undefined>;

/** Messages of a conversation, newest page first; each page is in chronological order. */
export const useMessages = (cid: Arg<string | undefined>) =>
  useInfiniteQuery({
    queryKey: computed(() => keys.messages(val(cid))),
    queryFn: ({ pageParam }: { pageParam: string | undefined }) =>
      unwrap(client.GET("/api/conversations/{cid}/messages", { params: { path: { cid: val(cid) }, query: { limit: MESSAGE_PAGE, before: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page: Page<MessageView>) => page.next_cursor ?? undefined,
    enabled: computed(() => Boolean(toValue(cid))),
  });

/** Flattens message pages (newest page first) into one chronological list. */
export function flattenMessages(data: { pages: Page<MessageView>[] } | undefined): MessageView[] {
  if (!data) return [];
  return [...data.pages].reverse().flatMap((p) => p.items);
}

/**
 * A project's deliverables. noImages asks the server to leave out hosted images published before
 * screenshots stopped being deliverables (lists of deliverables); a lookup by id keeps them.
 */
export const useDeliverables = (pid: Arg, kind: Arg, q: Arg, noImages = false) =>
  useKeyed(
    () => keys.deliverables(val(pid), val(kind), val(q), noImages),
    () =>
      unwrap(
        client.GET("/api/projects/{pid}/deliverables", {
          params: { path: { pid: val(pid) }, query: { kind: val(kind) || undefined, q: val(q) || undefined, no_images: noImages || undefined } },
        }),
      ),
    { enabled: has(pid) },
  );

export const useSchedules = (pid: Arg, enabled: Arg<boolean> = true) =>
  useKeyed(() => keys.schedules(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/schedules", path({ pid: val(pid) }))), {
    enabled: () => Boolean(toValue(pid)) && toValue(enabled),
  });

export const useProjectUsage = (pid: Arg, days: Arg<number>) =>
  useKeyed(
    () => keys.usage(val(pid), toValue(days)),
    () => unwrap(client.GET("/api/projects/{pid}/usage", { params: { path: { pid: val(pid) }, query: { days: toValue(days) } } })),
    { enabled: has(pid) },
  );

export const useMemory = (pid: Arg) =>
  useKeyed(() => keys.memory(val(pid)), () => list(client.GET("/api/projects/{pid}/memory", path({ pid: val(pid) }))), { enabled: has(pid) });

/** The project's shared files (no content) with the total and the limits; refetched on shared_file events. */
export const useSharedFiles = (pid: Arg) =>
  useKeyed(() => keys.sharedFiles(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/shared-files", path({ pid: val(pid) }))), { enabled: has(pid) });

/** One memory file with its content; `name` empty = nothing to load (a new file). */
export const useMemoryFile = (pid: Arg, name: Arg) =>
  useKeyed(
    () => keys.memoryFile(val(pid), val(name)),
    () => unwrap(client.GET("/api/projects/{pid}/memory/{name}", { params: { path: { pid: val(pid), name: val(name) } } })),
    { enabled: () => has(pid)() && has(name)() },
  );

export const useOfficeSnapshot = (pid: Arg) =>
  useKeyed(() => keys.office(val(pid)), () => unwrap(client.GET("/api/projects/{pid}/office", path({ pid: val(pid) }))), {
    enabled: has(pid),
    staleTime: Infinity,
  });

export const useEmployee = (id: Arg<string | null>) =>
  useKeyed(() => keys.employee(val(id)), () => unwrap(client.GET("/api/employees/{id}", path({ id: val(id) }))), { enabled: has(id) });

export const useSearch = (pid: Arg, q: Arg) =>
  useKeyed(
    () => keys.search(val(pid), val(q)),
    () => unwrap(client.GET("/api/projects/{pid}/search", { params: { path: { pid: val(pid) }, query: { q: val(q) } } })),
    { enabled: () => val(q).trim().length > 0 },
  );

export const useNodes = (refetchInterval = 15_000) =>
  useQuery({ queryKey: keys.nodes, queryFn: () => list(client.GET("/api/nodes")), refetchInterval });

export const useNodeSessions = (id: Arg) =>
  useKeyed(() => keys.nodeSessions(val(id)), () => list(client.GET("/api/nodes/{id}/sessions", path({ id: val(id) }))));

export const useRoles = () => useQuery({ queryKey: keys.roles, queryFn: () => unwrap(client.GET("/api/roles")) });

export const useRolesYaml = (enabled: Arg<boolean>) =>
  useKeyed(() => keys.rolesYaml, () => unwrap(client.GET("/api/roles/yaml")), { enabled: () => toValue(enabled) });

export const useModels = () => useQuery({ queryKey: keys.models, queryFn: () => unwrap(client.GET("/api/models")), staleTime: 60_000 });
export const useBuiltinCommands = () => useQuery({ queryKey: keys.builtinCommands, queryFn: () => unwrap(client.GET("/api/commands")), staleTime: 60_000 });
export const useConversationModel = (cid: Arg) =>
  useKeyed(() => keys.conversationModel(val(cid)), () => unwrap(client.GET("/api/conversations/{cid}/model", path({ cid: val(cid) }))), { enabled: has(cid) });
export const useRolePresets = () => useQuery({ queryKey: keys.rolePresets, queryFn: () => unwrap(client.GET("/api/roles/presets")), staleTime: 300_000 });

export const useSkills = () => useQuery({ queryKey: keys.skills, queryFn: () => unwrap(client.GET("/api/skills")) });

export const usePlugins = () => useQuery({ queryKey: keys.plugins, queryFn: () => unwrap(client.GET("/api/plugins")) });

export const useAdminPlugins = () => useQuery({ queryKey: keys.adminPlugins, queryFn: () => unwrap(client.GET("/api/admin/plugins")) });

/** Signed URLs expire after two hours: refetch well before that. */
export const useDeliverableFiles = (id: Arg, version: Arg<number> = 0, enabled: Arg<boolean> = true) =>
  useKeyed(
    () => keys.deliverableFiles(val(id), toValue(version)),
    () => unwrap(client.GET("/api/deliverables/{id}/files", { params: { path: { id: val(id) }, query: { version: toValue(version) || undefined } } })),
    { enabled: () => toValue(enabled) && Boolean(toValue(id)), staleTime: 30 * 60_000, refetchInterval: 60 * 60_000 },
  );

export const useDeliverableComments = (id: Arg) =>
  useKeyed(() => keys.deliverableComments(val(id)), () => unwrap(client.GET("/api/deliverables/{id}/comments", path({ id: val(id) }))), { enabled: has(id) });

export const useConnections = () => useQuery({ queryKey: keys.connections, queryFn: () => list(client.GET("/api/git/connections")) });

export const useRepos = (id: Arg, q: Arg = "") =>
  useKeyed(
    () => keys.repos(val(id), val(q)),
    () => unwrap(client.GET("/api/git/connections/{id}/repos", { params: { path: { id: val(id) }, query: { q: val(q) || undefined, limit: 100 } } })),
    { enabled: has(id) },
  );

export const useBranches = (id: Arg, repo: Arg) =>
  useKeyed(
    () => keys.branches(val(id), val(repo)),
    () => list(client.GET("/api/git/connections/{id}/branches", { params: { path: { id: val(id) }, query: { repo: val(repo) } } })),
    { enabled: () => Boolean(toValue(id) && toValue(repo)), retry: false },
  );

export const useGitHubApp = () => useQuery({ queryKey: keys.githubApp, queryFn: () => unwrap(client.GET("/api/git/github-app")) });

export const useAdminUsers = () => useQuery({ queryKey: keys.adminUsers, queryFn: () => list(client.GET("/api/admin/users")) });

export const useAdminSettings = () => useQuery({ queryKey: keys.adminSettings, queryFn: () => unwrap(client.GET("/api/admin/settings")) });

export const useAudit = () =>
  useQuery({ queryKey: keys.audit, queryFn: () => unwrap(client.GET("/api/admin/audit", { params: { query: { limit: 50 } } })) });

export const useFeedback = () => useQuery({ queryKey: keys.feedback, queryFn: () => list(client.GET("/api/admin/feedback")) });

export const useSessions = () => useQuery({ queryKey: keys.sessions, queryFn: () => list(client.GET("/api/me/sessions")) });
