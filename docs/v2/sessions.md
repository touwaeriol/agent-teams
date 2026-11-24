# 会话（多仓库会话）

A session is a lightweight entry next to projects: the user picks one or more repositories (GitHub
or standalone Git repositories, each from a branch), and one employee works in a folder holding all
of them side by side. The user talks to that employee directly. There is no coordinator, no lead
team and no task pool.

## Data

- A session is a row of `at_projects` with `kind = 'session'` (migration 00420). Projects have
  `kind = 'project'`. The sidebar shows both in one list and tells them apart by icon.
- It has exactly one thread (no. 1). The thread's role snapshot holds one entry: a `lead` with
  `solo = true`, made from the member role the user picked (`orchestrator.SessionRole`). A chosen
  model replaces the role's model. A chosen node becomes the employee's only candidate
  (`spec.node_ids`); without one the employee is scheduled like any other (most quota left).
- The branch picked for each repository is the thread's base there (`at_thread_repos.base_branch`).
  The employee works on `session/<project id[:8]>-<slug>` in every repository.

## Behaviour

- The solo lead writes code: member tools, the role's permission mode (bypass by default), and
  only the session platform tools (`ask_user`, `close_question`, `tell_user`, `set_status`, and
  the deliverable tools). Its prompt lists every repository with its folder and base branch. It has
  no project memory and no roster.
- The user's first message is recorded in the conversation as the user's own message and delivered
  as is. The thread is never reported to a coordinator and gets no stalled note.
- Its own session branch is not a protected ref. The employee pushes and opens PRs itself; nothing
  goes through the merge queue.
- `PUT /api/sessions/{pid}/repos` changes the repositories while the session runs. Added ones are
  checked out at once on the employee's node (from the picked branch). Removed ones are cleaned up
  on that node (unpushed commits are kept). The employee gets a note either way.
- 结束会话 completes the thread. The conversation stays, and the next message reopens it.

## Permission mode, model and effort (#53, migration 00440)

- The user switches them in the session (`/api/projects/{pid}/session-controls`). The pick is the
  session's own: it is kept on the employee (`at_employees.session_mode`, `model`,
  `session_effort`), a reopened session opens with it, and the role is left alone. Unlike a
  thread lead, the session's employee runs the model picked in the session (`modelSteps`).
- Initial mode: Claude opens for `auto` (sent as `default` + `prefer_auto`; the daemon switches to
  `auto` when the model reports `supportsAutoMode`, else stays in `default`, and `session.opened`
  says which). Codex and Antigravity keep the role's mode (bypass or ask).
- Menus by runtime: Claude 自动 `auto` (hidden when the model does not support it) / 手动 `default`
  / 接受编辑 `acceptEdits` / 计划 `plan` / 跳过权限确认 `bypassPermissions`; Codex 自动 (`auto`:
  workspace-write + on-request + `approvalsReviewer: auto_review`) / 手动 / 跳过权限确认;
  Antigravity 手动 (ask) / 跳过权限确认 (auto). 跳过权限确认 shows only when the role lets the
  employee skip permission prompts (not an ask role). Claude switches at once; Codex and
  Antigravity from the next turn. Effort levels come from the model (`init.models[].efforts`);
  Antigravity's effort is part of the model id.
- A switch goes to the live session as `session.set_mode` with a `request_id`; it is shown
  pending until `session.mode_set` comes back (protocol.md §4.5) and the web shows what the
  server wrote back (`at_agent_sessions.permission_mode` and `controls`). A failed switch is
  shown with its error and the employee's picks go back to what still runs. Daemons without the
  `session_controls` feature never answer: the switch counts as in force at once.
- A daemon without `session_controls` (older nodes): the session opens in the role's mode as
  before (no `prefer_auto`), the menu keeps Claude's `default` / `acceptEdits` /
  `bypassPermissions` (none for Codex and Antigravity), there is no effort, and `set_mode` goes out
  without `plan`, `effort` and `request_id` (`node_outdated: true` in the view).
- Switching to a model without the picked effort level drops the pick (the model's default).
- On a failed switch only the picks that switch changed go back.
- A switch with no answer within 2 minutes (a daemon that restarted meanwhile) ends the same way: the
  picks it changed go back to what the session runs and the view carries the error
  `节点没有回应，切换未生效` (done on the next GET or PUT); an effort a model switch dropped comes
  back too. If the node answers after all, the session and the employee's picks follow its answer.
- Approvals of a session go to its user directly (no lead review) and never time out
  (`approval_timeout_s` does not apply to projects of kind session). The platform allows a request
  without a card only while the session runs in `bypassPermissions` (`SkipsApprovals`).

## Layout on the node

```
projects/<pid>/threads/1/lead/          ← the CLI's working directory
  <owner>__<repo>/                      ← one worktree per repository
projects/<pid>/repos/<owner>__<repo>    ← shared clone
```

## API

- `POST /api/sessions` `{message, title?, repos: [{repo, git_connection_id | git_repo_id, branch?}], role?, model?, node_id?, branch?, permission_mode?, effort?}`
  → `201 {project, thread_id, thread_no}`. `permission_mode` (`auto` / `default` / `acceptEdits` /
  `plan` / `bypassPermissions`) and `effort` are the new-session page's picks; left out = the
  session's default (Claude: `auto`, or `default` when the model lacks it; Codex and Antigravity:
  the role's). They are checked against the role's runtime and model like a switch (422 with
  `field` for a value the session cannot take) and kept on the employee (`session_mode`,
  `session_effort`), so the first turn already runs with them.
- `GET /api/session-options?role=&model=` → `SessionOptions {runtime, default_mode, model, modes,
  models, efforts}`: what a new session of that role (and model; empty = the role's) can start
  with, for the new-session page. Same shapes as `SessionControls`; `models` come from the user's
  latest session of that runtime that reported them (`init.models`), empty before the first one.
- `PUT /api/sessions/{pid}/repos` `{repos}` → the project view.
- `GET /api/projects/{pid}/session-controls` → `SessionControls {thread_id, runtime, live,
  permission_mode, model, effort, pending: {permission_mode?, model?, effort?, since} | null,
  error?, modes: [{id, label, hint, applies: now|next_turn}], models: [{id, name?, description?,
  efforts, default_effort?, supports_auto, default?}], efforts, node_outdated}`; 409 `not_session` for a project.
- `PUT /api/projects/{pid}/session-controls` `{permission_mode?, model?, effort?}` → the same view
  with `pending` set (live session on a `session_controls` daemon). 422 for a value the session
  cannot take, 409 `switch_pending` while the previous switch has no answer (at most 2 minutes).
  The answer comes as a `session_controls` project event `{thread_id, applied, error?}`: refetch.
  (Not under `/api/sessions/{pid}`: gin's GET tree has `/api/sessions/:sid/stream` there.)
