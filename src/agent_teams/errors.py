"""Exception hierarchy."""


class AgentTeamsError(Exception):
    """Base class for all errors raised by this package."""


class LLMError(AgentTeamsError):
    """The model did not produce a usable reply (refusal, truncation, ...)."""


class MaxStepsExceeded(AgentTeamsError):  # noqa: N818 - reads better as an event name
    """An agent kept calling tools beyond its step budget."""


class ConfigError(AgentTeamsError):
    """A team definition is invalid."""
