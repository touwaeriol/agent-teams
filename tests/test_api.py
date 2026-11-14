"""The multi-user HTTP API (in-memory services)."""

from __future__ import annotations

from collections.abc import Iterator
from typing import Any

import pytest
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from agent_teams.server.accounts import MemoryAccounts
from agent_teams.server.app import create_app
from agent_teams.server.auth import SESSION_COOKIE
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.platform import Services
from agent_teams.server.store import MemoryStore

PASSWORD = "correct horse"
TEAM = """\
leader: lead
employees:
  - {name: lead, type: claude_code, node: box}
  - {name: coder, type: claude_code, node: box, description: writes code}
"""


@pytest.fixture
def client() -> Iterator[TestClient]:
    async def services() -> Services:
        return Services(
            MemoryStore(),
            MemoryAccounts(),
            MemoryBroker(),
            "http://console.example:3001",
            frozenset({"boss@example.com"}),
        )

    with TestClient(create_app(services)) as test_client:
        yield test_client


def signup(client: TestClient, email: str, password: str = PASSWORD) -> dict[str, str]:
    """Register and return bearer headers for that user (cookies cleared)."""
    response = client.post("/auth/register", json={"email": email, "password": password})
    assert response.status_code == 201, response.text
    token = response.cookies[SESSION_COOKIE]
    client.cookies.clear()
    return {"Authorization": f"Bearer {token}"}


def add_node(client: TestClient, headers: dict[str, str], name: str = "box") -> dict[str, Any]:
    response = client.post("/api/nodes", json={"name": name}, headers=headers)
    assert response.status_code == 201, response.text
    return dict(response.json())


def test_first_user_is_admin_and_registration_rules(client: TestClient) -> None:
    admin = signup(client, "First@Example.com")
    assert client.get("/api/me", headers=admin).json()["role"] == "admin"
    assert client.get("/api/me", headers=admin).json()["email"] == "first@example.com"

    user = signup(client, "second@example.com")
    assert client.get("/api/me", headers=user).json()["role"] == "user"
    assert (
        client.get("/api/me", headers=signup(client, "boss@example.com")).json()["role"] == "admin"
    )

    duplicate = client.post(
        "/auth/register", json={"email": "second@example.com", "password": PASSWORD}
    )
    weak = client.post("/auth/register", json={"email": "x@example.com", "password": "short"})
    invalid = client.post("/auth/register", json={"email": "not-an-email", "password": PASSWORD})
    assert duplicate.status_code == 409
    assert weak.status_code == 422
    assert invalid.status_code == 422

    settings = {"registration_open": False, "default_max_nodes": 5}
    assert client.put("/api/admin/settings", json=settings, headers=admin).status_code == 200
    closed = client.post("/auth/register", json={"email": "late@example.com", "password": PASSWORD})
    assert closed.status_code == 403


def test_login_logout_and_sessions(client: TestClient) -> None:
    signup(client, "a@example.com")
    assert client.get("/api/me").status_code == 401

    wrong = client.post("/auth/login", json={"email": "a@example.com", "password": "nope nope"})
    assert wrong.status_code == 401
    ok = client.post("/auth/login", json={"email": "A@example.com", "password": PASSWORD})
    assert ok.status_code == 200
    assert "httponly" in ok.headers["set-cookie"].lower()
    assert client.get("/api/me").json()["email"] == "a@example.com"  # cookie session

    assert client.post("/auth/logout").status_code == 204
    client.cookies.clear()
    assert client.get("/api/me").status_code == 401


def test_users_cannot_see_each_others_data(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    bob = signup(client, "bob@example.com")
    conversation = client.post("/api/conversations", json={"title": "secret"}, headers=alice).json()
    node = add_node(client, alice)["node"]
    base = f"/api/conversations/{conversation['id']}"

    assert client.get("/api/conversations", headers=bob).json() == []
    assert client.get(f"{base}/messages", headers=bob).status_code == 404
    assert client.get(f"{base}/tasks", headers=bob).status_code == 404
    assert client.get(f"{base}/events", headers=bob).status_code == 404
    assert client.post(f"{base}/messages", json={"content": "x"}, headers=bob).status_code == 404
    assert client.get("/api/nodes", headers=bob).json() == []
    assert client.delete(f"/api/nodes/{node['id']}", headers=bob).status_code == 404
    assert client.post(f"/api/nodes/{node['id']}/token", headers=bob).status_code == 404
    assert [c["title"] for c in client.get("/api/conversations", headers=alice).json()] == [
        "secret"
    ]


def test_nodes_tokens_install_and_quota(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    added = add_node(client, alice, "gpu-1")

    assert len(added["token"]) > 30
    assert added["install"].startswith("curl -fsSL http://console.example:3001/install-node.sh |")
    assert f"AGENT_TEAMS_NODE_TOKEN={added['token']}" in added["install"]
    assert "--name gpu-1" in added["install"]
    (listed,) = client.get("/api/nodes", headers=alice).json()
    assert (listed["name"], listed["online"]) == ("gpu-1", False)
    assert "token" not in listed

    assert client.post("/api/nodes", json={"name": "gpu-1"}, headers=alice).status_code == 409
    assert client.post("/api/nodes", json={"name": "bad name!"}, headers=alice).status_code == 422
    rotated = client.post(f"/api/nodes/{listed['id']}/token", headers=alice).json()
    assert rotated["token"] != added["token"]

    for number in range(2, 6):
        add_node(client, alice, f"gpu-{number}")
    over = client.post("/api/nodes", json={"name": "gpu-6"}, headers=alice)
    assert over.status_code == 403
    assert "5" in over.json()["detail"]

    assert client.delete(f"/api/nodes/{listed['id']}", headers=alice).status_code == 204
    assert len(client.get("/api/nodes", headers=alice).json()) == 4


def test_team_definition_is_validated_against_the_users_nodes(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    initial = client.get("/api/team", headers=alice).json()
    assert initial["saved"] is False
    assert "leader: lead" in initial["config"]

    conversation = client.post("/api/conversations", json={}, headers=alice).json()
    blocked = client.post(
        f"/api/conversations/{conversation['id']}/messages", json={"content": "hi"}, headers=alice
    )
    assert blocked.status_code == 409  # no team yet

    missing_node = client.put("/api/team", json={"config": TEAM}, headers=alice)
    assert missing_node.status_code == 422
    assert "unknown node 'box'" in missing_node.json()["detail"]

    add_node(client, alice, "box")
    saved = client.put("/api/team", json={"config": TEAM}, headers=alice)
    assert saved.status_code == 200, saved.text
    assert client.get("/api/team", headers=alice).json() == {
        "config": TEAM,
        "saved": True,
        "error": "",
    }

    agent_leader = TEAM.replace("{name: lead, type: claude_code, node: box}", "{name: lead}")
    rejected = client.put("/api/team", json={"config": agent_leader}, headers=alice)
    assert rejected.status_code == 422
    assert "claude_code" in rejected.json()["detail"]
    assert client.put("/api/team", json={"config": "a: [1"}, headers=alice).status_code == 422
    with_nodes = "nodes: [{name: x, local: true}]\n" + TEAM
    assert (
        "节点"
        in client.put("/api/team", json={"config": with_nodes}, headers=alice).json()["detail"]
    )


def test_admin_manages_users(client: TestClient) -> None:
    admin = signup(client, "admin@example.com")
    user = signup(client, "user@example.com")
    assert client.get("/api/admin/users", headers=user).status_code == 403
    add_node(client, user)

    users = {u["email"]: u for u in client.get("/api/admin/users", headers=admin).json()}
    target = users["user@example.com"]
    assert target["nodes"] == 1

    patched = client.patch(f"/api/admin/users/{target['id']}", json={"max_nodes": 1}, headers=admin)
    assert patched.json()["max_nodes"] == 1
    assert client.post("/api/nodes", json={"name": "second"}, headers=user).status_code == 403

    disabled = client.patch(
        f"/api/admin/users/{target['id']}", json={"active": False}, headers=admin
    )
    assert disabled.json()["active"] is False
    assert client.get("/api/me", headers=user).status_code == 401
    login = client.post("/auth/login", json={"email": "user@example.com", "password": PASSWORD})
    assert login.status_code == 403

    me = client.get("/api/me", headers=admin).json()
    demote = client.patch(f"/api/admin/users/{me['id']}", json={"role": "user"}, headers=admin)
    assert demote.status_code == 400
    assert client.delete(f"/api/admin/users/{me['id']}", headers=admin).status_code == 400
    assert client.delete(f"/api/admin/users/{target['id']}", headers=admin).status_code == 204
    assert [u["email"] for u in client.get("/api/admin/users", headers=admin).json()] == [
        "admin@example.com"
    ]


def test_node_socket_authenticates_by_token_and_rotation_disconnects(client: TestClient) -> None:
    alice = signup(client, "alice@example.com")
    added = add_node(client, alice, "box")
    node_id = added["node"]["id"]

    with (
        pytest.raises(WebSocketDisconnect),
        client.websocket_connect("/nodes/connect", headers={"Authorization": "Bearer wrong"}),
    ):
        pass

    headers = {"Authorization": f"Bearer {added['token']}"}
    with client.websocket_connect("/nodes/connect", headers=headers) as socket:
        socket.send_json({"type": "hello", "host": "box-host", "max_runs": 2, "active": []})
        status = next(
            n for n in client.get("/api/nodes", headers=alice).json() if n["id"] == node_id
        )
        assert (status["online"], status["host"]) == (True, "box-host")

        client.post(f"/api/nodes/{node_id}/token", headers=alice)  # revoke
        with pytest.raises(WebSocketDisconnect):
            socket.receive_json()

    status = next(n for n in client.get("/api/nodes", headers=alice).json() if n["id"] == node_id)
    assert status["online"] is False


def test_static_pages(client: TestClient) -> None:
    assert "Agent Teams" in client.get("/").text
    script = client.get("/install-node.sh")
    assert script.headers["content-type"].startswith("text/x-shellscript")
    assert "agent-teams node" in script.text
    assert client.get("/healthz").json() == {"ok": True}
    assert client.post("/mcp", json={}).status_code == 401
