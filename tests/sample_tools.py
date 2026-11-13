"""Importable tools referenced by config tests."""

from agent_teams import tool


def word_count(text: str) -> int:
    """Count words in text."""
    return len(text.split())


@tool(name="echo_tool")
def echo(text: str) -> str:
    return text


NOT_CALLABLE = 42
