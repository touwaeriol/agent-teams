# Shared folder (JuiceFS, one volume per user)

A persistent network folder mounted on every node of a user. Employees use it like a local
directory; it survives sessions, threads and node moves. Code stays in the node-local git worktree.

## Layout

One JuiceFS volume per user, `at-<id>` (`id` = first 16 hex of sha256("agent-teams-user-fs|"+user_id)):

```
/projects/<project_id>/                     one per project ($AGENT_TEAMS_PROJECT_DIR)
/projects/<project_id>/attachments/         attachments fetched with read_attachment
/projects/<project_id>/.deliverables/<id>/v<N>/   published deliverable versions (platform-kept)
/projects/<project_id>/.memory/             the project's auto memory (MEMORY.md + topic files)
/shared/                                    every project of the user ($AGENT_TEAMS_SHARED_DIR)
/shared/.memory/                            the user's own memory, read by every project
```

Every session's working directory (`projects/<pid>/threads/<no>/<emp>/` on the node) holds the
project's repositories as `owner__repo` plus three links: `.project-files` → the project's folder,
`.user-files` → `shared/`, `.attachments` → the project's `attachments/`. The system prompt explains
them. There are no file tools: employees read and write with their own file tools; `publish_file`
also accepts files under `.project-files/` and `.user-files/`. A session opened without the folder
(switched off, not mounted on that node) gets the three links removed (only those symlinks, never an
employee's files), so attachments fall back to a local `.attachments/`. Attachments are downloaded
into `attachments/` only while it is a real directory inside the volume; an employee who swaps in a
symlink gets the download refused.

Projects are only softly separated: a session can reach the user's other project folders. The
boundary that matters is between users, and that one is hard (separate metadata role and schema,
separate S3 user).

## Backends

- **Metadata:** PostgreSQL. Each volume has a role and schema `jfs_<id>` (search_path, no access to
  console tables, CONNECTION LIMIT 30). The console creates them through `AGENT_TEAMS_FS_META_ADMIN_URL`
  (default `DATABASE_URL`), and sets the role's password as a SCRAM-SHA-256 verifier computed in
  Go, so the plain password never reaches PostgreSQL or its statement log. The console's own
  metadata URLs (format, destroy, gateway) take `sslmode` (default `disable`) and the certificate
  files (`sslrootcert`, `sslcert`, `sslkey`) from that URL. Nodes reach PostgreSQL at
  `AGENT_TEAMS_FS_META_HOST` over TLS. In
  production this is a dedicated PostgreSQL (not the console's database), so the port opened to
  nodes leads only to volume metadata; it needs its own daily backup, since a volume cannot be read
  without its metadata.
- **Data:** one S3 bucket (`AGENT_TEAMS_FS_BUCKET`, RustFS in production). Volume data lives under
  `<bucket>/at-<id>/`, and an S3 user `jfs<id>` with policy `jfs-<id>` may touch only that prefix.
  `juicefs format` stores those keys in the volume's metadata, so mounting needs only the metadata
  URL and password.
- Table `at_user_fs` (migration 00490) holds the names, sealed secrets (credvault, AAD
  `UserFsAAD`), `generation` and state provisioning | ready | error | deleting. `generation`
  comes from the sequence `at_user_fs_generation`: the volume name is derived from the user id, so a
  folder switched off and on again has the same name, but never a generation a node may still have
  mounted; any reseal of the secrets must take a new one (it is part of the AAD).
- Only one call works on a volume at a time: `Enable` provisions only when its insert created the
  row or it claimed a failed provisioning (a conditional `UPDATE … RETURNING`), and `Disable` only
  when it moved a ready or failed volume to deleting; a provisioning or deletion left standing for
  15 minutes (console stopped) can be taken over. Provisioning runs detached from the request.
- **On by default.** Every user gets a folder without asking: one console replica (leader lock)
  runs `engine.runSharedFolders` at start and every minute; it provisions users without a volume,
  failed volumes 10 minutes after their last attempt and provisionings left standing
  (`Service.ProvisionAll`), then moves deliverable versions still kept in the database into their
  owners' folders (`artifacts.Store.MoveToVolumes`). `POST /api/storage/fs` retries a failed one at
  once. Users cannot switch it off; it is destroyed only with the account.
- Storage is always the platform's (no user-supplied S3 or JuiceFS).

## Quota

Each volume has a quota (`AGENT_TEAMS_FS_CAPACITY_GB`, default 20 GiB, 0 = none), set by
`juicefs format --capacity`.

## Mounting

| Node | How | Feature |
| --- | --- | --- |
| Host install with FUSE (`/dev/fuse` + `fusermount3`, or macFUSE) | The daemon downloads the pinned juicefs client (sha256 from the console), runs `juicefs mount -d` at `<data>/mnt/<volume>`, checks it every minute and remounts | `fs_mount` |
| Docker node | The host mounts it through the official volume plugin (`juicedata/juicefs`); the container gets `AGENT_TEAMS_FS_DIR=/home/agent/.fs`, keeps zero privileges and never sees credentials | `fs_external` |
| Existing container without the plugin | One-time hot mount: mount on the host, graft into the container with `open_tree`/`move_mount` (operations only, not in code) | `fs_external` |

`fs.mount` carries the metadata password, so it goes **only on the live link** (unreliable, never
stored in the outbox). The console sends it when a node with `fs_mount` comes online and when the
volume becomes ready. `fs.unmount` follows a deletion. The daemon reports `fs.status`, which is
stored in `at_nodes.fs_status` and shown on the node page; it is reset to `{}` when the node says
hello and when its link drops, so a stored status is always the live link's.

The daemon ignores an `fs.mount` whose generation is older than the mounted one. It records the
mounted volume and generation (never secrets) in its state directory, so after a daemon restart an
`fs.mount` of the same generation adopts the still-running `juicefs` mount instead of remounting it,
and `fs.unmount` still finds `<data>/mnt/<volume>`. The session directories are created only while
the mount point really is a JuiceFS mount, never in the bare directory.

In external mode the daemon cannot remount. When the volume at `AGENT_TEAMS_FS_DIR` was mounted and
then goes away (the plugin's mount process died), the daemon logs an error and exits with code 76;
the launcher exits with it, and the container's restart policy (`restart: unless-stopped`) starts a
new container, whose volume the plugin mounts again. A volume that was never mounted does not cause
exits (no crash loop when the plugin is not set up).

`session.open.fs` (`project`, `shared`, both relative to the volume root) is set only when the volume
is ready and the node reports that volume mounted at its current generation (an external mount,
reported without them, counts unless it names another volume). The daemon creates both directories, adds them to Claude's
`additionalDirectories` and sandbox `allowWrite`, and to Codex's workspace-write `writable_roots`,
exports the two variables, and appends a short note to the system prompt.

## Memory

Auto memory (architecture.md §6.7) lives in the folder: a project's in `projects/<pid>/.memory/`,
the user's own (preferences every project reads) in `shared/.memory/`. Each is flat Markdown:
`MEMORY.md` (the index every session reads at start) and topic files that start with frontmatter
(`name`, a one-line `description`, optional `type: project|feedback|reference|user`).

- **The files are the source.** Every tier edits them with its own file tools: members as any file;
  the coordinator and read-only leads through `claude.memory_files` (the daemon's PreToolUse hook
  allows Read/Glob/Grep/Edit/Write only inside the two `.memory/` directories, by real path; the
  coordinator gets just those, linked into its working directory). Codex leads (read-only sandbox)
  and sessions without the folder use the `memory_*` tools (`memory_edit` replaces part of a file;
  `scope: "user"` for the user's own).
- **The rows are the cache** (`at_memory_files`, `at_user_memory_files`): session prompts, the web
  pages and a folder out of reach read them. A write through the platform (tools, thread summaries,
  the web page) lands there as `pending` and is written out by `memfiles.Syncer` right after (and by
  the minute sweep); a pending row wins over a file changed meanwhile.
- **Changes on a node come back** with the sweep: a file whose mtime or size differs from what the
  row last saw is read (frontmatter parsed, content clipped to 32,000 characters), a new topic file
  gets its index line in MEMORY.md, a deleted one loses it (MEMORY.md itself is written again). The
  sweep reads the scopes with pending rows or a fresh touch every minute, every ready folder once an
  hour, and the web page reads its scope before listing.
- **Who changed it**: an employee's tool call whose summary names a `.memory/` file records a touch
  (`at_memory_touches`, 10 minutes) and has the folder read 5 seconds later; the change is credited
  to that employee, otherwise to `节点上直接修改`.
- Deleting through the platform removes the file first; with the folder out of reach a file already
  written out is refused (it would come back).
- Moving over: migration 00500 marks every existing row pending, so the first sweep writes all
  memory into the folders.

## Console access

The console never mounts a volume. For each volume in use it runs `juicefs gateway` as a child
process on 127.0.0.1 (random port and key, bucket `files`, no background jobs, no cache), stopped
after 10 idle minutes and before the volume is deleted, and talks to its S3 API
(`userfs.Gateways`, `userfs.S3Files`). A gateway starts outside the registry lock (callers of the
same volume wait for one start; other volumes are not held up). Each call, and each opened file
until it is closed, holds its gateway in use, so the idle reaper never stops one mid-transfer. The
gateways are stopped at console shutdown, and on Linux get `Pdeathsig` so they never outlive the
console. Bodies that cannot be rewound (attachment readers, object-store downloads) are uploaded
with an unsigned payload, since the gateway speaks plain http. Through it:

- the project's 共享文件 page lists, uploads, downloads and deletes `projects/<pid>/`
  (`/api/projects/{pid}/files…`, api.md §3.5.1) and saves attachments there;
- deliverable versions are written and read (`artifacts.Store`): `publish_file` writes
  `projects/<pid>/.deliverables/<id>/v<N>/` and `at_deliverable_files` keeps only the metadata
  (`in_volume`); they never go to the database again. Until the owner's folder is ready a publish is
  refused (`ErrFolderNotReady`: a tool refusal, 409 `folder_not_ready` for a library upload). Only a
  console without `AGENT_TEAMS_FS_*` (local development) keeps them in the database. Signed URLs
  and `read_deliverable` read the files back and refuse one whose size or sha256 changed. The page
  cannot write into `.deliverables/` (403 `managed_folder`), and employees are told to leave it
  alone. Older versions still in the database are moved in by the sweep above.

## Deleting

Only deleting the account removes the folder (admin `DELETE /api/admin/users/{id}` → hook
`UserDeleting` → `Service.Disable`). The volume is marked deleting, `fs.unmount` goes to the
user's nodes, and the deletion runs in the background from the secrets read at that moment (the row
goes with the user):

1. wait (60 s at most) until no node whose daemon mounts the volume still reports it mounted. A
   Docker node (external mount) cannot unmount on request: the volume is destroyed under it, which
   is logged;
2. stop the console's gateway, `juicefs destroy` (skipped when the role or schema is not there:
   provisioning failed before creating them, or an earlier attempt dropped them), remove the S3
   user and policy, drop the schema and role, delete the row.

## Moving old data in

`atctl storage import-shared` copies the old platform shared files (`at_shared_files`) into each
owner's `projects/<pid>/`, switching the folder on where needed and skipping files already there
with the same size; then it moves deliverable versions kept in the database into the volumes of
owners whose folder is on. Both steps can be re-run. File bytes are read one file at a time, and a
user whose folder cannot be switched on counts as one failure. A later migration drops `at_shared_files` and
`at_shared_uploads`.

## Consistency

JuiceFS gives close-to-open consistency. SQLite works there with a single writer at a time
(rollback journal; WAL only from one machine). Two machines writing one SQLite file lose
committed data.

## Not in v1

- Per-project quotas (`juicefs quota`) and a tool fallback for nodes without a mount.
