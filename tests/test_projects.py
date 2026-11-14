"""Projects, Git connections and the per-project workspace (in-memory services)."""

from __future__ import annotations

from collections.abc import Iterator
from typing import Any

import pytest
from fastapi.testclient import TestClient

from agent_teams.server.accounts import MemoryAccounts
from agent_teams.server.app import create_app
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.github import GitHubError, parse_repo
from agent_teams.server.platform import Platform, Services
from agent_teams.server.projects import MemoryProjects
from agent_teams.server.secrets import SecretBox
from agent_teams.server.store import MemoryStore
from agent_teams.server.workspaces import git_env

from .test_api import TEAM, add_node, signup

TOKEN = "github_pat_valid"


class FakeGitHub:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str]] = []

    async def login(self, token: str) -> str:
        self._check(token)
        return "octocat"

    async def repos(self, token: str) -> list[dict[str, Any]]:
        self._check(token)
        return [{"full_name": "octo/app", "private": True, "default_branch": "trunk"}]

    async def repo(self, token: str, slug: str) -> dict[str, Any]:
        self._check(token)
        self.calls.append(("repo", slug))
        if slug != "octo/app":
            raise GitHubError("找不到这个仓库，或者令牌没有它的访问权限")
        return {"full_name": "octo/app", "private": True, "default_branch": "trunk"}

    def _check(self, token: str) -> None:
        if token != TOKEN:
            raise GitHubError("GitHub 拒绝了这个令牌（无效、过期或权限不够）")


@pytest.fixture
def client() -> Iterator[TestClient]:
    async def services() -> Services:
        return Services(
            MemoryStore(),
            MemoryAccounts(),
            MemoryBroker(),
            "http://console.example:3001",
            projects=MemoryProjects(),
            secrets=SecretBox("test-secret"),
            github=FakeGitHub(),
        )

    with TestClient(create_app(services)) as test_client:
        yield test_client


def connect_github(client: TestClient, headers: dict[str, str]) -> dict[str, Any]:
    response = client.post("/api/git/connections", json={"token": TOKEN}, headers=headers)
    assert response.status_code == 201, response.text
    return dict(response.json())


@pytest.mark.parametrize(
    "text",
    [
        "https://github.com/octo/app",
        "https://github.com/octo/app.git",
        "https://github.com/octo/app/tree/dev",
        "git@github.com:octo/app.git",
        "octo/app",
    ],
)
def test_parse_repo_accepts_common_spellings(text: str) -> None:
    assert parse_repo(text) == "octo/app"


def test_parse_repo_rejects_other_input() -> None:
    with pytest.raises(GitHubError):
        parse_repo("https://gitlab.com/x")


def test_git_connections_keep_the_token_secret(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    bad = client.post("/api/git/connections", json={"token": "nope"}, headers=alice)
    assert bad.status_code == 422

    connection = connect_github(client, alice)
    assert connection["login"] == "octocat"
    assert connection["name"] == "GitHub · octocat"
    assert "token" not in connection
    listed = client.get("/api/git/connections", headers=alice).json()
    assert [c["id"] for c in listed] == [connection["id"]]
    repos = client.get(f"/api/git/connections/{connection['id']}/repos", headers=alice).json()
    assert repos[0]["full_name"] == "octo/app"

    bob = signup(client, "bob@example.com")
    assert client.get("/api/git/connections", headers=bob).json() == []
    other = client.get(f"/api/git/connections/{connection['id']}/repos", headers=bob)
    assert other.status_code == 404


def test_projects_link_a_repository_and_scope_conversations(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    connection = connect_github(client, alice)

    no_connection = client.post(
        "/api/projects", json={"name": "app", "repo": "octo/app"}, headers=alice
    )
    assert no_connection.status_code == 422
    unknown = client.post(
        "/api/projects",
        json={"name": "app", "repo": "octo/missing", "git_connection_id": connection["id"]},
        headers=alice,
    )
    assert unknown.status_code == 422

    created = client.post(
        "/api/projects",
        json={
            "name": "app",
            "context": "用 pnpm",
            "repo": "https://github.com/octo/app",
            "git_connection_id": connection["id"],
        },
        headers=alice,
    )
    assert created.status_code == 201, created.text
    project = created.json()
    assert (project["repo"], project["branch"]) == ("octo/app", "trunk")
    duplicate = client.post("/api/projects", json={"name": "app"}, headers=alice)
    assert duplicate.status_code == 409

    renamed = client.patch(
        f"/api/projects/{project['id']}", json={"description": "主站"}, headers=alice
    ).json()
    assert (renamed["description"], renamed["repo"]) == ("主站", "octo/app")
    cleared = client.patch(
        f"/api/projects/{project['id']}", json={"clear_repo": True}, headers=alice
    ).json()
    assert cleared["repo"] is None

    talk = client.post(
        "/api/conversations", json={"title": "t", "project_id": project["id"]}, headers=alice
    ).json()
    client.post("/api/conversations", json={"title": "loose"}, headers=alice)
    scoped = client.get(
        "/api/conversations", params={"project_id": project["id"]}, headers=alice
    ).json()
    assert [c["id"] for c in scoped] == [talk["id"]]
    assert scoped[0]["project_id"] == project["id"]

    bob = signup(client, "bob@example.com")
    stolen = client.post("/api/conversations", json={"project_id": project["id"]}, headers=bob)
    assert stolen.status_code == 404
    assert client.get(f"/api/projects/{project['id']}", headers=bob).status_code == 404


async def test_project_team_works_in_the_repository_with_context(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    add_node(client, alice, "box")
    assert client.put("/api/team", json={"config": TEAM}, headers=alice).status_code == 200
    connection = connect_github(client, alice)
    project = client.post(
        "/api/projects",
        json={
            "name": "app",
            "context": "先跑 make test",
            "repo": "octo/app",
            "git_connection_id": connection["id"],
        },
        headers=alice,
    ).json()

    state: Platform = client.app.state.platform  # type: ignore[attr-defined]
    me = client.get("/api/me", headers=alice).json()
    lead = await state.workspaces.lead(me["id"], project["id"])

    coder = lead.members[0]
    node = coder.node  # type: ignore[attr-defined]
    assert node.workdir == f"projects/{project['id'][:12]}"
    assert node.repo == {"url": "https://github.com/octo/app.git", "branch": "trunk"}
    assert node.env["GH_TOKEN"] == TOKEN
    assert "先跑 make test" in coder.instructions  # type: ignore[attr-defined]
    assert "octo/app" in lead.brain.instructions  # type: ignore[union-attr]

    plain = await state.workspaces.lead(me["id"])
    assert plain.members[0].node.workdir == ""  # type: ignore[attr-defined]


def test_structured_team_round_trip(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    add_node(client, alice, "box")
    team = {
        "leader": "lead",
        "employees": [
            {"name": "lead", "type": "claude_code", "node": "box", "description": "负责人"},
            {"name": "dev", "type": "claude_code", "node": "box", "max_turns": 30},
        ],
    }
    saved = client.put("/api/team", json={"team": team}, headers=alice)
    assert saved.status_code == 200, saved.text
    assert "负责人" in saved.json()["config"]
    assert client.get("/api/team", headers=alice).json()["team"] == team


def test_git_env_routes_credentials_through_the_environment() -> None:
    env = git_env("t0k")
    assert env["GH_TOKEN"] == "t0k"
    assert env["GIT_CONFIG_KEY_1"] == "credential.https://github.com.helper"
    assert "$GH_TOKEN" in env["GIT_CONFIG_VALUE_1"]
    assert git_env("") == {}
