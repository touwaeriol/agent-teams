// View objects of the REST API (docs/v2/api.md §2).
//
// Types for the endpoints the Go core already exports come straight from the
// generated OpenAPI file (schema.d.ts). The orchestration views (threads,
// tasks, questions, messages, deliverables, office) are written here from
// api.md until the orchestration routes land in openapi.json; when they do,
// replace each hand-written type with the generated one in this file only.
import type { ForwardedFrom } from "@/lib/forward";
import type { components } from "./schema";

type S = components["schemas"];

export type UserView = S["UserView"];
export type ProjectView = S["ProjectView"];
export type ProjectInput = S["ProjectInput"];
export type ProjectUpdate = S["ProjectUpdate"];
export type DeliverySettings = S["Delivery"];
export type MemoryBrief = S["MemoryBrief"];
export type MemoryFileView = S["MemoryFile"];
/** usage: subscription quota per runtime. The openapi does not mark it nullable, but the server
 * sends null until the node reports (and older servers omit it): both mean "no data yet". */
export type NodeView = Omit<S["NodeView"], "usage"> & { usage?: NodeUsage | null };
export type NodeUsage = S["NodeUsage"];
export type RuntimeUsage = S["RuntimeUsage"];
/** key: five_hour, weekly, weekly_opus, weekly_sonnet, weekly_model (Claude); primary, secondary (Codex). */
export type UsageWindow = S["UsageWindow"];
/** POST /api/nodes/{id}/usage/refresh; usage is null when the node never reported. */
export type UsageRefresh = Omit<S["UsageRefreshOutputBody"], "usage"> & { usage: NodeUsage | null };
export type NodeSession = S["NodeSession"];
export type FeedbackView = S["FeedbackView"];
export type Enrollment = S["Enrollment"];
export type NewNodeResult = S["NewNodeOutputBody"];
export type RoleView = S["Role"];
export type RolesBody = S["RolesBody"];
export type Look = S["Look"];
export type SkillView = S["Skill"];
export type SkillFile = S["File"];
export type PluginView = S["Plugin"];
export type PluginVersion = S["Version"];
export type PluginContents = S["Summary"];
export type GitConnectionView = S["GitConnectionView"];
export type RepoItem = S["RepoItem"];
export type GitHubAppStatus = S["AppStatusOutputBody"];
export type AdminUserView = S["AdminUserView"];
export type AdminSettings = S["AdminSettings"];
export type AuditEntry = S["AuditEntry"];
export type SessionView = S["SessionView"];
export type APIErrorBody = S["APIError"];
export type ScheduleView = S["Schedule"];
export type ScheduleBody = S["ScheduleBody"];
export type ProjectUsage = S["ProjectUsage"];
export type ForwardView = S["ForwardView"];

export type ThreadStatus = "queued" | "running" | "waiting" | "review" | "done" | "failed" | "paused" | "archived";
export type MainTaskStatus = "todo" | "doing" | "done";
export type TaskStatus = "pending" | "claimed" | "running" | "blocked" | "review" | "decision" | "merging" | "done" | "failed" | "cancelled";

export interface EmployeeStatusView {
  state: string;
  substate?: string;
  bubble?: string;
  bubble_kind?: string;
  task_id?: string;
  task_kind?: string;
  tier?: string;
  role?: string;
  since?: string;
  seq?: number;
  cause?: string;
}

export interface EmployeeBrief {
  id: string;
  name: string;
  tier: "coordinator" | "lead" | "member";
  role: string;
  title: string;
  runtime: "claude" | "codex";
  node: { id: string; name: string; online: boolean } | null;
  look: Look;
  status: EmployeeStatusView | null;
  /** A lead's own thread; for a project member the thread of its current task, else the one it last worked in. */
  thread_no?: number | null;
  /** In a thread's member list: the member's current task is in another thread (thread_no). */
  busy_elsewhere?: boolean;
  /** Counts towards the project's headcount (a project-level member); absent in older data. */
  staff?: boolean;
  /** Position of the role in the team template (office desk rows). */
  sort?: number;
  /** Instance number among the role's employees (dev-2 → 2). */
  instance?: number;
}

export interface MainTaskView {
  id: string;
  pos: number;
  title: string;
  status: MainTaskStatus;
  auto_done: boolean;
  tasks_total: number;
  tasks_done: number;
}

export interface ThreadCounts {
  waiting: number;
  questions_open: number;
  approvals_open: number;
  tasks_total: number;
  tasks_done: number;
  /** Small tasks not finished yet (not done, failed or cancelled); older servers leave it out. */
  tasks_open?: number;
  main_total: number;
  main_done: number;
  /** Deliverables the thread published (older servers leave it out). */
  deliverables?: number;
}

export interface ThreadView {
  id: string;
  project_id: string;
  no: number;
  title: string;
  brief: string;
  status: ThreadStatus;
  queue_position: number | null;
  pause_reason: string;
  branch: string;
  /** Deprecated: bases are per repository (repos). */
  base_branch: string;
  pr_number: number | null;
  pr_url: string | null;
  pr_state: string | null;
  /** Why the last accept failed to merge (Chinese, for the user); null once accepted again, rejected or merged. */
  merge_error: S["ThreadView"]["merge_error"];
  /** The project's: how the thread ends once its PRs are open. */
  merge_policy: S["ThreadView"]["merge_policy"];
  /** Repositories the thread set a base for, branched or opened a PR in. */
  repos?: ThreadRepoView[];
  summary: string;
  auto_approve: boolean;
  opened_by: string;
  members: EmployeeBrief[];
  main_tasks: MainTaskView[];
  counts: ThreadCounts;
  lead_last: { text: string; at: string } | null;
  cost_usd_est: number | null;
  created_at: string;
  updated_at: string;
  /** When the thread first ran (null while queued). */
  started_at?: string | null;
  /** When it reached done, failed or archived. */
  finished_at?: string | null;
  /** Latest state change, message or deliverable of the thread. */
  last_activity_at?: string;
  /** The newest deliverable the thread published. */
  latest_deliverable?: DeliverableView | null;
  /** When the user pinned it (null: not pinned); pinned threads sort first in their group. */
  pinned_at?: string | null;
  /** When the reminder the user set will fire (null: none pending). */
  remind_at?: string | null;
  /** When a reminder fired; the thread waits on the user (等你处理) until it is cleared. */
  reminded_at?: string | null;
  /** Who handed over the brief and the user's words it came from (null: forwarded, or older threads; older servers leave it out). */
  dispatch?: ThreadDispatch | null;
}

/** How a thread was opened: who handed the brief (ThreadView.brief) to the lead, when, and from which of the user's words (null: none, or deleted). */
export type ThreadDispatch = Omit<S["ThreadDispatch"], "source"> & { source: DispatchSource | null };

/** The user's project-chat message a thread was opened for (attachments: same shape as meta.attachments). */
export type DispatchSource = S["DispatchSource"];

/** One repository of a thread: its base, whether the thread branch exists there, its PR. */
export interface ThreadRepoView {
  repo: string;
  base_branch: string;
  branch_created: boolean;
  pr_number: number | null;
  pr_url: string | null;
  pr_state: string;
}

export interface TaskView {
  id: string;
  thread_no: number;
  main_task_id: string | null;
  assignee: string;
  role: string;
  claimed_by: string;
  kind: string;
  instruction: string;
  status: TaskStatus;
  depends_on: string[];
  parent: string;
  priority: number;
  attempt: number;
  summary: string;
  branch: string;
  head_sha: string;
  created_at: string;
  claimed_at: string | null;
  finished_at: string | null;
}

export interface QuestionDetail {
  tool_name?: string;
  input?: Record<string, unknown>;
  title?: string;
  description?: string;
  diff?: string;
  source?: string;
  /** Lead review (approval_routing lead_first): the lead asked, its verdict and reason. */
  reviewer?: string;
  review?: { by: string; decision: "allow" | "deny" | "ask_user"; reason: string };
  /** Structured questions (ask_user questions[], or Claude's AskUserQuestion: via). */
  questions?: AskItem[];
  /** The user's choices, one per structured question (after answering). */
  answers?: AskAnswer[];
  via?: string;
}

export interface AskOption {
  label: string;
  description?: string;
  recommended?: boolean;
}

export interface AskItem {
  question: string;
  header: string;
  multi_select?: boolean;
  options: AskOption[];
}

export interface AskAnswer {
  question: string;
  header?: string;
  selected: string[];
  other?: string;
}

/** What the answer endpoint takes for one structured question. */
export interface AskChoice {
  selected: string[];
  other?: string;
}

export interface TodoItem {
  content: string;
  status: "pending" | "in_progress" | "completed";
  active_form?: string;
}

export interface QuestionView {
  id: string;
  thread_no: number | null;
  asker: string;
  task_id: string | null;
  audience: "lead" | "user";
  kind: "question" | "approval";
  text: string;
  options: string[];
  detail: QuestionDetail;
  blocking: boolean;
  answer: string | null;
  status: "open" | "answered" | "cancelled";
  decided_by: string | null;
  created_at: string;
  answered_at: string | null;
}

export interface MessageView {
  id: string;
  role: "user" | "leader" | "employee" | "system" | "error";
  author: string;
  /** chat / note / interrupt, or a chat card: todos, plan, artifact, resumed, image (screenshots an employee uploaded). */
  kind: "chat" | "note" | "interrupt" | "todos" | "plan" | "artifact" | "resumed" | "image";
  content: string;
  meta: {
    thread_no?: number;
    task_id?: string;
    opens_thread?: number;
    request_kind?: string;
    /** A platform note to a thread's lead: the thread looked stalled after a node daemon restart. */
    stalled?: boolean;
    attachments?: AttachmentRef[];
    /** @ references the user put in the message (resolved into context for the employee). */
    refs?: MessageRefChip[];
    /** Chat cards: who the card is about. */
    employee?: string;
    todos?: TodoItem[];
    deliverable?: { id: string; kind: string; title: string; url: string; hosted?: boolean; entry?: string; render?: string; version?: number };
    /** A copy in a thread chat of the user's project-chat message (forwarded). */
    forwarded_from?: ForwardedFrom;
    /** The project-chat note of a forward. */
    forwarded_to?: { no: number; count: number; created: boolean; by: "user" | "coordinator"; message_ids: number[] };
    /** kind=image: the title the employee gave the screenshots. */
    title?: string;
  };
  created_at: string;
}

/** One @ reference of a sent message (meta.refs). */
export interface MessageRefChip {
  kind: "file" | "thread" | "deliverable" | "memory";
  label: string;
  repo?: string;
  path?: string;
  ref?: string;
  no?: number;
  id?: string;
  name?: string;
}

/** An image attached to a message (meta.attachments); served at /api/attachments/{id}. */
export interface AttachmentRef {
  id: string;
  name: string;
  content_type: string;
  size: number;
}

export type AttachmentView = S["AttachmentView"];

export interface MailView {
  id: string;
  sender: string;
  recipient: string;
  body: string;
  created_at: string;
}

export interface ActivityView {
  thread_no?: number | null;
  employee: string;
  text: string;
  at: string;
}

export interface DeliverableView {
  id: string;
  project_id: string;
  thread_no: number | null;
  kind: string;
  title: string;
  url: string;
  detail: {
    summary?: string;
    state?: string;
    version?: number;
    author?: string;
    /** Files published with publish_file and hosted by the console (GET /api/deliverables/{id}/files). */
    hosted?: boolean;
    entry?: string;
    render?: DeliverableRender;
    files?: number;
    [key: string]: unknown;
  };
  published_by: string;
  created_at: string;
  updated_at: string;
}

export type DeliverableRender = "html" | "markdown" | "image" | "pdf" | "text";

export interface DeliverableFile {
  path: string;
  content_type: string;
  size: number;
  sha256: string;
}

export interface DeliverableVersion {
  version: number;
  files: number;
  size: number;
  published_by: string;
  /** Thread number of the employee who published this version (absent for the user and older versions). */
  publisher_thread_no?: number;
  created_at: string;
}

/** One version of a hosted deliverable with a short-lived signed URL. */
export interface DeliverableFiles {
  deliverable_id: string;
  version: number;
  versions: DeliverableVersion[];
  entry: string;
  render: DeliverableRender;
  files: DeliverableFile[];
  base_url: string;
  entry_url: string;
  expires_at: string;
  /** Files are served from their own origin (AGENT_TEAMS_ARTIFACT_ORIGIN). */
  isolated: boolean;
}

export interface DeliverableComment {
  id: string;
  version: number | null;
  text: string;
  sent_to: string;
  created_at: string;
}

export interface MergeEntryView {
  id: string;
  thread_no: number;
  task_id: string;
  employee: string;
  repo?: string;
  head_branch: string;
  status: string;
  attempts: number;
  error: string;
  created_at: string;
  updated_at: string;
}

export interface CheckView {
  name: string;
  status: string;
  conclusion: string | null;
  url: string;
  summary: string;
}

export interface Page<T> {
  items: T[];
  next_cursor: string | null;
}

export interface CoordinatorView {
  conversation_id: string;
  employee: EmployeeBrief;
  session_state: string;
}

export interface WaitingView {
  questions: QuestionView[];
  reviews: ThreadView[];
}

/** orchapi.NodeBrief (office snapshot, overview). */
export interface NodeBrief {
  id: string;
  name: string;
  online: boolean;
  isolation?: string;
  active_sessions: number;
  parked_sessions: number;
  queued: number;
  slots: number;
}

export interface OverviewView {
  project: ProjectView;
  threads: ThreadView[];
  waiting: WaitingView;
  recent_deliverables: DeliverableView[];
  memory: MemoryBrief[];
  usage_7d: { cost_usd_est: number | null; by_day: { day: string; cost_usd_est: number }[] };
  nodes: NodeBrief[];
}

export interface OfficeEmployeeView extends EmployeeBrief {
  thread_no: number | null;
}

export interface OfficeSnapshot {
  layout_id: string;
  layout_version: string;
  threads: ThreadView[];
  employees: OfficeEmployeeView[];
  board: { claimable: number; by_thread?: Record<string, Record<string, number>>; queue?: QueueCardView[] };
  nodes: NodeBrief[];
  merge_queue: MergeEntryView[];
  ci: Record<string, CheckView[]>;
  last_event_id: number;
}

/** One pending task of the project queue (office task board). */
export interface QueueCardView {
  thread_no: number;
  id: string;
  role: string;
  assignee: string;
  locked: boolean;
  reserved_for: string;
}

export interface ReplayEvent {
  id: number;
  at: string;
  type: string;
  data: unknown;
}

export interface OfficeReplay {
  snapshot: OfficeSnapshot;
  events: ReplayEvent[];
}

export interface EmployeeCard extends EmployeeBrief {
  current_task: TaskView | null;
  recent_activity: ActivityView[];
  session: { state: string; node: string | null } | null;
}

export interface ThreadDetail extends ThreadView {
  messages?: MessageView[];
  questions?: QuestionView[];
}

export interface SearchResult {
  threads: ThreadView[];
  messages: (MessageView & { conversation_id?: string })[];
  tasks: TaskView[];
  deliverables: DeliverableView[];
}
