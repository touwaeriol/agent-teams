"""Example tools. Any plain function works; ``@tool`` is only needed for overrides."""

from __future__ import annotations

from datetime import date


def word_count(text: str) -> int:
    """Count the words in a piece of text.

    Args:
        text: The text to measure.
    """
    return len(text.split())


def today() -> str:
    """Return today's date in ISO format."""
    return date.today().isoformat()
