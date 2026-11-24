import type { components } from "@/api/schema";
import type { MessageView, QuestionView, TaskView, ThreadView } from "@/api/views";

type ContextDetailView = components["schemas"]["ContextDetailView"];

export function thread(no: number, patch: Partial<ThreadView> = {}): ThreadView {
  return {
    id: `t${no}`, project_id: "p1", no, title: `T${no}`, brief: "brief", status: "running", queue_position: null, pause_reason: "", branch: `thread/${no}`,
    base_branch: "main", pr_number: null, pr_url: null, pr_state: null, merge_error: null, merge_policy: "manual", summary: "", auto_approve: false, opened_by: "coordinator", members: [], main_tasks: [],
    counts: { waiting: 0, questions_open: 0, approvals_open: 0, tasks_total: 0, tasks_done: 0, tasks_open: 0, main_total: 0, main_done: 0 }, lead_last: null, cost_usd_est: null,
    created_at: "2026-11-14T10:00:00Z", updated_at: "2026-11-14T10:00:00Z", ...patch,
  };
}

export function question(id: string, patch: Partial<QuestionView> = {}): QuestionView {
  return {
    id, thread_no: 12, asker: "dev-2", task_id: null, audience: "user", kind: "question", text: "?", options: [], detail: {}, blocking: true, answer: null,
    status: "open", decided_by: null, created_at: "2026-11-14T10:00:00Z", answered_at: null, ...patch,
  };
}

export function message(id: string, patch: Partial<MessageView> = {}): MessageView {
  return { id, role: "leader", author: "leader", kind: "chat", content: "hi", meta: {}, created_at: "2026-11-14T10:00:00Z", ...patch };
}

export function task(id: string, patch: Partial<TaskView> = {}): TaskView {
  return {
    id, thread_no: 12, main_task_id: "m1", assignee: "", role: "dev", claimed_by: "", kind: "dev", instruction: `do ${id}\nmore`, status: "pending", depends_on: [],
    parent: "", priority: 0, attempt: 1, summary: "", branch: "", head_sha: "", created_at: "", claimed_at: null, finished_at: null, ...patch,
  };
}

/** GET /api/conversations/{cid}/context/detail of a Claude session (the design's numbers, #53). */
export function contextDetail(patch: Partial<ContextDetailView> = {}): ContextDetailView {
  return {
    supported: true, runtime: "claude", session_id: "s_1", state: "active", can_refresh: true, detail: "summary", updated_at: "2026-11-24T10:00:00Z",
    model: "claude-opus-5-5", total_tokens: 77000, max_tokens: 1000000, raw_max_tokens: 1000000, auto_compact: true, auto_compact_threshold: 967000,
    categories: [
      { id: "system_prompt", name: "System prompt", tokens: 3200, kind: "used" },
      { id: "system_tools", name: "System tools", tokens: 16800, kind: "used" },
      { id: "mcp_tools", name: "MCP tools", tokens: 9600, kind: "used" },
      { id: "mcp_instructions", name: "MCP server instructions", tokens: 1100, kind: "used" },
      { id: "memory_files", name: "Memory files", tokens: 2400, kind: "used" },
      { id: "skills", name: "Skills", tokens: 1900, kind: "used" },
      { id: "messages", name: "Messages", tokens: 42000, kind: "used" },
      { id: "autocompact_buffer", name: "Autocompact buffer", tokens: 33000, kind: "buffer" },
      { id: "free_space", name: "Free space", tokens: 890000, kind: "free" },
      { id: "mcp_tools_deferred", name: "MCP tools (deferred)", tokens: 12400, kind: "deferred" },
      { id: "system_tools_deferred", name: "System tools (deferred)", tokens: 8700, kind: "deferred" },
    ],
    mcp_tools: [
      { name: "mcp__platform__publish_file", server: "platform", tokens: 1380, loaded: true },
      { name: "mcp__platform__ask_user", server: "platform", tokens: 1240, loaded: true },
      { name: "mcp__platform__set_status", server: "platform", tokens: 410, loaded: true },
      { name: "mcp__plugin_github_github__create_pull_request_review_comment_with_suggestions", server: "plugin_github_github", tokens: 1580, loaded: true },
      { name: "mcp__plugin_github_github__search_code", server: "plugin_github_github", tokens: 1000, loaded: false },
    ],
    mcp_servers: [{ server: "platform", tokens: 1100 }],
    memory_files: [{ path: "/w/CLAUDE.md", type: "Project", tokens: 2400 }],
    skills: [{ name: "design", source: "plugin", tokens: 1900 }],
    ...patch,
  };
}
