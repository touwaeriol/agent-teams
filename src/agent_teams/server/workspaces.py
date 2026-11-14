"""Each user's team, built from their team YAML and their registered nodes.

On the platform both the leader and the employees are Claude Code sessions
on the user's own nodes; no platform API key is involved.
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from ..claude_code import ClaudeCodeWorker
from ..config import build_team, parse_yaml
from ..errors import ConfigError
from ..leader import Leader
from ..llm.base import LLM
from ..remote import NodeRegistry, RemoteNode
from .accounts import AccountStore
from .models import NodeView

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
    def __init__(self, accounts: AccountStore, registry: NodeRegistry) -> None:
        self._accounts = accounts
        self._registry = registry
        self._cache: dict[str, tuple[str, Leader]] = {}

    async def lead(self, user_id: str) -> Leader:
        """The user's leader, rebuilt whenever their team or nodes change."""
        config = await self._accounts.team(user_id)
        if config is None:
            raise ConfigError('还没有配置团队：请先在"团队"页填写并保存')
        nodes = await self._accounts.nodes(user_id)
        fingerprint = config + "|" + ",".join(f"{n.id}:{n.name}" for n in nodes)
        cached = self._cache.get(user_id)
        if cached is not None and cached[0] == fingerprint:
            return cached[1]
        lead = self.build(config, nodes)
        self._cache[user_id] = (fingerprint, lead)
        return lead

    def build(self, config: str, nodes: list[NodeView]) -> Leader:
        """Validate and build a team definition against the user's nodes."""
        data = parse_yaml(config, "团队配置")
        if "nodes" in data:
            raise ConfigError('节点在"节点"页管理，团队配置里不要写 nodes')
        remote = {n.name: RemoteNode(n.name, self._registry, key=n.id) for n in nodes}
        lead = build_team(data, _no_platform_llm, remote)
        if not isinstance(lead, Leader) or not isinstance(lead.brain, ClaudeCodeWorker):
            raise ConfigError("团队需要用 leader 指定负责人，并且负责人要是 type: claude_code")
        return lead
