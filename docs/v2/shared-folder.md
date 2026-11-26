# Shared folder (JuiceFS, one volume per user)

A persistent network folder mounted on every node of a user. Employees use it like a local
directory; it survives sessions, threads and node moves. Code stays in the node-local git worktree.

## Layout

One JuiceFS volume per user, `at-<id>` (`id` = first 16 hex of sha256("agent-teams-user-fs|"+user_id)):

```
/projects/<project_id>/                     one per project ($AGENT_TEAMS_PROJECT_DIR)
/projects/<project_id>/attachments/         attachments fetched with read_attachment
/projects/<project_id>/.deliverables/<id>/v<N>/   published deliverable versions (platform-kept)
/shared/                                    every project of the user ($AGENT_TEAMS_SHARED_DIR)
```

Every session's working directory (`projects/<pid>/threads/<no>/<emp>/` on the node) holds the
project's repositories as `owner__repo` plus three links: `.project-files` → the project's folder,
`.user-files` → `shared/`, `.attachments` → the project's `attachments/`. The system prompt explains
them. There are no file tools: employees read and write with their own file tools.

Projects are only softly separated: a session can reach the user's other project folders. The
boundary that matters is between users, and that one is hard (separate metadata role and schema,
separate S3 user).

## Backends

- **Metadata:** PostgreSQL. Each volume has a role and schema `jfs_<id>` (search_path, no access to
  console tables, CONNECTION LIMIT 30). The console creates them through `AGENT_TEAMS_FS_META_ADMIN_URL`
  (default `DATABASE_URL`). Nodes reach PostgreSQL at `AGENT_TEAMS_FS_META_HOST` over TLS. In
  production this is a dedicated PostgreSQL (not the console's database), so the port opened to
  nodes leads only to volume metadata; it needs its own daily backup, since a volume cannot be read
  without its metadata.
- **Data:** one S3 bucket (`AGENT_TEAMS_FS_BUCKET`, RustFS in production). Volume data lives under
  `<bucket>/at-<id>/`, and an S3 user `jfs<id>` with policy `jfs-<id>` may touch only that prefix.
  `juicefs format` stores those keys in the volume's metadata, so mounting needs only the metadata
  URL and password.
- Table `at_user_fs` (migration 00490) holds the names, sealed secrets (credvault, AAD
  `UserFsAAD`), `generation` (grows when credentials change: nodes remount) and state
  provisioning | ready | error | deleting.
- Storage is always the platform's (no user-supplied S3 or JuiceFS).

## Quota

Each volume has a quota (`AGENT_TEAMS_FS_CAPACITY_GB`, default 20 GiB, 0 = none), set by
`juicefs format --capacity`.

## Mounting

| Node | How | Feature |
| --- | --- | --- |
| Host install with FUSE (`/dev/fuse` + `fusermount3`, or macFUSE) | The daemon downloads the pinned juicefs client (sha256 from the console), runs `juicefs mount -d` at `<data>/mnt/<volume>`, checks it every minute and remounts | `fs_mount` |
| Docker node | The host mounts it through the official volume plugin (`juicedata/juicefs`); the container gets `AGENT_TEAMS_FS_DIR=/home/agent/fs`, keeps zero privileges and never sees credentials | `fs_external` |
| Existing container without the plugin | One-time hot mount: mount on the host, graft into the container with `open_tree`/`move_mount` (operations only, not in code) | `fs_external` |

`fs.mount` carries the metadata password, so it goes **only on the live link** (unreliable, never
stored in the outbox). The console sends it when a node with `fs_mount` comes online and when the
volume becomes ready. `fs.unmount` follows a deletion. The daemon reports `fs.status`, which is
stored in `at_nodes.fs_status` and shown on the node page.

`session.open.fs` (`project`, `shared`, both relative to the volume root) is set only when the volume
is ready and the node reports it mounted. The daemon creates both directories, adds them to Claude's
`additionalDirectories` and sandbox `allowWrite`, and to Codex's workspace-write `writable_roots`,
exports the two variables, and appends a short note to the system prompt.

## Console access

The console never mounts a volume. For each volume in use it runs `juicefs gateway` as a child
process on 127.0.0.1 (random port and key, bucket `files`, no background jobs, no cache), stopped
after 10 idle minutes and before the volume is deleted, and talks to its S3 API
(`userfs.Gateways`, `userfs.S3Files`). Through it:

- the project's 共享文件 page lists, uploads, downloads and deletes `projects/<pid>/`
  (`/api/projects/{pid}/files…`, api.md §3.5.1) and saves attachments there;
- deliverable versions are written and read (`artifacts.Store`): with the owner's folder on,
  `publish_file` writes `projects/<pid>/.deliverables/<id>/v<N>/` and `at_deliverable_files` keeps
  only the metadata (`in_volume`); signed URLs and `read_deliverable` read the files back and refuse
  one whose size changed. The page cannot write into `.deliverables/` (403 `managed_folder`), and
  employees are told to leave it alone. Versions from before the folder was on stay in the database.

## Moving old data in

`atctl storage import-shared` copies the old platform shared files (`at_shared_files`) into each
owner's `projects/<pid>/`, switching the folder on where needed and skipping files already there
with the same size; then it moves deliverable versions kept in the database into the volumes of
owners whose folder is on. Both steps can be re-run. A later migration drops `at_shared_files` and
`at_shared_uploads`.

## Consistency

JuiceFS gives close-to-open consistency. SQLite works there with a single writer at a time
(rollback journal; WAL only from one machine). Two machines writing one SQLite file lose
committed data.

## Not in v1

- Per-project quotas (`juicefs quota`) and a tool fallback for nodes without a mount.
