"""Exception hierarchy and user-facing error descriptions."""

import anthropic

AUTH_HINT = "set ANTHROPIC_API_KEY, or run `ant auth login` once to sign in"


class AgentTeamsError(Exception):
    """Base class for all errors raised by this package."""


class LLMError(AgentTeamsError):
    """The model did not produce a usable reply (refusal, truncation, ...)."""


class MaxStepsExceeded(AgentTeamsError):  # noqa: N818 - reads better as an event name
    """An agent kept calling tools beyond its step budget."""


class ConfigError(AgentTeamsError):
    """A team definition is invalid."""


def describe_error(exc: BaseException) -> str:
    """A one-line explanation suitable for showing to a user."""
    if isinstance(exc, anthropic.AuthenticationError):
        return f"Claude rejected the credentials ({exc}); {AUTH_HINT}"
    return str(exc) or type(exc).__name__
