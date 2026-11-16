"""Seed a v1 database with rows written by the real v1 Python code (agent_teams.server).

Run against a database loaded with the production v1 schema (pg_dump -s), before
`atctl migrate up`. Prints JSON with the plaintext credentials so the caller can check that
the v2 control plane accepts them:

    DATABASE_URL=... AGENT_TEAMS_SECRET_KEY=... python v1_seed.py > v1.json
"""

from __future__ import annotations

import asyncio
import json
import os
import uuid

import asyncpg

from agent_teams.server.accounts import PostgresAccounts
from agent_teams.server.projects import PostgresProjects
from agent_teams.server.secrets import SecretBox
from agent_teams.server.security import hash_password, new_token
from agent_teams.server.store import PostgresStore

EMAIL = "v1-user@example.com"
MEMBER_EMAIL = "v1-member@example.com"
PASSWORD = "v1-password-Correct-Horse"
GIT_TOKEN = "ghp_v1SealedToken0123456789abcdef"
TEAM_YAML = """name: v1-team
leader: lead
employees:
  - name: lead
    type: claude_code
    description: Team lead
    instructions: You lead the team.
  - name: dev
    type: claude_code
    description: Developer
    instructions: You write code.
"""


async def seed(pool: asyncpg.Pool, box: SecretBox) -> dict[str, str]:
    accounts, projects, store = PostgresAccounts(pool), PostgresProjects(pool), PostgresStore(pool)
    user = await accounts.create_user(EMAIL, hash_password(PASSWORD), "admin")
    member = await accounts.create_user(MEMBER_EMAIL, hash_password(PASSWORD), "user")
    session_token, session_hash = new_token()
    await accounts.create_session(user.id, session_hash)
    node_token, node_hash = new_token()
    node = await accounts.create_node(user.id, "v1-node", node_hash)
    await accounts.save_team(user.id, TEAM_YAML)
    conn = await projects.create_connection(user.id, "v1-pat", "local", box.seal(GIT_TOKEN))
    project = await projects.create_project(
        user.id,
        {"name": "v1 project", "repo": "local/v1repo", "branch": "main", "git_connection_id": conn.id},
    )
    conversation_id = uuid.uuid4().hex
    await store.create_conversation(conversation_id, "v1 conversation", user.id, project.id)
    await store.add_message(conversation_id, "user", "hello from v1")
    await store.add_message(conversation_id, "leader", "v1 reply")
    return {
        "email": EMAIL,
        "password": PASSWORD,
        "user_id": user.id,
        "member_email": MEMBER_EMAIL,
        "member_id": member.id,
        "session_token": session_token,
        "node_id": node.id,
        "node_token": node_token,
        "connection_id": conn.id,
        "git_token": GIT_TOKEN,
        "project_id": project.id,
        "conversation_id": conversation_id,
    }


async def main() -> None:
    box = SecretBox(os.environ["AGENT_TEAMS_SECRET_KEY"])
    pool = await asyncpg.create_pool(os.environ["DATABASE_URL"])
    try:
        print(json.dumps(await seed(pool, box), indent=2))
    finally:
        await pool.close()


if __name__ == "__main__":
    asyncio.run(main())
