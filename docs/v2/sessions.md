# 会话（多仓库会话）

A session is a lightweight entry next to projects: the user picks one or more repositories (GitHub
or standalone Git repositories, each from a branch), and one employee works in a folder holding all
of them side by side. The user talks to that employee directly. There is no coordinator, no lead
team and no task pool.

## Data

- A session is a row of `at_projects` with `kind = 'session'` (migration 00420). Projects have
  `kind = 'project'`. The sidebar shows both in one list and tells them apart by icon.
- It has exactly one thread (no. 1). The thread's role snapshot holds one entry: a `lead` with
  `solo = true`, made from the member role the user picked (`orchestrator.SessionRole`). The role
  is only a template: the employee is not one of its staff and does not take the role's nodes.
  The agent (`runtime`) is picked among the role's runtimes (`role.runtimes`, its first by
  default); the session keeps to it, with the role's model for that runtime unless a model was
  picked (a chosen model replaces it). Without a runtime pick, a chosen model applies to the
  role's first runtime; with neither the session may run on any of the role's runtimes. A chosen node becomes the employee's only candidate
  (`spec.node_ids`); without one the employee is scheduled like any other (lowest load
  score: used quota plus 10 points per live session on the node, see shared-members.md §4.3).
- The branch picked for each repository is the thread's base there (`at_thread_repos.base_branch`).
  The employee works on `session/<project id[:8]>-<slug>` in every repository.
- Repositories are optional (#53 t48). A session without any is a **local folder**: the
  employee works in its own directory on the node (`projects/<pid>/threads/1/lead`, `PathsOf`),
  with no git repository; the files exist only on that machine. Its employee spec carries
  `local: true`, and the first session that opens pins it to that node (`spec.node_ids =
  [node]`, `pinLocal`): it is never moved (`MoveOnLimit` refuses, `pickNode` has only that
  candidate), and while the node is offline the session waits for it (the employee shows
  offline → 等你处理, t25). 已解决 does not clean the folder up (`ThreadEnded` skips local
  sessions; the folder is not recorded as a checkout in `at_thread_nodes`), so the next message
  goes on in the same folder on the same node. Deleting the session purges `projects/<pid>` on
  its nodes (`ProjectDeleting` → `Delivery.Purge`). Repositories added later are checked out
  inside the folder (`./owner__repo`); the session stays pinned (the folder may hold files outside
  git). Removing every repository from a session with repositories makes it local (pinned to the
  node it is on).
- Its prompt says so (`soloLocalGuide` and a `## 工作目录` section instead of `## 仓库`): a local
  folder, no git, files only on this machine; nothing about pushing or PRs.

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
- 已解决 (the confirm bar under a finished round) completes the thread. The conversation stays,
  and the next message reopens it.
- New session (`/sessions/new`, the sidebar's 新建): a clean conversation window with the box at
  the bottom; under it node (自动选择 or one of the user's nodes), role, agent (a read-only tag when
  the role has one runtime), model, effort and permission mode. Once the session starts, node,
  role and agent are fixed: the session page shows them read-only (the box's controls row and
  会话信息); model, effort and mode can still be switched (session controls below).

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
- Switching to a model without the picked effort level drops the pick (the model's default). The
  daemon leaves the old level out of `session.mode_set` and the session record (it knows the levels
  from the session's init models); the server ignores a level an older daemon still reports there.
- On a failed switch only the picks that switch changed go back.
- A switch with no answer within 2 minutes (a daemon that restarted meanwhile) ends the same way: the
  picks it changed go back to what the session runs and the view carries the error
  `节点没有回应，切换未生效` (done on the next GET or PUT); an effort a model switch dropped comes
  back too; an effort pick that followed the default (none on the employee) keeps following it. If
  the node answers after all, the session follows its answer, and so do the employee's picks unless
  a newer switch is waiting (its own answer settles them). A switch that took also sets the
  employee's mode and model to what the session reports (the effort only when that switch changed it).
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

- `POST /api/sessions` `{message, title?, repos: [{repo, git_connection_id | git_repo_id, branch?}] (may be empty: a local folder), role?, runtime?, model?, node_id?, branch?, permission_mode?, effort?}`
  → `201 {project, thread_id, thread_no}`. `permission_mode` (`auto` / `default` / `acceptEdits` /
  `plan` / `bypassPermissions`) and `effort` are the new-session page's picks; left out = the
  session's default (Claude: `auto`, or `default` when the model lacks it; Codex and Antigravity:
  the role's). They are checked against the role's runtime and model like a switch (422 with
  `field` for a value the session cannot take) and kept on the employee (`session_mode`,
  `session_effort`), so the first turn already runs with them. `runtime` is the agent, one of the
  role's runtimes (422 with `field: "runtime"` otherwise); modes and efforts are checked against it.
  A picked `node_id` must be able to run the agent: a runtime that needs isolation on a node
  without it, or a node whose CLI report (`at_node_runtimes`, runtime_status) lacks the agent's
  install or login, is refused (422, `field: "node_id"`, the reason in the message). The
  new-session page greys those nodes out in its node menu with the same reasons.
- `GET /api/session-options?role=&runtime=&model=` → `SessionOptions {runtime, default_mode, model, modes,
  models, efforts}`: what a new session of that role, agent (`runtime`, one of the role's; empty =
  its first; 422 otherwise) and model (empty = the role's for the runtime) can start with, for the new-session page. Same shapes as `SessionControls`; `models` come from the user's
  latest session of that runtime that reported them (`init.models`), empty before the first one.
- `PUT /api/sessions/{pid}/repos` `{repos}` → the project view. `repos` may be empty (every
  repository removed: the session goes on in its local folder, pinned to its node).
- `GET /api/projects/{pid}/session-controls` → `SessionControls {thread_id, runtime, live,
  permission_mode, model, effort, pending: {permission_mode?, model?, effort?, since} | null,
  error?, modes: [{id, label, hint, applies: now|next_turn}], models: [{id, name?, description?,
  efforts, default_effort?, supports_auto, default?}], efforts, node_outdated}`; 409 `not_session` for a project.
- `PUT /api/projects/{pid}/session-controls` `{permission_mode?, model?, effort?}` → the same view
  with `pending` set (live session on a `session_controls` daemon). 422 for a value the session
  cannot take, 409 `switch_pending` while the previous switch has no answer (at most 2 minutes).
  The answer comes as a `session_controls` project event `{thread_id, applied, error?}`: refetch.
  (Not under `/api/sessions/{pid}`: gin's GET tree has `/api/sessions/:sid/stream` there.)
