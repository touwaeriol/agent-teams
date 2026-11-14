"""Each user's team, built from their team YAML and their registered nodes.

On the platform both the leader and the employees are Claude Code sessions
on the user's own nodes; no platform API key is involved.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from ..claude_code import ClaudeCodeWorker
from ..config import build_team, parse_yaml
from ..errors import ConfigError
from ..leader import Leader
from ..llm.base import LLM
from ..remote import EnvSource, NodeRegistry, RemoteNode
from .accounts import AccountStore
from .credentials import GitCredentials
from .models import NodeView, ProjectView
from .projects import ProjectStore

DEFAULT_TEAM = """\
# 你的团队：leader 和员工都是你节点上的 Claude Code。
# 先在"节点"页添加节点并安装守护进程，再把下面的 node 改成你的节点名。
leader: lead
employees:
  - name: lead
    type: claude_code
    node: my-node
    description: 负责人，直接和你对话
    instructions: 你是技术负责人。把需求拆成任务派给工程师，检查结果后用中文汇报。
  - name: engineer
    type: claude_code
    node: my-node
    description: 工程师：改代码、跑测试
    allowed_tools: ["Bash(git *)"]
"""


def _no_platform_llm(_options: Mapping[str, Any]) -> LLM:
    raise ConfigError(
        "平台上的 leader 和员工都运行在你的节点上，请使用 type: claude_code"
        "（type: agent 需要平台 API key，这里不提供）"
    )


class Workspaces:
    def __init__(
        self,
        accounts: AccountStore,
        registry: NodeRegistry,
        projects: ProjectStore | None = None,
        credentials: GitCredentials | None = None,
    ) -> None:
        self._accounts = accounts
        self._registry = registry
        self._projects = projects
        self._credentials = credentials
        self._cache: dict[tuple[str, str], tuple[str, Leader]] = {}

    async def lead(self, user_id: str, project_id: str | None = None) -> Leader:
        """The user's leader (for a project, if given), rebuilt when anything it uses changes."""
        config = await self._accounts.team(user_id)
        if config is None:
            raise ConfigError('还没有配置团队：请先在"团队"页填写并保存')
        nodes = await self._accounts.nodes(user_id)
        project = await self._project(user_id, project_id)
        fingerprint = "|".join(
            [config, ",".join(f"{n.id}:{n.name}" for n in nodes), _project_key(project)]
        )
        key = (user_id, project_id or "")
        cached = self._cache.get(key)
        if cached is not None and cached[0] == fingerprint:
            return cached[1]
        workspace = await self._workspace(user_id, project)
        lead = self.build(config, nodes, workspace)
        self._cache[key] = (fingerprint, lead)
        return lead

    def build(
        self, config: str, nodes: list[NodeView], workspace: ProjectWorkspace | None = None
    ) -> Leader:
        """Validate and build a team definition against the user's nodes."""
        data = parse_yaml(config, "团队配置")
        if "nodes" in data:
            raise ConfigError('节点在"节点"页管理，团队配置里不要写 nodes')
        space = workspace or ProjectWorkspace()
        remote = {
            n.name: RemoteNode(
                n.name,
                self._registry,
                workdir=space.workdir,
                key=n.id,
                env_source=space.env_source,
                repo=space.repo,
            )
            for n in nodes
        }
        lead = build_team(space.brief(data), _no_platform_llm, remote)
        if not isinstance(lead, Leader) or not isinstance(lead.brain, ClaudeCodeWorker):
            raise ConfigError("团队需要用 leader 指定负责人，并且负责人要是 type: claude_code")
        return lead

    async def _project(self, user_id: str, project_id: str | None) -> ProjectView | None:
        if project_id is None or self._projects is None:
            return None
        project = await self._projects.project(user_id, project_id)
        if project is None:
            raise ConfigError("这个对话所属的项目已经不存在了")
        return project

    async def _workspace(self, user_id: str, project: ProjectView | None) -> ProjectWorkspace:
        if project is None:
            return ProjectWorkspace()
        return ProjectWorkspace.of(project, self._env_source(user_id, project.git_connection_id))

    def _env_source(self, user_id: str, connection_id: str | None) -> EnvSource | None:
        """Fetches the Git environment at the start of every run, so short-lived
        (GitHub App) tokens are always fresh."""
        credentials = self._credentials
        if connection_id is None or credentials is None:
            return None

        async def env() -> Mapping[str, str]:
            return git_env(await credentials.token(user_id, connection_id))

        return env


def _project_key(project: ProjectView | None) -> str:
    return f"{project.id}@{project.updated_at.isoformat()}" if project else ""


@dataclass(frozen=True)
class ProjectWorkspace:
    """Where a project's team works on its nodes, and what it knows about the project.

    Every employee gets its own clone of the project's repository in
    ``projects/<project id>/<employee>`` (the daemon clones it before the
    first run). The Git token reaches git and gh through the run's
    environment only; it is never written to disk.
    """

    workdir: str = ""
    env_source: EnvSource | None = None
    repo: Mapping[str, str] | None = None
    context: str = ""

    @classmethod
    def of(cls, project: ProjectView, env_source: EnvSource | None) -> ProjectWorkspace:
        repo = None
        if project.repo:
            repo = {"url": f"https://github.com/{project.repo}.git", "branch": project.branch}
        return cls(f"projects/{project.id[:12]}", env_source, repo, _context(project))

    def brief(self, data: Mapping[str, Any]) -> Mapping[str, Any]:
        """The team definition with the project context added to every employee."""
        if not self.context:
            return data
        employees = data.get("employees")
        if not isinstance(employees, list):
            return data
        briefed = [_with_context(e, self.context) for e in employees]
        return {**data, "employees": briefed}


def _with_context(employee: Any, context: str) -> Any:
    if not isinstance(employee, Mapping):
        return employee
    own = str(employee.get("instructions") or "")
    return {**employee, "instructions": "\n\n".join(p for p in (own, context) if p)}


def _context(project: ProjectView) -> str:
    lines = [f"# 项目：{project.name}"]
    if project.description:
        lines.append(project.description)
    if project.repo:
        branch = project.branch or "默认分支"
        lines.append(
            f"仓库：https://github.com/{project.repo}（基准分支 {branch}）。"
            "你的工作目录就是这个仓库的独立克隆；"
            "git 和 gh 已经配好凭证，可以直接拉取、推送和建 PR。"
            "不要直接推送基准分支，改动放在新分支上。"
        )
    if project.context:
        lines.append("## 项目上下文\n" + project.context)
    return "\n\n".join(lines)


_HELPER = '!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f'


def git_env(token: str) -> dict[str, str]:
    """Environment that lets git (HTTPS) and gh use ``token`` for github.com."""
    if not token:
        return {}
    return {
        "GH_TOKEN": token,
        "GITHUB_TOKEN": token,
        "GIT_TERMINAL_PROMPT": "0",
        "GIT_CONFIG_COUNT": "2",
        "GIT_CONFIG_KEY_0": "credential.https://github.com.helper",
        "GIT_CONFIG_VALUE_0": "",
        "GIT_CONFIG_KEY_1": "credential.https://github.com.helper",
        "GIT_CONFIG_VALUE_1": _HELPER,
    }
