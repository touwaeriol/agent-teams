"""Small text helpers shared by tracing and task summaries."""


def clip(text: str, limit: int = 120) -> str:
    """Collapse whitespace and cut to ``limit`` characters."""
    flat = " ".join(text.split())
    return flat if len(flat) <= limit else f"{flat[: limit - 1]}…"
