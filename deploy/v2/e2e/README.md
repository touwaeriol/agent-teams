# Local end-to-end test of v2 (rc)

Runs the whole system on this machine: compose stack (PostgreSQL 17, Valkey, `atctl migrate`,
control plane) on a database that starts as the **production v1 schema with rows written by the
v1 Python code**, a GitHub REST emulator over local bare repositories, and the real node daemon
(installed from the tarball the control plane serves) with real Claude employees.

```bash
S=/tmp/claude-1000/<session>/scratchpad/e2e        # any scratch dir, never the repository
docker build -f server/Dockerfile -t agent-teams-server:e2e .
deploy/v2/e2e/up.sh "$S" /path/to/v1-schema.sql     # writes deploy/v2/.env.e2e (git-ignored), seeds v1 rows
node deploy/v2/e2e/v1_check.mjs "$S/v1.json"        # v1 login, session, node token, sealed token, team, conversations
node deploy/v2/e2e/run.mjs "$S"                     # coordinator → thread → lead → dev → approval → push → merge queue
node deploy/v2/e2e/run_multi.mjs "$S"               # two repos (base of one only in the context) + designer publish_file
node deploy/v2/e2e/run_lifecycle.mjs "$S"           # image to the coordinator, live status, archive / unarchive / delete (haiku)
node deploy/v2/e2e/run_bypass.mjs "$S"              # default bypass: Bash and writes with no approval card, ask_user still reaches the user (haiku)
node deploy/v2/e2e/run_permissions.mjs "$S"         # an ask role: lead review, a request the lead hands to the user (haiku)
node deploy/v2/e2e/run_restart.mjs "$S"             # daemon restart while a card waits: resume, same card reattached (haiku)
node deploy/v2/e2e/run_resume.mjs "$S"              # code word survives park, SIGTERM, SIGKILL, console restart, lost transcript; question cards (haiku)
# screenshots (Playwright image, NODE_PATH with the playwright package):
node deploy/v2/e2e/screenshots.cjs http://127.0.0.1:<PORT> <out> <project-id> 1
node deploy/v2/e2e/screenshots_multi.cjs http://127.0.0.1:<PORT> <out> <project-id> 1 <deliverable-id>   # ids in $S/multi-state.json
ENV_FILE=deploy/v2/.env.e2e deploy/v2/compose.sh -f deploy/v2/docker-compose.e2e.yml down
node --test deploy/v2/e2e/localgh.test.mjs          # the emulator's own tests
```

| File | What |
| --- | --- |
| `up.sh` | env file with random secrets and a free port 3470–3499, bare repos `local/hello`, `local/v1repo`, `local/web` and `local/api` (with a `develop` branch), v1 schema load, `v1_seed.py`, then `up --wait` |
| `v1_seed.py` | creates a user, session, node, team YAML, sealed PAT connection, project and conversation through `agent_teams.server` (the v1 code) |
| `v1_check.mjs` | proves v2 accepts all of it (the PAT is checked by localgh, which accepts only that token) |
| `localgh.mjs` | refs, merges (`git merge-tree`), pull requests (numbered per repository, base must exist), contents, empty check runs; `LOCALGH_TOKENS` restricts tokens |
| `harness.mjs` | login, daemon install/start, approval answering, git and SQL helpers shared by the runs |
| `run_multi.mjs` | project on `local/web` + `local/api` (`develop` stated only in the project context), CI off, the "设计师" preset; one request touching both repositories plus a design: checks per-repository bases, lazy thread branches, files only in their own repository, one PR per repository (web → main, api → develop), acceptance merging both, and the published page served from a signed URL with a CSP sandbox (tampered signature → 403). Roles use `--model haiku` by default; about 15 minutes |
| `screenshots_multi.cjs` | New Project with two repositories, Team skills, Deliverables, the viewer, the thread and its delivery tab |
| `run.mjs` | the real-Claude run; the daemon runs with XDG dirs under the scratch dir and never reads `~/.config/agent-teams/node.env`. Roles use `--model sonnet` by default to keep it cheap. `E2E_DAEMON=node/dist/node.mjs` runs a local build instead of the served tarball |
| `run_lifecycle.mjs` | a solid red PNG sent to the coordinator (the model must name the colour), a thread whose status events and live member states are checked while an approval card waits, then archive (sessions closed), unarchive, archive and delete (404, rows gone, thread directory purged on the node). It pauses at `approval` and `archived` until `<scratch>/go-<name>` exists, for screenshots; about 5 minutes |
| `run_bypass.mjs` | the dev in the default `bypass` mode on a plain (not isolated) node runs Bash, writes in its worktree and outside it with zero approval rows in `at_questions` and no `approval.request` from the daemon; its `ask_user` question reaches the user, is answered through the API, and the dev writes the answer down and completes its task; about 3 minutes |
| `run_permissions.mjs` | the dev is an `ask` role; its first write goes to the lead, which allows it (the card never reaches the user); a second one the lead hands over (`ask_user`) becomes the user's card, answered through the API, and the dev completes its task. Pauses at `running` and `escalated` until `<scratch>/go-<name>` exists; about 3 minutes |
| `run_restart.mjs` | the dev's write outside its worktree raises the user's card; the daemon gets SIGTERM and is started again while the card is open; the dev receives the restart note, re-issues the write, the same card is attached to the new request (never cancelled, no second card), the API answers it and the dev completes its task; about 3 minutes |
| `run_resume.mjs` | the coordinator is told a code word, then its session is parked (`park_after_s`), the daemon is stopped with SIGTERM and with SIGKILL and started again, the control plane restarts, and the session transcript is deleted on the node (resume fails → a fresh session seeded with the recap, a `resumed` note in the chat); after each one it must answer the code word. Then Claude's own AskUserQuestion becomes a blocking platform card (`detail.via`) and a structured `ask_user` card, both answered through the API with per-question choices, and the coordinator repeats the choice. `--only park,sigterm,…` runs a subset; about 10 minutes |
| `screenshots.cjs` | Main, thread panel, full thread page, Office, Team, Nodes at deviceScaleFactor 2 (signs in once: `/auth/login` is rate limited per account) |

With no CI on the test repository the merge queue waits `appear_timeout_s` (300 s) and then
passes (`on_missing: pass`), so a full run takes about 7 minutes.
