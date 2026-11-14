"""Projects, Git connections and the per-project workspace (in-memory services)."""

from __future__ import annotations

import base64
import json
import time
from collections.abc import Iterator
from typing import Any
from urllib.parse import parse_qs, urlsplit

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from fastapi.testclient import TestClient

from agent_teams.server.accounts import MemoryAccounts
from agent_teams.server.app import create_app
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.credentials import StateSigner
from agent_teams.server.github import GitHubError, Token, app_jwt, parse_repo
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
        self.minted = 0

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

    async def installation_repos(self, token: str) -> list[dict[str, Any]]:
        assert token.startswith("ghs_")
        return [{"full_name": "octo/app", "private": True, "default_branch": "trunk"}]

    async def convert_manifest(self, code: str) -> dict[str, Any]:
        assert code == "manifest-code"
        return {
            "id": 42,
            "slug": "agent-teams-x",
            "name": "Agent Teams x",
            "owner": {"login": "octo"},
            "html_url": "https://github.com/apps/agent-teams-x",
            "client_id": "Iv1.abc",
            "client_secret": "client-secret",
            "pem": "-----BEGIN RSA PRIVATE KEY-----",
        }

    async def installation_token(self, app_id: int, pem: str, installation_id: int) -> Token:
        assert (app_id, pem) == (42, "-----BEGIN RSA PRIVATE KEY-----")
        self.minted += 1
        return Token(f"ghs_{installation_id}_{self.minted}", time.time() + 3600)

    async def user_token(self, client_id: str, client_secret: str, code: str) -> str:
        assert (client_id, client_secret) == ("Iv1.abc", "client-secret")
        return f"user-{code}"

    async def user_installations(self, user_token: str) -> list[dict[str, Any]]:
        return [{"id": 7, "account": {"login": "octo"}}] if user_token == "user-good" else []

    def _check(self, token: str) -> None:
        if token != TOKEN and not token.startswith("ghs_"):
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
    assert (await node.setup())["env"]["GH_TOKEN"] == TOKEN
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


def test_state_signer_binds_user_purpose_and_time() -> None:
    signer = StateSigner(SecretBox("k"))
    state = signer.sign("install", "u1", now=1000)
    assert signer.verify("install", state, now=1100) == "u1"
    assert signer.verify("create", state, now=1100) is None
    assert signer.verify("install", state, now=1000 + 7200) is None
    assert signer.verify("install", state.replace("u1", "u2"), now=1100) is None
    assert StateSigner(SecretBox("other")).verify("install", state, now=1100) is None


def test_app_jwt_is_rs256_signed_for_the_app() -> None:
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    pem = key.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.TraditionalOpenSSL,
        serialization.NoEncryption(),
    ).decode()
    header, claims, _signature = app_jwt(42, pem, now=1000).split(".")
    decoded = json.loads(base64.urlsafe_b64decode(claims + "=="))
    assert json.loads(base64.urlsafe_b64decode(header + "=="))["alg"] == "RS256"
    assert decoded == {"iat": 940, "exp": 1480, "iss": "42"}


def _query(url: str) -> dict[str, str]:
    return {k: v[0] for k, v in parse_qs(urlsplit(url).query).items()}


def test_github_app_one_click_flow(client: TestClient) -> None:
    admin = signup(client, "admin@example.com")
    alice = signup(client, "alice@example.com")
    assert client.get("/api/git/github-app", headers=alice).json() == {"configured": False}
    forbidden = client.post("/api/admin/github-app/manifest", json={}, headers=alice)
    assert forbidden.status_code == 403

    form = client.post(
        "/api/admin/github-app/manifest", json={"organization": "octo"}, headers=admin
    ).json()
    assert form["action"].startswith("https://github.com/organizations/octo/settings/apps/new?")
    manifest = json.loads(form["manifest"])
    assert manifest["redirect_url"].endswith("/api/git/github-app/created")
    assert manifest["default_permissions"]["contents"] == "write"
    state = _query(form["action"])["state"]

    stolen = client.get(
        "/api/git/github-app/created",
        params={"code": "manifest-code", "state": state},
        headers=alice,
        follow_redirects=False,
    )
    assert stolen.status_code == 403  # not an admin
    done = client.get(
        "/api/git/github-app/created",
        params={"code": "manifest-code", "state": state},
        headers=admin,
        follow_redirects=False,
    )
    assert done.headers["location"] == "/admin?github_app=agent-teams-x"
    status = client.get("/api/git/github-app", headers=alice).json()
    assert status["app"]["slug"] == "agent-teams-x"

    go = client.get("/api/git/github-app/install", headers=alice, follow_redirects=False)
    assert go.headers["location"].startswith(
        "https://github.com/apps/agent-teams-x/installations/new?"
    )
    install_state = _query(go.headers["location"])["state"]

    foreign = client.get(
        "/api/git/github-app/installed",
        params={"installation_id": 7, "code": "bad", "state": install_state},
        headers=alice,
        follow_redirects=False,
    )
    assert "error=" in foreign.headers["location"]
    back = client.get(
        "/api/git/github-app/installed",
        params={"installation_id": 7, "code": "good", "state": install_state},
        headers=alice,
        follow_redirects=False,
    )
    assert back.headers["location"].startswith("/settings/git?connected=")
    connection = client.get("/api/git/connections", headers=alice).json()[0]
    assert (connection["kind"], connection["installation_id"], connection["login"]) == (
        "app",
        7,
        "octo",
    )
    again = client.get(
        "/api/git/github-app/installed",
        params={"installation_id": 7, "setup_action": "update", "state": install_state},
        headers=alice,
        follow_redirects=False,
    )
    assert again.headers["location"].startswith("/settings/git?connected=")
    assert len(client.get("/api/git/connections", headers=alice).json()) == 1

    repos = client.get(f"/api/git/connections/{connection['id']}/repos", headers=alice).json()
    assert repos[0]["full_name"] == "octo/app"
    project = client.post(
        "/api/projects",
        json={"name": "app", "repo": "octo/app", "git_connection_id": connection["id"]},
        headers=alice,
    )
    assert project.status_code == 201, project.text
