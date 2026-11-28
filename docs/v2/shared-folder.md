# Shared folder (geesefs on the object store)

A persistent network folder mounted on every node of a user. Employees use it like a local
directory; it survives sessions, threads and node moves. Code stays in the node-local git worktree.

## Layout

One folder per user on the platform's object store, named by `id` = the first 16 hex of
sha256("agent-teams-user-fs|"+user_id), so no user id reaches the store:

- **rustfs** (default, `AGENT_TEAMS_FS_PROVIDER=rustfs`; RustFS or MinIO): the user's own bucket
  `atu-<id>`, with a hard quota.
- **b2** (Backblaze B2): the prefix `users/<id>/` of one platform bucket (`AGENT_TEAMS_FS_BUCKET`).

Inside it:

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
into `attachments/` only while it is a real directory inside the folder; an employee who swaps in a
symlink gets the download refused.

Projects are only softly separated: a session can reach the user's other project folders. The
boundary that matters is between users, and that one is hard: each user's key reaches its own
folder only.

## Backends

`userfs.Store` is the provider; `rustfs` and `b2` implement it.

| | rustfs | b2 |
| --- | --- | --- |
| Folder | bucket `atu-<id>` | `<bucket>/users/<id>/` |
| User key | IAM user `atu<id>`, policy `atu-<id>` allowing that bucket only (`FolderPolicy`) | application key `atu-<id>` (`b2_create_key`) limited to the bucket and the prefix |
| Capacity | bucket hard quota (`SetBucketQuota`) | soft: the `shared-folder-quota` job (15 min) counts each folder; over the capacity the user's key is replaced by one without `writeFiles` (it can still read and delete), back under it by a full one |
| Bucket setup | created by the console with a lifecycle rule (abort unfinished uploads after a day) and CORS for the console's public URL | exists already; lifecycle (keep only the last version, cancel unfinished large files) set on B2 |
| Platform key | admin key (buckets, users, policies, quotas) | application key with `writeKeys` and the file capabilities on the bucket |

Policies allow list, read, write (unless read-only) and delete inside the folder; never bucket
creation or deletion, quota or policy changes. A key is revoked by deleting it (the IAM user, the
B2 key): every node of the user loses the folder at once.

- Table `at_user_fs` (migrations 00490, 00530) holds the folder (`bucket`, `prefix`: empty for a
  bucket of its own), the key id (`s3_access_key`), its sealed secret (credvault, AAD
  `UserFsAAD(user, bucket:prefix, generation)`), `read_only`, `generation` and state
  provisioning | ready | error | deleting. `generation` comes from the sequence
  `at_user_fs_generation` and grows with every new key (provisioned again, switched to or from
  read-only): nodes remount when it changes, and it is part of the AAD.
- Only one call works on a folder at a time: `Enable` provisions only when its insert created the
  row or it claimed a failed provisioning (a conditional `UPDATE … RETURNING`), and `Disable` only
  when it moved a ready or failed folder to deleting; a provisioning or deletion left standing for
  15 minutes (console stopped) can be taken over. Provisioning runs detached from the request and
  issues a new key each attempt (the earlier attempt's key is revoked).
- **On by default.** Every user gets a folder without asking: the `shared-folders` job (every
  minute) provisions users without a folder, failed ones 10 minutes after their last attempt and
  provisionings left standing (`Service.ProvisionAll`), then moves deliverable versions still kept in
  the database into their owners' folders. `POST /api/storage/fs` retries a failed one at once.
  Users cannot switch it off; it is destroyed only with the account.
- The `orphan-volumes` job removes folders and keys named like a user's that no row owns, at the
  second sweep that finds them.
- Storage is always the platform's (no user-supplied S3).

## Mounting

Nodes mount with **geesefs** (an S3 FUSE client, v0.43.9 pinned with its sha256 per platform in
`userfs.DefaultAssets`; `AGENT_TEAMS_FS_CLIENT_VERSION` + `AGENT_TEAMS_FS_CLIENT_SHA256` override).
The daemon uses `/opt/geesefs/geesefs-<version>` when its sha256 matches (the node image has it),
otherwise downloads the release binary from GitHub into `<data>/tools/`.

| Node | How | Feature |
| --- | --- | --- |
| Host install with FUSE (`fuse3`: `/dev/fuse` + `fusermount3`, or macFUSE) | The daemon runs geesefs itself at `<data>/mnt/atu-<id>` (or `AGENT_TEAMS_FS_MOUNT_DIR`), as its own user | `fs_s3_mount` |
| Docker node (`deploy/node`) | The same inside the container at `/home/agent/.fs`; the container needs `devices: [/dev/fuse]`, `cap_add: [SYS_ADMIN]`, `security_opt: [apparmor:unconfined]` | `fs_s3_mount` |
| Mounted by something else at `AGENT_TEAMS_FS_DIR` | The daemon only uses and reports it | `fs_external` |

geesefs runs with `--stat-cache-ttl 5s --memory-limit 1000 --fsync-on-close`: a file changed on
another node shows within 5 s, and `close` waits for the upload, so a closed file is in the store.
Its key reaches it as `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` in its environment, never on its
command line. The daemon checks the mount every minute and remounts; on exit it unmounts
(`fusermount3 -u`, lazily when busy) and waits for geesefs to exit, so writes it still holds are
uploaded.

`fs.mount` (`name`, `bucket`, `prefix`, `generation`, `endpoint`, key, client) carries the secret
key, so it goes **only on the live link** (unreliable, never stored in the outbox). The console
sends it when a node with `fs_s3_mount` comes online and whenever the folder or its key change.
`fs.unmount` follows a deletion. The daemon reports `fs.status` (with `name` and `generation`),
which is stored in `at_nodes.fs_status` and shown on the node page; it is reset to `{}` when the node
says hello and when its link drops. Daemons of the earlier JuiceFS mount (`fs_mount`) get nothing.

The daemon ignores an `fs.mount` whose generation is older than the mounted one. It records the
mounted name and generation (never secrets) in its state directory, so after a daemon restart an
`fs.mount` of the same generation adopts the still-running mount. The session directories are
created only while the mount point really is a mount point.

In external mode the daemon cannot remount: when the folder at `AGENT_TEAMS_FS_DIR` was mounted and
then goes away, the daemon exits with code 76 and the container's restart policy brings it back.

`session.open.fs` (`project`, `shared`, both relative to the folder root) is set only when the folder
is ready and the node reports it mounted at its current generation. The daemon creates both
directories, adds them to Claude's `additionalDirectories` and sandbox `allowWrite`, and to Codex's
workspace-write `writable_roots`, exports the two variables, and appends a short note to the system
prompt.

### Session confinement (opt-in)

`AGENT_TEAMS_CONFINE=on` (node environment) runs every employee session (Claude Code, codex
app-server, agy) under `setpriv --no-new-privs` (and, when the daemon runs as root, with an empty
capability bounding set): setuid programs such as `fusermount3` and `sudo` give nothing, so a
session cannot use the container's `SYS_ADMIN`. Off by default, so employees can `sudo` to install
software where the node allows it.

## Capacity and errors

Over the capacity a write fails: on RustFS the upload is refused by the bucket quota, on B2 the key
cannot write. geesefs reports it when the file is flushed, usually as an I/O error (`EIO`) from
`close` or `fsync` rather than from `write`. The 共享文件夹 page shows the usage and, on B2, that the
folder is read-only.

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

The console never mounts a folder: it reads and writes it over S3 with the platform key
(`userfs.Objects`, `userfs.S3Files`, keys under the folder's prefix). Bodies that cannot be rewound
(attachment readers, object-store downloads) are uploaded with an unsigned payload. Through it:

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
`UserDeleting` → `Service.Disable`). The folder is marked deleting, `fs.unmount` goes to the
user's nodes, and the deletion runs in the background:

1. revoke the user's key (every node loses the folder at once);
2. wait (60 s at most) until no node whose daemon mounts the folder still reports it mounted (an
   external mount cannot unmount on request; it is logged);
3. delete every object version and unfinished upload in the folder (rustfs: then the bucket), and
   the row.

## Moving old data in

`atctl storage import-shared` copies the old platform shared files (`at_shared_files`) into each
owner's `projects/<pid>/`, switching the folder on where needed and skipping files already there
with the same size; then it moves deliverable versions kept in the database into the folders of
owners whose folder is on. Both steps can be re-run. File bytes are read one file at a time, and a
user whose folder cannot be switched on counts as one failure. A later migration drops `at_shared_files` and
`at_shared_uploads`.

## Consistency

geesefs gives close-to-open consistency with a 5 s stat cache, and no hard links or file locks.
Git repositories, live databases (a PostgreSQL / MySQL data directory, an SQLite or DuckDB file in
use) and `node_modules` do not belong in the folder: without locks two nodes can corrupt them, and
every small write re-uploads the whole object. Employees are told how the folder works rather than
given rules (`sharedFolderPrompt` in `node/src/fs/session.ts`), with the usual way out as a hint:
keep such things on the node's local disk and put a snapshot in the folder when another node needs
it, to be copied back to a local disk before use.

## Not in v1

- Per-project quotas and a tool fallback for nodes without a mount.
