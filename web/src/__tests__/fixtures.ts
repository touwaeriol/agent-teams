import type { MessageView, QuestionView, TaskView, ThreadView } from "@/api/views";

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
