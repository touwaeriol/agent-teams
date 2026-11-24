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
  (`spec.node_ids`); without one the employee is scheduled like any other (lowest load
  score: used quota plus 10 points per live session on the node, see shared-members.md §4.3).
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

## Layout on the node

```
projects/<pid>/threads/1/lead/          ← the CLI's working directory
  <owner>__<repo>/                      ← one worktree per repository
projects/<pid>/repos/<owner>__<repo>    ← shared clone
```

## API

- `POST /api/sessions` `{message, title?, repos: [{repo, git_connection_id | git_repo_id, branch?}], role?, model?, node_id?}`
  → `201 {project, thread_id, thread_no}`.
- `PUT /api/sessions/{pid}/repos` `{repos}` → the project view.
