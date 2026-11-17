// Orchestration routes of docs/v2/api.md in the shape openapi-typescript
// generates, so the one openapi-fetch client covers them too. They are being
// implemented in parallel; once `atctl openapi` exports them, delete the
// matching entries here (the generated ones in schema.d.ts take over and any
// mismatch shows up as a type error at the call site).
import type {
  ActivityView,
  CheckView,
  CoordinatorView,
  DeliverableComment,
  DeliverableFiles,
  DeliverableView,
  EmployeeBrief,
  EmployeeCard,
  MailView,
  MainTaskStatus,
  MainTaskView,
  MergeEntryView,
  MessageView,
  OfficeReplay,
  OfficeSnapshot,
  OverviewView,
  Page,
  QuestionView,
  SearchResult,
  TaskView,
  ThreadDetail,
  ThreadView,
  WaitingView,
} from "./views";

type Json<T> = { content: { "application/json": T } };
type Ok<T, C extends number = 200> = { responses: { [K in C]: { headers: Record<string, unknown> } & Json<T> } };
type Empty<C extends number = 204> = { responses: { [K in C]: { headers: Record<string, unknown>; content?: never } } };
type PathOf<P> = [P] extends [never] ? { path?: never } : { path: P };
type QueryOf<Q> = [Q] extends [never] ? { query?: never } : { query?: Q };
type Params<P, Q> = { parameters: PathOf<P> & QueryOf<Q> & { header?: never; cookie?: never } };
type BodyOf<B> = [B] extends [never] ? { requestBody?: never } : { requestBody: Json<B> };

/** One operation: path params, query, JSON body, response. */
type Op<R, P = never, Q = never, B = never> = Params<P, Q> & BodyOf<B> & R;

type Pid = { pid: string };
type Tid = { tid: string };
type Cid = { cid: string };
type Paged = { before?: string; cursor?: string; limit?: number };

export interface OrchestrationPaths {
  "/api/projects/{pid}/coordinator": { get: Op<Ok<CoordinatorView>, Pid> };
  "/api/projects/{pid}/threads": {
    get: Op<Ok<ThreadView[]>, Pid, { status?: string; group?: "waiting" | "active" | "done" }>;
    post: Op<Ok<ThreadView, 201>, Pid, never, { title: string; brief: string; roles?: { role: string; count: number }[] }>;
  };
  "/api/projects/{pid}/waiting": { get: Op<Ok<WaitingView>, Pid> };
  "/api/projects/{pid}/overview": { get: Op<Ok<OverviewView>, Pid> };
  "/api/projects/{pid}/search": { get: Op<Ok<SearchResult>, Pid, { q: string }> };
  "/api/projects/{pid}/deliverables": {
    get: Op<Ok<Page<DeliverableView>>, Pid, { kind?: string; thread?: number; q?: string; cursor?: string; no_images?: boolean }>;
    post: Op<Ok<DeliverableView, 201>, Pid, never, { kind: string; title: string; url: string; thread_no?: number }>;
  };
  "/api/projects/{pid}/deliverables/request": { post: Op<Ok<MessageView, 202>, Pid, never, { kind: string; brief: string }> };
  "/api/deliverables/{id}": {
    patch: Op<Ok<DeliverableView>, { id: string }, never, { title: string }>;
    delete: Op<Empty, { id: string }>;
  };
  "/api/deliverables/{id}/files": { get: Op<Ok<DeliverableFiles>, { id: string }, { version?: number }> };
  "/api/deliverables/{id}/comments": {
    get: Op<Ok<{ items: DeliverableComment[] }>, { id: string }>;
    post: Op<Ok<DeliverableComment, 201>, { id: string }, never, { text: string; version?: number }>;
  };
  "/api/projects/{pid}/office": { get: Op<Ok<OfficeSnapshot>, Pid> };
  "/api/projects/{pid}/office/replay": { get: Op<Ok<OfficeReplay>, Pid, { from: string; to: string; thread?: number }> };
  "/api/employees/{id}": { get: Op<Ok<EmployeeCard>, { id: string }> };

  "/api/conversations/{cid}/messages": {
    get: Op<Ok<Page<MessageView>>, Cid, Paged>;
    post: Op<Ok<MessageView, 202>, Cid, never, { content: string; interrupt?: boolean }>;
  };
  "/api/conversations/{cid}/interrupt": { post: Op<Empty<202>, Cid> };
  "/api/conversations/{cid}/questions": { get: Op<Ok<QuestionView[]>, Cid, { status?: "open" | "all" }> };
  "/api/conversations/{cid}/questions/{qid}/answer": {
    post: Op<Ok<QuestionView>, Cid & { qid: string }, never, { answer: string; reason?: string }>;
  };

  "/api/threads/{tid}": {
    get: Op<Ok<ThreadDetail>, Tid, { include?: string }>;
    patch: Op<Ok<ThreadView>, Tid, never, { title?: string; status?: "paused" | "running" | "archived"; auto_approve?: boolean }>;
  };
  "/api/threads/{tid}/main-tasks": { get: Op<Ok<MainTaskView[]>, Tid> };
  "/api/threads/{tid}/main-tasks/{mid}": { patch: Op<Ok<MainTaskView>, Tid & { mid: string }, never, { status: MainTaskStatus }> };
  "/api/threads/{tid}/tasks": { get: Op<Ok<TaskView[]>, Tid, { status?: string; main?: string }> };
  "/api/threads/{tid}/tasks/{task}/cancel": { post: Op<Ok<TaskView>, Tid & { task: string }> };
  "/api/threads/{tid}/tasks/{task}/retry": { post: Op<Ok<TaskView>, Tid & { task: string }> };
  "/api/threads/{tid}/members": { get: Op<Ok<EmployeeBrief[]>, Tid> };
  "/api/threads/{tid}/members/{name}/interrupt": { post: Op<Empty<202>, Tid & { name: string }> };
  "/api/threads/{tid}/members/{name}/message": { post: Op<Ok<MessageView, 202>, Tid & { name: string }, never, { content: string }> };
  "/api/threads/{tid}/mail": { get: Op<Ok<Page<MailView>>, Tid, Paged> };
  "/api/threads/{tid}/activity": { get: Op<Ok<Page<ActivityView>>, Tid, Paged> };
  "/api/threads/{tid}/merge-queue": { get: Op<Ok<MergeEntryView[]>, Tid> };
  "/api/threads/{tid}/checks": { get: Op<Ok<CheckView[]>, Tid> };
  "/api/threads/{tid}/open-pr": { post: Op<Ok<ThreadView>, Tid, never, { title?: string; body?: string; force?: boolean }> };
  "/api/threads/{tid}/accept": { post: Op<Ok<ThreadView>, Tid> };
  "/api/threads/{tid}/reject": { post: Op<Ok<ThreadView>, Tid, never, { reason: string }> };

}
